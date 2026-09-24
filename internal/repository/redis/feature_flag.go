package redis

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// FeatureFlags membaca sakelar fitur yang bisa diubah tanpa restart.
//
// §13 docs/08-PILIH-KARTU-API-SPEC.md mensyaratkan flag pilih kartu dibaca dari
// konfigurasi runtime, bukan environment variable: gunanya justru sebagai jalan
// keluar ketika sisipan bermasalah di produksi, dan jalan keluar yang menuntut
// deploy ulang bukan jalan keluar.
//
// Nilai dari environment tetap dipakai sebagai default. Redis hanya menimpa,
// sehingga deployment baru berangkat dari keadaan yang ditulis di konfigurasi,
// bukan dari sisa sakelar yang mungkin sudah lama terlupakan.
type FeatureFlags struct {
	client   *goredis.Client
	defaults map[string]bool

	// cacheTTL menahan pembacaan Redis sebentar. Tanpa ini setiap permintaan
	// katalog menambah satu round-trip; dengan ini, perubahan sakelar berlaku
	// dalam hitungan detik — cukup cepat untuk sebuah jalan keluar darurat.
	cacheTTL time.Duration

	// mu melindungi cached: sakelar ini dibaca dari setiap handler HTTP, jadi
	// map tanpa penjaga adalah data race, bukan sekadar nilai usang.
	mu     sync.RWMutex
	cached map[string]cachedFlag
}

type cachedFlag struct {
	value     bool
	expiresAt time.Time
}

// FlagCardSelection adalah kunci Redis sakelar sisipan pilih kartu.
const FlagCardSelection = "flag:onboarding:card_selection:enabled"

func NewFeatureFlags(client *goredis.Client, defaults map[string]bool) *FeatureFlags {
	if defaults == nil {
		defaults = map[string]bool{}
	}
	return &FeatureFlags{
		client:   client,
		defaults: defaults,
		cacheTTL: 5 * time.Second,
		cached:   map[string]cachedFlag{},
	}
}

// CardSelectionEnabled melaporkan status sisipan pilih kartu.
func (f *FeatureFlags) CardSelectionEnabled(ctx context.Context) bool {
	return f.lookup(ctx, FlagCardSelection)
}

// lookup membaca satu sakelar: cache pendek → Redis → default konfigurasi.
//
// Redis bermasalah tidak boleh mengubah perilaku fitur secara diam-diam, jadi
// kegagalan apa pun jatuh ke nilai default, bukan ke false.
func (f *FeatureFlags) lookup(ctx context.Context, key string) bool {
	if f == nil {
		return false
	}

	now := time.Now()

	f.mu.RLock()
	entry, ok := f.cached[key]
	f.mu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.value
	}

	value := f.defaults[key]

	if f.client != nil {
		raw, err := f.client.Get(ctx, key).Result()
		switch {
		case err == goredis.Nil:
			// Belum pernah disetel: pakai default konfigurasi.
		case err != nil:
			slog.Warn("read feature flag failed; using configured default",
				"flag", key, "default", value, "error", err)
		default:
			parsed, parseErr := strconv.ParseBool(raw)
			if parseErr != nil {
				slog.Warn("feature flag bernilai tidak terbaca; using configured default",
					"flag", key, "value", raw, "default", value)
			} else {
				value = parsed
			}
		}
	}

	f.mu.Lock()
	f.cached[key] = cachedFlag{value: value, expiresAt: now.Add(f.cacheTTL)}
	f.mu.Unlock()

	return value
}
