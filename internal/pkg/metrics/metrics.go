// Package metrics adalah registry counter dan histogram in-process.
//
// Kenapa bukan Prometheus: repo ini belum punya stack metrik sama sekali, dan
// menambah dependensi beserta endpoint /metrics adalah keputusan operasional
// yang lebih besar daripada sisipan pilih kartu. Yang dibutuhkan §Prompt 8
// docs/08-PILIH-KARTU-API-SPEC.md adalah angka yang bisa dibaca dan dijadikan
// dasar alert — dan itu bisa dipenuhi tanpa menambah satu pun dependensi.
//
// Bentuknya sengaja meniru model data Prometheus (nama + label + counter
// monotonik, histogram dengan bucket kumulatif) supaya pemindahan ke
// client_golang kelak hanya mengganti implementasi Registry, bukan setiap
// pemanggilnya.
//
// Seluruh operasi aman dipakai bersamaan: counter dinaikkan dari setiap handler
// HTTP, dan map tanpa penjaga adalah data race, bukan sekadar angka yang keliru.
package metrics

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Labels adalah dimensi satu sampel, mis. {"product_type": "TAHAPAN_BCA"}.
type Labels map[string]string

// defaultBuckets adalah batas atas bucket histogram latensi, dalam milidetik.
//
// Rapat di bawah 100 ms karena di situlah cache hit seharusnya berada; longgar
// di atasnya karena yang perlu dibedakan hanya "lambat" dari "sangat lambat".
var defaultBuckets = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500}

// windowRetention adalah umur maksimum bucket per-menit yang disimpan.
//
// Alert terpanjang yang dijawab registry ini memakai jendela 15 menit (§Prompt
// 8); 60 menit memberi ruang untuk jendela yang lebih lapang tanpa membuat
// jejak memorinya bertumbuh tanpa batas.
const windowRetention = 60 * time.Minute

// Registry menyimpan seluruh counter dan histogram satu proses.
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]int64
	histograms map[string]*histogram

	// windowed menyimpan counter yang sama, dipecah per menit, supaya rasio
	// "dalam 15 menit terakhir" bisa dijawab. Counter kumulatif saja tidak bisa:
	// rasio seumur proses akan tetap tenang berjam-jam setelah stok salah
	// dikonfigurasi, dan alert-nya tidak pernah berbunyi.
	windowed map[string]map[int64]int64

	// now dapat diganti di test. Jendela waktu yang hanya bisa diuji dengan
	// menunggu menit berganti bukan jendela yang diuji.
	now func() time.Time
}

type histogram struct {
	buckets []float64
	counts  []int64
	sum     float64
	total   int64
}

// CounterSample adalah satu counter beserta labelnya saat dibaca.
type CounterSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  int64             `json:"value"`
}

// HistogramSample adalah ringkasan satu histogram saat dibaca.
type HistogramSample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Count  int64             `json:"count"`
	SumMS  float64           `json:"sum_ms"`
	AvgMS  float64           `json:"avg_ms"`

	// Buckets memetakan batas atas (ms) ke jumlah kumulatif, seperti Prometheus.
	Buckets map[string]int64 `json:"buckets"`
}

func NewRegistry() *Registry {
	return &Registry{
		counters:   map[string]int64{},
		histograms: map[string]*histogram{},
		windowed:   map[string]map[int64]int64{},
		now:        time.Now,
	}
}

// Inc menaikkan satu counter sebanyak 1.
func (r *Registry) Inc(name string, labels Labels) {
	r.Add(name, labels, 1)
}

// Add menaikkan satu counter sebanyak delta.
//
// Delta negatif diabaikan: counter di model ini monotonik, dan membiarkannya
// turun membuat setiap rasio yang dihitung darinya tidak bisa dipercaya.
func (r *Registry) Add(name string, labels Labels, delta int64) {
	if name == "" || delta <= 0 {
		return
	}
	key := seriesKey(name, labels)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[key] += delta
	r.addWindowedLocked(key, delta)
}

