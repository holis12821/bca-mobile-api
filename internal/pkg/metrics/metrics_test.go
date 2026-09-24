package metrics

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestRegistry_CounterWithLabels(t *testing.T) {
	r := NewRegistry()

	r.Inc(CardSelected, Labels{"product_type": "TAHAPAN_BCA", "card_type": "PASPOR_BLUE"})
	r.Inc(CardSelected, Labels{"product_type": "TAHAPAN_BCA", "card_type": "PASPOR_BLUE"})
	r.Inc(CardSelected, Labels{"product_type": "TAHAPAN_BCA", "card_type": "PASPOR_GOLD"})

	blue := r.Counter(CardSelected, Labels{"product_type": "TAHAPAN_BCA", "card_type": "PASPOR_BLUE"})
	if blue != 2 {
		t.Errorf("counter BLUE: got %d, want 2", blue)
	}
	if total := r.CounterSum(CardSelected); total != 3 {
		t.Errorf("CounterSum: got %d, want 3", total)
	}
	// Seri yang belum pernah dinaikkan bernilai 0, bukan error.
	if v := r.Counter(CardSelected, Labels{"card_type": "PASPOR_PLATINUM"}); v != 0 {
		t.Errorf("counter belum ada: got %d, want 0", v)
	}
}

// Urutan label tidak boleh memecah satu seri menjadi dua — kalau ini gagal,
// angka yang sama terbelah di dashboard tanpa ada yang menyadarinya.
func TestRegistry_LabelOrderDoesNotSplitSeries(t *testing.T) {
	r := NewRegistry()

	r.Inc(CardChanged, Labels{"from": "PASPOR_BLUE", "to": "PASPOR_GOLD"})
	r.Inc(CardChanged, Labels{"to": "PASPOR_GOLD", "from": "PASPOR_BLUE"})

	got := r.Counter(CardChanged, Labels{"from": "PASPOR_BLUE", "to": "PASPOR_GOLD"})
	if got != 2 {
		t.Fatalf("counter: got %d, want 2 — urutan label memecah seri", got)
	}

	counters, _ := r.Snapshot()
	if len(counters) != 1 {
		t.Fatalf("seri: got %d, want 1: %+v", len(counters), counters)
	}
}

// Label kosong menjadi "none", bukan string kosong: reason_key memang sering
// tidak ada, dan seri tanpa nilai label sulit dibaca di dashboard.
func TestRegistry_EmptyLabelValueBecomesNone(t *testing.T) {
	r := NewRegistry()
	r.Inc(CardUnavailable, Labels{"card_type": "PASPOR_GOLD", "reason_key": ""})

	counters, _ := r.Snapshot()
	if len(counters) != 1 {
		t.Fatalf("seri: %+v", counters)
	}
	if counters[0].Labels["reason_key"] != "none" {
		t.Errorf("reason_key: got %q, want none", counters[0].Labels["reason_key"])
	}
}

// Koma di nilai label tidak boleh memecah kunci saat dibaca kembali.
func TestRegistry_LabelValueSeparatorsAreSanitized(t *testing.T) {
	r := NewRegistry()
	r.Inc(CardUnavailable, Labels{"reason_key": "STOCK,EMPTY=X"})

	counters, _ := r.Snapshot()
	if len(counters) != 1 {
		t.Fatalf("seri: %+v", counters)
	}
	if counters[0].Labels["reason_key"] != "STOCK_EMPTY_X" {
		t.Errorf("reason_key: got %q", counters[0].Labels["reason_key"])
	}
}

func TestRegistry_HistogramBucketsAreCumulative(t *testing.T) {
	r := NewRegistry()
	labels := Labels{"cache_result": CacheHit}

	// 3 ms, 30 ms, 3000 ms → satu di bawah 5, satu di bawah 50, satu di +Inf.
	r.Observe(CardCatalogLatency, labels, 3*time.Millisecond)
	r.Observe(CardCatalogLatency, labels, 30*time.Millisecond)
	r.Observe(CardCatalogLatency, labels, 3000*time.Millisecond)

	_, histograms := r.Snapshot()
	if len(histograms) != 1 {
		t.Fatalf("histogram: got %d, want 1", len(histograms))
	}
	h := histograms[0]
	if h.Count != 3 {
		t.Errorf("count: got %d, want 3", h.Count)
	}

	// Kumulatif: bucket 5ms memuat 1, bucket 50ms memuat 2 (3ms DAN 30ms),
	// +Inf memuat ketiganya.
	for bucket, want := range map[string]int64{"5": 1, "10": 1, "50": 2, "2500": 2, "+Inf": 3} {
		if got := h.Buckets[bucket]; got != want {
			t.Errorf("bucket %s: got %d, want %d (buckets: %v)", bucket, got, want, h.Buckets)
		}
	}

	wantAvg := (3.0 + 30.0 + 3000.0) / 3
	if h.AvgMS < wantAvg-0.001 || h.AvgMS > wantAvg+0.001 {
		t.Errorf("avg: got %f, want %f", h.AvgMS, wantAvg)
	}
}

