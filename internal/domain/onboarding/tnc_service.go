package onboarding

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

// TNCService melayani layar Syarat & Ketentuan dan menjaga agar persetujuan
// yang tercatat selalu mengacu ke teks yang betul-betul dilihat nasabah.
//
// Service sendiri, bukan bagian SessionService, karena isinya dibaca SEBELUM
// sesi ada: layar S&K muncul tanpa session_id dan tanpa Authorization. Menaruh
// pembacaan itu di SessionService berarti sebuah service tentang sesi harus
// melayani permintaan yang tidak punya sesi.
type TNCService struct {
	repo  TNCRepository
	cache TNCCache
}

type TNCServiceConfig struct {
	Repo TNCRepository

	// Cache boleh nil. Teksnya kecil dan jarang berubah; melayaninya langsung
	// dari Postgres adalah deployment yang benar, hanya lebih lambat.
	Cache TNCCache
}

func NewTNCService(cfg TNCServiceConfig) *TNCService {
	return &TNCService{repo: cfg.Repo, cache: cfg.Cache}
}

// Active melayani GET /v1/onboarding/tnc tanpa parameter: versi yang sedang
// berlaku.
func (s *TNCService) Active(ctx context.Context) (*TNCDocument, error) {
	return s.load(ctx, "")
}

// ByVersion melayani GET /v1/onboarding/tnc?version=... — termasuk versi yang
// sudah dicabut, supaya teks yang pernah disetujui bisa ditampilkan kembali.
//
// Versi kosong diperlakukan sebagai permintaan versi aktif, bukan galat: query
// `?version=` tanpa nilai adalah bentuk yang wajar dari client dan tidak ada
// gunanya menolaknya.
func (s *TNCService) ByVersion(ctx context.Context, version string) (*TNCDocument, error) {
	return s.load(ctx, strings.TrimSpace(version))
}

// ValidateVersion memastikan `accepted_tnc_version` yang dikirim nasabah adalah
// versi yang SEDANG berlaku, lalu mengembalikan dokumennya.
//
// Dipanggil CreateSession sebelum baris sesi ditulis. Sebelum ada pemeriksaan
// ini, satu-satunya syarat adalah versinya tidak kosong — string apa pun
// tersimpan sebagai bukti persetujuan.
//
// Versi lama DITOLAK, tidak diterima dengan penanda "outdated" seperti
// card_catalog_version. Bedanya mendasar: katalog kartu yang basi hanya membuat
// nasabah melihat biaya yang keliru, sementara S&K yang basi berarti bank
// menyimpan persetujuan atas pasal yang tidak pernah dibaca. Satu-satunya
// pemulihan yang benar adalah memaksa teks baru ditampilkan lagi.
func (s *TNCService) ValidateVersion(ctx context.Context, version string) (*TNCDocument, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, apperr.ValidationError
	}

	active, err := s.load(ctx, "")
	if err != nil {
		return nil, err
	}

	if active.Version == version {
		return active, nil
	}

	// Membedakan "client lama" dari "client mengarang nilai" butuh satu
	// pembacaan lagi. Harganya hanya dibayar pada jalur yang sudah gagal, dan
	// imbalannya adalah aplikasi tahu harus memuat ulang S&K (409) atau
	// melaporkan bug (422).
	if _, err := s.load(ctx, version); err != nil {
		appErr := apperr.From(err)
		if appErr.Code == apperr.TNCVersionUnknown.Code {
			return nil, appErr
		}
		return nil, err
	}

	outdated := apperr.TNCVersionOutdated
	outdated.Details = map[string]any{
		"sent_version":    version,
		"current_version": active.Version,
	}
	return nil, outdated
}

// load membaca satu versi lewat cache. version "" berarti versi aktif.
func (s *TNCService) load(ctx context.Context, version string) (*TNCDocument, error) {
	if s.cache != nil {
		cached, err := s.cache.GetTNC(ctx, version)
		if err != nil {
			// Peringatan, bukan kegagalan. Redis yang mati tidak boleh menutup
			// pintu masuk buka rekening.
			slog.Warn("tnc cache get failed", "error", err, "version", version)
		}
		if cached != nil {
			return cached, nil
		}
	}

	var (
		doc *TNCDocument
		err error
	)
	if version == "" {
		doc, err = s.repo.ActiveTNC(ctx)
	} else {
		doc, err = s.repo.TNCByVersion(ctx, version)
	}
	if err != nil {
		return nil, fmt.Errorf("read tnc: %w", err)
	}

	if s.cache != nil {
		if err := s.cache.SetTNC(ctx, version, doc); err != nil {
			slog.Warn("tnc cache set failed", "error", err, "version", version)
		}
	}
	return doc, nil
}
