package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// OnboardingProductCache menyimpan katalog jenis rekening tabungan.
//
// Tinggal di instance cache, bukan session: isinya sama untuk semua nasabah dan berubah
// beberapa kali setahun — sama seperti OnboardingTNCCache dan ContentCache.
type OnboardingProductCache struct {
	client *goredis.Client
}

func NewOnboardingProductCache(client *goredis.Client) *OnboardingProductCache {
	return &OnboardingProductCache{client: client}
}

const (
	// Versi bentuk response ikut di dalam kunci. Mengubah bentuknya berarti menaikkan
	// angkanya, dan entri lama kedaluwarsa sendiri — tidak ada penghapusan manual yang
	// bisa terlupakan saat deploy.
	productCatalogKey = "onboarding:products:v1:catalog"

	// Snapshot per versi: client yang masih memegang ETag versi lama tetap bisa
	// dilayani, dan product_catalog_version yang sudah tersimpan di baris sesi tetap
	// punya isinya. Prefix-nya saja — versinya disusulkan pemanggil.
	productCatalogVerKeyPrefix = "onboarding:products:v1:ver:"

	// 24 jam, mengikuti tncTTL dan contentTTL dengan alasan yang sama: isinya diubah
	// lewat SQL/migrasi, dan siapa pun yang mengubahnya tidak akan menjalankan
	// invalidasi cache.
	//
	// Invalidasi yang benar terjadi lewat KENAIKAN catalog_version, bukan DEL berpola:
	// versi baru menulis kunci snapshot baru, dan entri `catalog` yang lama kedaluwarsa
	// sendiri. Sisi lainnya: pada hari katalog diperbarui, entri lama bisa bertahan
	// sampai sehari — yang terjadi hanyalah nasabah melihat setoran awal lama, jadi
	// memperbarui katalog harus diikuti `DEL onboarding:products:v1:catalog`. Runbook
	// menyebutkannya.
	productCatalogTTL = 24 * time.Hour
)

// GetCatalog mengembalikan nil, nil saat cache miss.
//
// Miss BUKAN error: service memperlakukannya sebagai sinyal untuk membaca database, dan
// mengembalikan error akan membuat setiap cold start terbaca sebagai kegagalan Redis.
func (c *OnboardingProductCache) GetCatalog(ctx context.Context) (*onboarding.SavingsProductCatalog, error) {
	raw, err := c.client.Get(ctx, productCatalogKey).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("get product catalog cache: %w", err)
	}

	var catalog onboarding.SavingsProductCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		// Entri rusak diperlakukan sebagai MISS, bukan sebagai kegagalan: bentuk
		// response yang berubah tanpa menaikkan `v1` akan meninggalkan entri yang tidak
		// bisa di-decode, dan menolak permintaan karenanya berarti seluruh layar mati
		// sampai seseorang menghapus kuncinya. Pola yang sama dengan cache katalog kartu.
		return nil, nil
	}
	return &catalog, nil
}

// SetCatalog menulis entri aktif DAN snapshot per versi.
//
// Keduanya sekaligus, tapi kegagalan snapshot tidak menggagalkan penulisan utama:
// snapshot hanya dibutuhkan jalur yang menanyakan versi lama, dan jalur itu belum ada
// pemanggilnya hari ini.
func (c *OnboardingProductCache) SetCatalog(ctx context.Context, catalog *onboarding.SavingsProductCatalog) error {
	if catalog == nil {
		return nil
	}

	payload, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("marshal product catalog: %w", err)
	}

	if err := c.client.Set(ctx, productCatalogKey, payload, productCatalogTTL).Err(); err != nil {
		return fmt.Errorf("set product catalog cache: %w", err)
	}

	// Versi kosong berarti migrasi penanda versi belum jalan — tidak ada gunanya menulis
	// snapshot berkunci "…:ver:".
	if catalog.CatalogVersion == "" {
		return nil
	}

	verKey := productCatalogVerKeyPrefix + catalog.CatalogVersion
	if err := c.client.Set(ctx, verKey, payload, productCatalogTTL).Err(); err != nil {
		return fmt.Errorf("set product catalog version snapshot: %w", err)
	}
	return nil
}

// InvalidateCatalog menghapus entri katalog aktif setelah katalog ditulis.
//
// HANYA kunci `catalog`, bukan snapshot per versi. Snapshot-nya justru yang melayani
// client yang masih memegang ETag versi lama dan baris sesi yang sudah menyimpan versi
// itu; menghapusnya akan membuat jejak "katalog apa yang dilihat nasabah" kehilangan
// isinya.
//
// Tanpa pemanggilan ini, penulisan admin tertunda sampai TTL 24 jam habis. Itu bukan
// hipotesis: kunci katalog produk TIDAK memuat versinya — berbeda dari katalog kartu —
// jadi catalog_version yang naik tidak mengubah kunci yang dibaca GetCatalog.
//
// Kunci yang sudah tidak ada BUKAN error: Del mengembalikan 0 dan itu hasil yang benar.
// Penulisan kedua dalam satu menit, atau penulisan saat cache memang kosong, tidak boleh
// terbaca sebagai kegagalan.
func (c *OnboardingProductCache) InvalidateCatalog(ctx context.Context) error {
	if err := c.client.Del(ctx, productCatalogKey).Err(); err != nil {
		return fmt.Errorf("invalidate product catalog cache: %w", err)
	}
	return nil
}