// Latensi cache hit dan miss harus terpisah — itu seluruh gunanya memisahkan
// keduanya di §Prompt 8.
func TestRegistry_HistogramSplitsByLabel(t *testing.T) {
	r := NewRegistry()
	r.Observe(CardCatalogLatency, Labels{"cache_result": CacheHit}, 2*time.Millisecond)
	r.Observe(CardCatalogLatency, Labels{"cache_result": CacheMiss}, 200*time.Millisecond)

	_, histograms := r.Snapshot()
	if len(histograms) != 2 {
		t.Fatalf("histogram: got %d, want 2", len(histograms))
	}
	for _, h := range histograms {
		if h.Count != 1 {
			t.Errorf("%v: count %d, want 1", h.Labels, h.Count)
		}
	}
}

// Counter monotonik: delta nol atau negatif diabaikan, karena rasio yang
// dihitung dari counter yang bisa turun tidak bisa dipercaya.
func TestRegistry_NonPositiveDeltaIgnored(t *testing.T) {
	r := NewRegistry()
	r.Add(CardSelected, nil, 5)
	r.Add(CardSelected, nil, -3)
	r.Add(CardSelected, nil, 0)

	if got := r.Counter(CardSelected, nil); got != 5 {
		t.Errorf("counter: got %d, want 5", got)
	}
}

func TestRegistry_Reset(t *testing.T) {
	r := NewRegistry()
	r.Inc(CardSelected, nil)
	r.Observe(CardCatalogLatency, nil, time.Millisecond)

	r.Reset()

	counters, histograms := r.Snapshot()
	if len(counters) != 0 || len(histograms) != 0 {
		t.Errorf("registry belum kosong: %d counter, %d histogram", len(counters), len(histograms))
	}
}

// Counter dinaikkan dari setiap handler HTTP, jadi pemakaian bersamaan adalah
// keadaan normal, bukan kasus tepi. Dijalankan dengan -race di make test.
func TestRegistry_ConcurrentUse(t *testing.T) {
	r := NewRegistry()

	const goroutines = 8
	const perGoroutine = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				r.Inc(CardSelected, Labels{"card_type": "PASPOR_" + strconv.Itoa(id%2)})
				r.Observe(CardCatalogLatency, Labels{"cache_result": CacheHit}, time.Millisecond)
				r.CounterSum(CardSelected)
				r.Snapshot()
			}
		}(i)
	}
	wg.Wait()

	if total := r.CounterSum(CardSelected); total != goroutines*perGoroutine {
		t.Errorf("total: got %d, want %d", total, goroutines*perGoroutine)
	}
}

// --- Jendela waktu (dasar alert §Prompt 8) ---

// withClock memasang jam yang bisa dikendalikan test.
func withClock(r *Registry, at *time.Time) {
	r.now = func() time.Time { return *at }
}

func TestRegistry_WindowSumOnlyCountsRecent(t *testing.T) {
	r := NewRegistry()
	clock := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	withClock(r, &clock)

	// Dua kejadian 30 menit lalu, tiga kejadian dalam 5 menit terakhir.
	r.Inc(CardUnavailable, Labels{"card_type": "PASPOR_GOLD"})
	r.Inc(CardUnavailable, Labels{"card_type": "PASPOR_GOLD"})

	clock = clock.Add(25 * time.Minute)
	r.Inc(CardUnavailable, Labels{"card_type": "PASPOR_GOLD"})
	r.Inc(CardUnavailable, Labels{"card_type": "PASPOR_PLATINUM"})
	r.Inc(CardUnavailable, Labels{"card_type": "PASPOR_PLATINUM"})

	clock = clock.Add(5 * time.Minute) // sekarang 10:30

	// Jendela 15 menit hanya memuat tiga kejadian terakhir (pada 10:25).
	if got := r.WindowSum(CardUnavailable, 15*time.Minute); got != 3 {
		t.Errorf("WindowSum 15m: got %d, want 3", got)
	}
	// Jendela 45 menit memuat semuanya.
	if got := r.WindowSum(CardUnavailable, 45*time.Minute); got != 5 {
		t.Errorf("WindowSum 45m: got %d, want 5", got)
	}
	// Counter kumulatif tidak terpengaruh jendela.
	if got := r.CounterSum(CardUnavailable); got != 5 {
		t.Errorf("CounterSum: got %d, want 5", got)
	}
}

// Bucket yang sudah lewat masa simpan dibuang, supaya proses yang hidup lama
// tidak menumpuk satu bucket per menit tanpa batas.
func TestRegistry_WindowBucketsArePruned(t *testing.T) {
	r := NewRegistry()
	clock := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	withClock(r, &clock)

	for i := 0; i < 90; i++ {
		r.Inc(CardSelected, nil)
		clock = clock.Add(time.Minute)
	}

	r.mu.RLock()
	buckets := len(r.windowed[CardSelected])
	r.mu.RUnlock()

	maxBuckets := int(windowRetention/time.Minute) + 1
	if buckets > maxBuckets {
		t.Errorf("bucket tersimpan %d, maksimum %d", buckets, maxBuckets)
	}
	// Dan yang kumulatif tetap utuh — pemangkasan hanya menyentuh jendela.
	if got := r.CounterSum(CardSelected); got != 90 {
		t.Errorf("CounterSum: got %d, want 90", got)
	}
}

func TestRegistry_WindowSumZeroWindow(t *testing.T) {
	r := NewRegistry()
	r.Inc(CardSelected, nil)
	if got := r.WindowSum(CardSelected, 0); got != 0 {
		t.Errorf("jendela nol: got %d, want 0", got)
	}
}
