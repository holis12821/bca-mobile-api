package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// CardCatalogCache menyimpan katalog kartu yang sudah jadi.
//
// Tinggal di instance cache (allkeys-lru), bukan session: kehilangan entri di
// sini hanya berarti satu query database berikutnya, bukan nasabah kehilangan
// sesi. Sumber kebenarannya tetap PostgreSQL.
type CardCatalogCache struct {
	client *goredis.Client
}

func NewCardCatalogCache(client *goredis.Client) *CardCatalogCache {
	return &CardCatalogCache{client: client}
}

// catalogVersionKey menyimpan versi katalog terkini (§12).
const catalogVersionKey = "onb:cards:version"

// catalogKey memuat versi di dalam kuncinya, jadi katalog yang sudah usang
// tidak pernah terbaca — tidak perlu penghapusan eksplisit saat katalog
// berubah, cukup naikkan versinya. Wilayah kosong diberi penanda "nat"
// (nasional) supaya kuncinya tidak berakhir dengan titik dua menggantung.
func catalogKey(productType onboarding.ProductType, regionCode, version string) string {
	region := regionCode
	if region == "" {
		region = "nat"
	}
	return fmt.Sprintf("onb:cards:%s:%s:%s", productType, region, version)
}

// GetCatalog mengembalikan katalog dari cache. nil, nil bila tidak ada.
func (c *CardCatalogCache) GetCatalog(ctx context.Context, productType onboarding.ProductType, regionCode, version string) (*onboarding.CardCatalog, error) {
	data, err := c.client.Get(ctx, catalogKey(productType, regionCode, version)).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get card catalog: %w", err)
	}

	var catalog onboarding.CardCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		// Entri rusak diperlakukan sebagai cache miss, bukan kegagalan: katalog
		// masih bisa dibaca dari database, dan menggagalkan permintaan karena
		// satu entri cache busuk akan membuat layar pilih kartu ikut mati.
		return nil, nil
	}
	return &catalog, nil
}

// SetCatalog menyimpan katalog dengan TTL.
func (c *CardCatalogCache) SetCatalog(ctx context.Context, productType onboarding.ProductType, regionCode, version string, catalog *onboarding.CardCatalog) error {
	data, err := json.Marshal(catalog)
	if err != nil {
		return fmt.Errorf("marshal card catalog: %w", err)
	}
	// Kunci cache memuat catalog_version, jadi TTL hanyalah jaring pengaman
	// untuk entri yatim — katalog yang berubah sudah langsung terlihat lewat
	// versi yang naik. Angkanya tinggal di sini, bukan di domain: lama hidup
	// entri Redis bukan pengetahuan yang boleh dimiliki lapisan domain.
	const ttl = 15 * time.Minute
	if err := c.client.Set(ctx, catalogKey(productType, regionCode, version), data, ttl).Err(); err != nil {
		return fmt.Errorf("set card catalog: %w", err)
	}
	return nil
}

// GetVersion membaca versi katalog yang di-cache. "" bila tidak ada.
func (c *CardCatalogCache) GetVersion(ctx context.Context) (string, error) {
	v, err := c.client.Get(ctx, catalogVersionKey).Result()
	if err == goredis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get catalog version: %w", err)
	}
	return v, nil
}

// versionTTL sengaja pendek.
//
// Penanda versi adalah bayangan dari PostgreSQL dan satu-satunya hal yang
// menentukan entri katalog mana yang terbaca. TTL panjang (dulu 24 jam)
// berarti satu invalidasi yang terlewat menyajikan katalog basi seharian.
// Dengan satu menit, kesalahan seperti itu sembuh sendiri, dan ongkosnya hanya
// satu pembacaan satu baris ber-primary-key per menit per instance.
const versionTTL = time.Minute

// SetVersion menyimpan versi katalog.
func (c *CardCatalogCache) SetVersion(ctx context.Context, version string) error {
	if err := c.client.Set(ctx, catalogVersionKey, version, versionTTL).Err(); err != nil {
		return fmt.Errorf("set catalog version: %w", err)
	}
	return nil
}

// InvalidateVersion menghapus penanda versi.
//
// Pembacaan berikutnya jatuh ke database dan menghangatkan ulang. Dipakai saat
// penulisan versi baru ke cache gagal — lebih baik tidak ada versi sama sekali
// daripada versi lama yang menahan katalog basi.
func (c *CardCatalogCache) InvalidateVersion(ctx context.Context) error {
	if err := c.client.Del(ctx, catalogVersionKey).Err(); err != nil {
		return fmt.Errorf("invalidate catalog version: %w", err)
	}
	return nil
}