// addWindowedLocked menambah delta ke bucket menit saat ini dan membuang bucket
// yang sudah lewat masa simpan. Pemanggil memegang r.mu.
func (r *Registry) addWindowedLocked(key string, delta int64) {
	now := r.now()
	minute := now.Unix() / 60

	buckets := r.windowed[key]
	if buckets == nil {
		buckets = map[int64]int64{}
		r.windowed[key] = buckets
	}
	buckets[minute] += delta

	// Pemangkasan dilakukan saat menulis, bukan lewat goroutine tersendiri:
	// satu seri yang berhenti dinaikkan juga berhenti menumpuk bucket, jadi
	// tidak ada yang perlu dibersihkan secara berkala.
	cutoff := minute - int64(windowRetention/time.Minute)
	for m := range buckets {
		if m < cutoff {
			delete(buckets, m)
		}
	}
}

// WindowSum menjumlahkan seluruh seri satu nama counter dalam jendela terakhir.
//
// Label diabaikan, seperti CounterSum: yang dibutuhkan alert adalah total
// lintas card_type, bukan satu per satu.
func (r *Registry) WindowSum(name string, window time.Duration) int64 {
	if window <= 0 {
		return 0
	}
	prefix := name + "{"

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Bucket dihitung inklusif dari menit terlama yang masih masuk jendela.
	// Menit berjalan ikut dihitung meski belum penuh — alert yang menunggu
	// menit selesai selalu terlambat satu menit.
	oldest := r.now().Add(-window).Unix() / 60

	var total int64
	for key, buckets := range r.windowed {
		if key != name && !strings.HasPrefix(key, prefix) {
			continue
		}
		for minute, value := range buckets {
			if minute >= oldest {
				total += value
			}
		}
	}
	return total
}

// Observe mencatat satu pengukuran durasi ke histogram.
func (r *Registry) Observe(name string, labels Labels, d time.Duration) {
	if name == "" {
		return
	}
	ms := float64(d) / float64(time.Millisecond)
	key := seriesKey(name, labels)

	r.mu.Lock()
	defer r.mu.Unlock()

	h := r.histograms[key]
	if h == nil {
		h = &histogram{
			buckets: defaultBuckets,
			counts:  make([]int64, len(defaultBuckets)+1), // +1 untuk +Inf
		}
		r.histograms[key] = h
	}

	h.total++
	h.sum += ms
	// Bucket kumulatif: satu pengukuran dihitung di bucketnya sendiri dan semua
	// bucket di atasnya, seperti definisi histogram Prometheus.
	for i, upper := range h.buckets {
		if ms <= upper {
			for j := i; j < len(h.counts); j++ {
				h.counts[j]++
			}
			return
		}
	}
	h.counts[len(h.counts)-1]++
}

// Counter membaca nilai satu counter. 0 bila belum pernah dinaikkan.
func (r *Registry) Counter(name string, labels Labels) int64 {
	key := seriesKey(name, labels)
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.counters[key]
}

// CounterSum menjumlahkan seluruh seri satu nama counter, mengabaikan label.
//
// Dipakai untuk rasio seperti "berapa persen pemilihan kartu yang ditolak
// karena stok" — pembilangnya perlu total lintas card_type, bukan satu per satu.
func (r *Registry) CounterSum(name string) int64 {
	prefix := name + "{"
	var total int64

	r.mu.RLock()
	defer r.mu.RUnlock()
	for key, value := range r.counters {
		if key == name || strings.HasPrefix(key, prefix) {
			total += value
		}
	}
	return total
}

