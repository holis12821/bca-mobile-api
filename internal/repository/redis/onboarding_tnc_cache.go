package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

// OnboardingTNCCache menyimpan teks Syarat & Ketentuan buka rekening.
//
// Tinggal di instance cache, bukan session: isinya sama untuk semua orang dan
// berubah beberapa kali setahun, sama seperti ContentCache.
type OnboardingTNCCache struct {
	client *goredis.Client
}

func NewOnboardingTNCCache(client *goredis.Client) *OnboardingTNCCache {
	return &OnboardingTNCCache{client: client}
}

const (
	// Versi bentuk response ikut di dalam kunci. Mengubah bentuknya berarti
	// menaikkan angkanya, dan entri lama kedaluwarsa sendiri — tidak ada
	// penghapusan manual yang bisa terlupakan saat deploy.
	tncActiveKey = "onboarding:tnc:v1:active"
	tncByVerKey  = "onboarding:tnc:v1:ver:"

	// 24 jam, sama dengan contentTTL dan alasan yang sama: teks ini diubah
	// lewat SQL, dan siapa pun yang mengubahnya tidak akan menjalankan
	// invalidasi cache.
	//
	// Sisi lainnya: pada hari versi baru diaktifkan, entri `active` yang lama
	// bisa bertahan sampai sehari. Yang terjadi kalau itu dibiarkan hanyalah
	// nasabah melihat teks lama lalu persetujuannya DITOLAK 409 oleh
	// ValidateVersion — yang juga membaca cache ini, jadi penolakannya
	// konsisten, bukan acak. Mengaktifkan versi baru karena itu harus
	// diikuti `DEL onboarding:tnc:v1:active`; runbook menyebutkannya.
	tncTTL = 24 * time.Hour
)

// tncVersionFormat membatasi bentuk versi yang boleh ikut ke dalam kunci Redis.
//
// Nilainya datang dari query string dan dari body request, jadi tanpa batas ini
// setiap nilai karangan mencetak kunci baru — dan titik dua di dalamnya bisa
// menyelipkan pemisah tambahan ke dalam kunci. Persis pelajaran yang sama dari
// normalizeRegionCode pada katalog kartu. 20 karakter adalah batas kolom
// onboarding_tnc_documents.version.
var tncVersionFormat = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

// tncKey mengembalikan kunci untuk sebuah versi, dan false bila versinya tidak
// layak di-cache. version "" berarti entri versi aktif.
func tncKey(version string) (string, bool) {
	if version == "" {
		return tncActiveKey, true
	}
	if !tncVersionFormat.MatchString(version) {
		return "", false
	}
	return tncByVerKey + version, true
}

// GetTNC membaca satu versi dari cache. nil, nil bila tidak ada.
func (c *OnboardingTNCCache) GetTNC(ctx context.Context, version string) (*onboarding.TNCDocument, error) {
	key, ok := tncKey(version)
	if !ok {
		// Versi berbentuk aneh tidak pernah ada di database, jadi melewati
		// cache hanya meneruskannya ke satu query yang akan menolaknya.
		return nil, nil
	}

	data, err := c.client.Get(ctx, key).Bytes()
	if err == goredis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tnc cache: %w", err)
	}

	var doc onboarding.TNCDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		// Entri rusak diperlakukan sebagai cache miss: database masih punya
		// jawabannya, dan mematikan layar S&K karena satu entri busuk berarti
		// mematikan seluruh pembukaan rekening.
		return nil, nil
	}
	return &doc, nil
}

// SetTNC menyimpan satu versi dengan TTL.
func (c *OnboardingTNCCache) SetTNC(ctx context.Context, version string, doc *onboarding.TNCDocument) error {
	if doc == nil {
		return nil
	}
	key, ok := tncKey(version)
	if !ok {
		return nil
	}

	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal tnc: %w", err)
	}
	if err := c.client.Set(ctx, key, data, tncTTL).Err(); err != nil {
		return fmt.Errorf("set tnc cache: %w", err)
	}
	return nil
}