// Snapshot mengembalikan seluruh sampel, terurut nama supaya keluarannya stabil
// antar pembacaan — respons monitoring yang berubah urutan tiap panggilan
// menyulitkan perbandingan antar waktu.
func (r *Registry) Snapshot() ([]CounterSample, []HistogramSample) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	counters := make([]CounterSample, 0, len(r.counters))
	for key, value := range r.counters {
		name, labels := parseKey(key)
		counters = append(counters, CounterSample{Name: name, Labels: labels, Value: value})
	}
	sort.Slice(counters, func(i, j int) bool {
		if counters[i].Name != counters[j].Name {
			return counters[i].Name < counters[j].Name
		}
		return labelString(counters[i].Labels) < labelString(counters[j].Labels)
	})

	histograms := make([]HistogramSample, 0, len(r.histograms))
	for key, h := range r.histograms {
		name, labels := parseKey(key)
		sample := HistogramSample{
			Name:    name,
			Labels:  labels,
			Count:   h.total,
			SumMS:   h.sum,
			Buckets: map[string]int64{},
		}
		if h.total > 0 {
			sample.AvgMS = h.sum / float64(h.total)
		}
		for i, upper := range h.buckets {
			sample.Buckets[formatBucket(upper)] = h.counts[i]
		}
		sample.Buckets["+Inf"] = h.counts[len(h.counts)-1]
		histograms = append(histograms, sample)
	}
	sort.Slice(histograms, func(i, j int) bool {
		if histograms[i].Name != histograms[j].Name {
			return histograms[i].Name < histograms[j].Name
		}
		return labelString(histograms[i].Labels) < labelString(histograms[j].Labels)
	})

	return counters, histograms
}

// Reset mengosongkan registry. Hanya untuk test — sebuah proses yang berjalan
// tidak boleh kehilangan counter-nya di tengah jalan.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters = map[string]int64{}
	r.histograms = map[string]*histogram{}
	r.windowed = map[string]map[int64]int64{}
}

// seriesKey membentuk kunci "nama{k=v,k=v}" dengan label terurut.
//
// Urutan label WAJIB dinormalkan: tanpa itu, {a,b} dan {b,a} menjadi dua seri
// berbeda untuk pengukuran yang sama, dan angkanya terpecah tanpa terlihat.
func seriesKey(name string, labels Labels) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(sanitizeLabelValue(labels[k]))
	}
	b.WriteByte('}')
	return b.String()
}

// sanitizeLabelValue membuang karakter yang dipakai sebagai pemisah kunci.
//
// Nilai label bisa berasal dari luar (reason_key dari katalog, card_type dari
// request), dan sebuah koma di dalamnya akan memecah kunci saat dibaca kembali.
func sanitizeLabelValue(v string) string {
	if v == "" {
		return "none"
	}
	replacer := strings.NewReplacer(",", "_", "=", "_", "{", "_", "}", "_")
	return replacer.Replace(v)
}

func parseKey(key string) (string, map[string]string) {
	open := strings.IndexByte(key, '{')
	if open < 0 || !strings.HasSuffix(key, "}") {
		return key, nil
	}
	name := key[:open]
	labels := map[string]string{}
	for _, pair := range strings.Split(key[open+1:len(key)-1], ",") {
		k, v, found := strings.Cut(pair, "=")
		if found {
			labels[k] = v
		}
	}
	return name, labels
}

func labelString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

// formatBucket memberi nama bucket seperti Prometheus: "50", "2500", "+Inf".
func formatBucket(upper float64) string {
	return strconv.FormatFloat(upper, 'f', -1, 64)
}

// --- Nama metrik sisipan pilih kartu (§Prompt 8) ---
//
// Konstanta, bukan string literal di tempat pemanggilan: satu salah ketik pada
// nama metrik menghasilkan seri baru yang sunyi, dan kesalahan itu baru
// ketahuan saat dashboard-nya kosong.
const (
	CardCatalogRequests  = "onboarding_card_catalog_requests_total"
	CardCatalogLatency   = "onboarding_card_catalog_duration_ms"
	CardSelected         = "onboarding_card_selected_total"
	CardChanged          = "onboarding_card_changed_total"
	CardUnavailable      = "onboarding_card_unavailable_total"
	CardIssuanceRequests = "onboarding_card_issuance_total"
)

// Nilai label cache_result pada CardCatalogRequests.
const (
	CacheHit   = "hit"
	CacheMiss  = "miss"
	CacheError = "error"
)
