package onboarding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/metrics"
)

// CardService melayani katalog kartu dan pemilihan kartu pada sesi.
type CardService struct {
	cards    CardRepository
	cache    CardCache
	sessions SessionRepository

	// sessionCache dipegang supaya perubahan kartu ikut menyegarkan entri sesi
	// di Redis. Tanpa ini, PUT kartu menulis ke Postgres sementara pembacaan
	// sesi berikutnya masih dilayani cache lama — nasabah menekan simpan, layar
	// berikutnya menampilkan kartu yang lama.
	sessionCache SessionCache

	// audit menulis jejak transisi langkah saat PUT kartu memajukan sesi dari
	// CARD_SELECTION ke OCR, sejajar dengan SessionService.TransitionStep.
	audit AuditRepository

	// flag dibaca setiap request, bukan sekali saat start (§13). Mati berarti
	// katalog menjawab CARD_CATALOG_EMPTY dan flow lama kembali utuh tanpa
	// rollback deployment (aturan wajib #8).
	flag CardFeatureFlag

	// metrics opsional. nil berarti instrumentasi mati, bukan panic: sisipan ini
	// tidak boleh ikut mati karena registry-nya tidak dipasang.
	metrics *metrics.Registry
}

type CardServiceConfig struct {
	Cards        CardRepository
	Cache        CardCache
	Sessions     SessionRepository
	SessionCache SessionCache
	Audit        AuditRepository
	Flag         CardFeatureFlag
	Metrics      *metrics.Registry
}

func NewCardService(cfg CardServiceConfig) *CardService {
	return &CardService{
		cards:        cfg.Cards,
		cache:        cfg.Cache,
		sessions:     cfg.Sessions,
		sessionCache: cfg.SessionCache,
		audit:        cfg.Audit,
		flag:         cfg.Flag,
		metrics:      cfg.Metrics,
	}
}

// Enabled melaporkan status feature flag saat ini.
// Tanpa flag yang terpasang, sisipan dianggap mati — default yang aman.
func (s *CardService) Enabled(ctx context.Context) bool {
	return s != nil && s.flag != nil && s.flag.CardSelectionEnabled(ctx)
}

// GetCatalog mengembalikan katalog kartu untuk satu produk.
//
// Urutannya: versi → cache → database. Redis yang bermasalah di titik mana pun
// hanya menurunkan kecepatan, tidak menggagalkan permintaan — layar pilih kartu
// tidak boleh ikut mati karena cache mati.
func (s *CardService) GetCatalog(ctx context.Context, productType ProductType, regionCode string) (*CardCatalog, error) {
	if !productType.Valid() {
		return nil, apperr.OnboardingProductUnknown
	}
	if !s.Enabled(ctx) {
		return nil, apperr.CardCatalogEmpty
	}

	// Latensi diukur dari sini, dan dipisah cache hit vs miss (§Prompt 8): satu
	// angka gabungan akan menyembunyikan bahwa jalur miss lambat selama jalur
	// hit mendominasi jumlah permintaan.
	started := time.Now()
	cacheResult := metrics.CacheMiss
	defer func() {
		s.observeCatalog(productType, cacheResult, time.Since(started))
	}()

	version := s.resolveVersion(ctx)

	// Cache hanya bisa dipakai kalau versinya diketahui: tanpa versi, kunci
	// cache tidak bisa dibentuk dan kita tidak punya cara tahu entri lama masih
	// mewakili katalog hari ini.
	if version != "" && s.cache != nil {
		cached, err := s.cache.GetCatalog(ctx, productType, regionCode, version)
		if err != nil {
			cacheResult = metrics.CacheError
			slog.Warn("card catalog cache read failed; falling back to database",
				"product_type", productType, "region_code", regionCode, "error", err)
		} else if cached != nil {
			cacheResult = metrics.CacheHit
			return cached, nil
		}
	}

	cards, err := s.cards.ListCards(ctx, productType, regionCode)
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	if len(cards) == 0 {
		return nil, apperr.CardCatalogEmpty
	}

	// Versi belum diketahui dari cache → ambil dari database. Dilakukan setelah
	// ListCards supaya katalog kosong tidak membayar query tambahan.
	if version == "" {
		version, err = s.cards.CatalogVersion(ctx)
		if err != nil {
			return nil, fmt.Errorf("catalog version: %w", err)
		}
		if s.cache != nil {
			if err := s.cache.SetVersion(ctx, version); err != nil {
				slog.Warn("card catalog version cache write failed", "error", err)
			}
		}
	}

	catalog := &CardCatalog{
		CatalogVersion:  version,
		ProductType:     productType,
		DefaultCardType: resolveDefaultCard(cards),
		Currency:        catalogCurrency(cards),
		Cards:           cards,
	}

	if s.cache != nil {
		if err := s.cache.SetCatalog(ctx, productType, regionCode, version, catalog); err != nil {
			slog.Warn("card catalog cache write failed",
				"product_type", productType, "error", err)
		}
	}

	return catalog, nil
}

// FindCard mengembalikan satu kartu pada satu produk, atau error domain yang
// sesuai bila kartunya tidak dikenal atau tidak bisa dipilih.
//
// Dipakai saat create session (§7) dan PUT kartu (§8), sehingga keduanya
// menolak dengan alasan yang sama persis.
func (s *CardService) FindCard(ctx context.Context, productType ProductType, cardType, regionCode string) (*CardOption, error) {
	if !productType.Valid() {
		return nil, apperr.OnboardingProductUnknown
	}
	if cardType == "" {
		return nil, apperr.CardTypeInvalid
	}

	card, err := s.cards.GetCard(ctx, productType, cardType, regionCode)
	if err != nil {
		return nil, fmt.Errorf("get card: %w", err)
	}
	// Divalidasi terhadap katalog PRODUK ini, bukan daftar kartu global: kartu
	// yang sah untuk Tahapan BCA belum tentu ditawarkan untuk TabunganKu.
	if card == nil {
		return nil, apperr.CardTypeInvalid
	}

	switch card.Availability.Status {
	case CardAvailable:
		return card, nil
	case CardNotEligible:
		s.countUnavailable(card.CardType, card.Availability.ReasonKey)
		return nil, apperr.Error{
			Status:  apperr.CardNotEligible.Status,
			Code:    apperr.CardNotEligible.Code,
			Message: apperr.CardNotEligible.Message,
			Details: reasonDetails(card.Availability.ReasonKey),
		}
	default:
		s.countUnavailable(card.CardType, card.Availability.ReasonKey)
		return nil, apperr.Error{
			Status:  apperr.CardTypeUnavailable.Status,
			Code:    apperr.CardTypeUnavailable.Code,
			Message: apperr.CardTypeUnavailable.Message,
			Details: reasonDetails(card.Availability.ReasonKey),
		}
	}
}

// CurrentVersion mengembalikan versi katalog untuk ETag dan pencatatan audit.
func (s *CardService) CurrentVersion(ctx context.Context) (string, error) {
	if v := s.resolveVersion(ctx); v != "" {
		return v, nil
	}
	v, err := s.cards.CatalogVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("catalog version: %w", err)
	}
	return v, nil
}

// BumpVersion menaikkan versi katalog dan MENYEGARKAN penanda versi di cache.
//
// Dua langkah itu tidak boleh dipisah. Menaikkan versi di database saja
// meninggalkan versi lama di Redis, dan karena kunci katalog memuat versi,
// GetCatalog akan terus menyajikan katalog lama sampai penanda versi itu
// kedaluwarsa dengan sendirinya — sebelumnya berarti hampir 24 jam.
//
// Dipanggil setiap kali katalog ditulis (admin API, Fase 7), satu kali per
// transaksi penulisan, bukan per baris.
func (s *CardService) BumpVersion(ctx context.Context) (string, error) {
	version, err := s.cards.BumpCatalogVersion(ctx)
	if err != nil {
		return "", fmt.Errorf("bump catalog version: %w", err)
	}

	if s.cache != nil {
		// Tulis versi baru; kalau gagal, hapus yang lama supaya pembacaan
		// berikutnya jatuh ke database. Menyisakan versi lama di cache adalah
		// satu-satunya hasil yang tidak boleh terjadi.
		if err := s.cache.SetVersion(ctx, version); err != nil {
			slog.Warn("write new catalog version to cache failed; invalidating instead",
				"version", version, "error", err)
			if err := s.cache.InvalidateVersion(ctx); err != nil {
				slog.Error("invalidate catalog version failed; katalog lama bisa tersaji sampai TTL habis",
					"error", err)
			}
		}
	}

	return version, nil
}

// LogSelection menulis jejak audit. Best effort di sisi pemanggil, tapi
// kegagalannya dicatat: §11 menyebut audit ini syarat kepatuhan.
func (s *CardService) LogSelection(ctx context.Context, entry CardSelectionLogEntry) {
	if s.cards == nil {
		return
	}
	if entry.Actor == "" {
		entry.Actor = "CUSTOMER"
	}
	if err := s.cards.LogCardSelection(ctx, entry); err != nil {
		slog.Error("card selection audit write failed",
			"session_id", entry.SessionID, "to_card_type", entry.ToCardType, "error", err)
	}

	// Metrik dan log dinaikkan di satu tempat — di sini — karena SETIAP jalur
	// pemilihan kartu (create session dan PUT kartu) melewatinya. Menaruhnya di
	// masing-masing pemanggil berarti satu jalur baru akan diam-diam tidak
	// terhitung.
	if s.metrics != nil {
		if entry.FromCardType == nil {
			s.metrics.Inc(metrics.CardSelected, metrics.Labels{
				"product_type": entry.ProductType,
				"card_type":    entry.ToCardType,
			})
		} else {
			s.metrics.Inc(metrics.CardChanged, metrics.Labels{
				"from": *entry.FromCardType,
				"to":   entry.ToCardType,
			})
		}
	}

	// Tanpa PII: session_id, card_type, versi katalog, dan request_id saja —
	// tidak ada nama, NIK, maupun nomor telepon (§Prompt 8, aturan log repo).
	slog.Info("kartu dipilih",
		"session_id", entry.SessionID,
		"product_type", entry.ProductType,
		"from_card_type", derefOr(entry.FromCardType, ""),
		"card_type", entry.ToCardType,
		"catalog_version", entry.CatalogVersion,
	)
}

// observeCatalog mencatat satu permintaan katalog beserta latensinya.
func (s *CardService) observeCatalog(productType ProductType, cacheResult string, elapsed time.Duration) {
	if s.metrics == nil {
		return
	}
	s.metrics.Inc(metrics.CardCatalogRequests, metrics.Labels{
		"product_type": string(productType),
		"cache_result": cacheResult,
	})
	s.metrics.Observe(metrics.CardCatalogLatency, metrics.Labels{
		"cache_result": cacheResult,
	}, elapsed)
}

// countUnavailable mencatat kartu yang ditolak karena keadaannya.
//
// Rasio counter ini terhadap CardSelected adalah dasar alert §Prompt 8: stok
// yang salah dikonfigurasi menaikkannya tajam, sementara perilaku nasabah tidak.
func (s *CardService) countUnavailable(cardType string, reasonKey *string) {
	if s.metrics == nil {
		return
	}
	s.metrics.Inc(metrics.CardUnavailable, metrics.Labels{
		"card_type":  cardType,
		"reason_key": derefOr(reasonKey, ""),
	})
}

// CountIssuance mencatat hasil satu permintaan cetak kartu.
//
// Di CardService, bukan SubmitService, karena registry-nya tinggal di sini —
// dan satu registry yang dibagi lebih baik daripada dua yang terpisah, di mana
// alert membaca angka yang jalur lain naikkan.
func (s *CardService) CountIssuance(cardType, outcome string) {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.Inc(metrics.CardIssuanceRequests, metrics.Labels{
		"card_type": cardType,
		"outcome":   outcome,
	})
}

func derefOr(v *string, fallback string) string {
	if v == nil {
		return fallback
	}
	return *v
}

// resolveVersion membaca versi dari cache. "" berarti tidak diketahui, bukan
// gagal — pemanggil melanjutkan ke database.
func (s *CardService) resolveVersion(ctx context.Context) string {
	if s.cache == nil {
		return ""
	}
	version, err := s.cache.GetVersion(ctx)
	if err != nil {
		slog.Warn("card catalog version cache read failed", "error", err)
		return ""
	}
	return version
}

// resolveDefaultCard memilih kartu yang dipilih lebih dulu di layar.
//
// Kartu bertanda is_default yang kebetulan tidak tersedia diturunkan ke kartu
// AVAILABLE pertama. Keputusan ini milik server: menyerahkannya ke client
// berarti tiga platform menebak sendiri dan bisa berbeda-beda.
func resolveDefaultCard(cards []CardOption) string {
	for _, c := range cards {
		if c.IsDefault && c.Availability.Status.Selectable() {
			return c.CardType
		}
	}
	for _, c := range cards {
		if c.Availability.Status.Selectable() {
			return c.CardType
		}
	}
	// Semua kartu tidak tersedia: katalog tetap dikirim supaya nasabah melihat
	// kartu beserta alasannya, hanya tanpa pilihan awal.
	return ""
}

// catalogCurrency mengambil mata uang dari kartu pertama yang menyebutkannya.
// Kolomnya per kartu di database tapi dikirim sekali di tingkat katalog (§4);
// katalog dengan dua mata uang berbeda bukan keadaan yang didukung.
func catalogCurrency(cards []CardOption) string {
	for _, c := range cards {
		if c.Currency != "" {
			return c.Currency
		}
	}
	return "IDR"
}

func reasonDetails(reasonKey *string) any {
	if reasonKey == nil || *reasonKey == "" {
		return nil
	}
	return map[string]any{"reason_key": *reasonKey}
}

// HasSelectableCatalog melaporkan apakah produk ini benar-benar menawarkan
// kartu yang bisa dipilih.
//
// Dipakai create session sebelum memarkir sesi di CARD_SELECTION. Sakelar yang
// menyala tidak berarti setiap produk punya kartu: katalog hanya terisi untuk
// produk yang sudah dikonfigurasi, dan memarkir sesi di langkah yang layarnya
// hanya bisa menampilkan CARD_CATALOG_EMPTY adalah jalan buntu yang sama
// dengan yang dihindari komentar di CreateSession.
//
// Katalog yang tidak terbaca (Redis dan Postgres sama-sama bermasalah)
// dijawab false: sesi lanjut ke OCR seperti flow lama. Kehilangan langkah
// pilih kartu jauh lebih ringan daripada sesi yang tidak bisa dilanjutkan.
func (s *CardService) HasSelectableCatalog(ctx context.Context, productType ProductType, regionCode string) bool {
	if !s.Enabled(ctx) {
		return false
	}
	catalog, err := s.GetCatalog(ctx, productType, regionCode)
	if err != nil {
		if !errors.Is(err, apperr.CardCatalogEmpty) {
			slog.Warn("katalog kartu tidak terbaca saat membuat sesi; sesi lanjut ke OCR",
				"product_type", productType, "error", err)
		}
		return false
	}
	return catalog.DefaultCardType != ""
}

// LookupCard mengembalikan satu kartu APA ADANYA, tanpa memeriksa ketersediaan.
//
// Dipakai untuk menampilkan kartu yang SUDAH tersimpan pada sesi. Kartu yang
// stoknya habis setelah nasabah memilihnya tetap harus muncul di layar
// Ringkasan — memakai FindCard di sini akan membuat kartu itu lenyap dari
// respons hanya karena stok berubah setelah pilihan dibuat.
func (s *CardService) LookupCard(ctx context.Context, productType ProductType, cardType string) (*CardOption, error) {
	if s == nil || s.cards == nil || cardType == "" {
		return nil, nil
	}
	card, err := s.cards.GetCard(ctx, productType, cardType, "")
	if err != nil {
		return nil, fmt.Errorf("get card: %w", err)
	}
	return card, nil
}

// VersionForLog memilih versi katalog yang dicatat di jejak audit: versi yang
// DILIHAT client bila dikirim, selain itu versi katalog saat ini.
//
// Kolom catalog_version di onboarding_card_selection_log bertipe NOT NULL, dan
// mengisinya dengan penanda kosong membuat jejak biaya tidak bisa dipakai saat
// sengketa — justru satu-satunya alasan tabel itu ada (§11).
func (s *CardService) VersionForLog(ctx context.Context, clientVersion string) string {
	if clientVersion != "" {
		return clientVersion
	}
	version, err := s.CurrentVersion(ctx)
	if err != nil || version == "" {
		slog.Warn("versi katalog tidak terbaca untuk jejak audit kartu", "error", err)
		return "unknown"
	}
	return version
}

// SetCard menyimpan atau mengganti kartu pada sesi berjalan (§8).
//
// Perubahan kartu tidak menyentuh data langkah lain: hanya kolom kartu,
// current_step, dan bendera card_selected yang ditulis.
func (s *CardService) SetCard(ctx context.Context, req SetCardRequest, ipAddress, userAgent string) (*SetCardResponse, error) {
	if !s.Enabled(ctx) {
		// Sisipan mati berarti tidak ada layar kartu sama sekali, jadi endpoint
		// ini menjawab seperti katalognya: kosong, bukan error internal.
		return nil, apperr.CardCatalogEmpty
	}

	session, err := resolveOnboardingSession(ctx, s.sessions, s.sessionCache, req.SessionID)
	if err != nil {
		return nil, err
	}

	// Setelah submit, permintaan cetak sudah masuk ke core banking. Mengganti
	// kartu di sini hanya akan membuat data sesi berbeda dari kartu yang
	// benar-benar dicetak.
	if session.StepsCompleted.Submitted {
		return nil, apperr.CardLocked
	}

	card, err := s.FindCard(ctx, session.ProductType, req.CardType, req.RegionCode)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	completed := session.StepsCompleted
	completed.CardSelected = true

	// Langkah sesi dipertahankan apa adanya kecuali sesi memang sedang berhenti
	// di CARD_SELECTION. Nasabah yang mengganti kartu dari layar Ringkasan
	// tetap di REVIEW — memajukannya ke OCR akan melempar dia kembali ke awal
	// flow dan menghapus kemajuan yang sudah dibuat (§8).
	step := session.CurrentStep
	if step == StepCardSelection {
		step = StepOCR
	}

	// Panggilan berturut-turut untuk kartu yang sama tidak menghasilkan
	// perubahan apa pun selain baris audit — simpanan dan langkahnya identik,
	// jadi ulangan dari client yang gugup aman.
	previous := session.CardType

	if err := s.sessions.UpdateCard(ctx, req.SessionID, SessionCardUpdate{
		CardType:       card.CardType,
		CatalogVersion: req.CardCatalogVersion,
		SelectedAt:     now,
		CurrentStep:    step,
		StepsCompleted: completed,
	}); err != nil {
		return nil, fmt.Errorf("update session card: %w", err)
	}

	// Cache disegarkan setelah tulisan berhasil, bukan sebelum: entri cache yang
	// mendahului Postgres akan menyajikan kartu yang gagal tersimpan.
	if s.sessionCache != nil {
		session.CardType = card.CardType
		session.CardSelectedAt = &now
		session.CardCatalogVersion = req.CardCatalogVersion
		session.CurrentStep = step
		session.StepsCompleted = completed
		if err := s.sessionCache.Store(ctx, session); err != nil {
			slog.Error("segarkan cache sesi setelah ganti kartu gagal",
				"session_id", req.SessionID, "error", err)
		}
	}

	var from *string
	if previous != "" {
		from = &previous
	}
	s.LogSelection(ctx, CardSelectionLogEntry{
		SessionID:            req.SessionID,
		ProductType:          string(session.ProductType),
		FromCardType:         from,
		ToCardType:           card.CardType,
		CatalogVersion:       s.VersionForLog(ctx, req.CardCatalogVersion),
		MonthlyAdminFeeShown: card.Fees.MonthlyAdmin,
		Actor:                "CUSTOMER",
		IPAddress:            ipAddress,
	})

	s.writeCardAudit(ctx, req.SessionID, session.DeviceID, map[string]any{
		"from_card_type":       previous,
		"to_card_type":         card.CardType,
		"card_catalog_version": req.CardCatalogVersion,
		"current_step":         string(step),
	}, ipAddress, userAgent)

	return &SetCardResponse{
		Card:        *toSessionCard(card, &now),
		CurrentStep: step,
		Steps:       completed,
	}, nil
}

func (s *CardService) writeCardAudit(ctx context.Context, sessionID, deviceID string, details map[string]any, ip, ua string) {
	if s.audit == nil {
		return
	}
	entry := &AuditLog{
		ID:        uuid.New(),
		SessionID: sessionID,
		EventType: AuditCardSelected,
		Actor:     "nasabah:" + deviceID,
		Details:   details,
		IPAddress: ip,
		UserAgent: ua,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.audit.Insert(ctx, entry); err != nil {
		slog.Error("audit pemilihan kartu gagal ditulis",
			"session_id", sessionID, "error", err)
	}
}

// --- Admin katalog kartu (§3, §13, Prompt 7) ---

// CardAdminRepository adalah bagian CardRepository yang hanya dipakai admin.
//
// Dipisah dari CardRepository supaya jalur nasabah tidak ikut membawa
// kemampuan menulis katalog: satu antarmuka yang bisa keduanya berarti setiap
// pemakai jalur baca juga memegang kemampuan mengubahnya.
type CardAdminRepository interface {
	ListAllCards(ctx context.Context) ([]AdminCardRow, error)

	UpdateCardProduct(ctx context.Context, cardType string, w CardProductWrite,
		actor, ipAddress string) (*CardCatalogWriteResult, error)

	UpdateProductCard(ctx context.Context, productType ProductType, cardType string,
		w ProductCardWrite, actor, ipAddress string) (*CardCatalogWriteResult, error)
}

// CardAdminService melayani penulisan katalog oleh admin.
//
// Terpisah dari CardService: yang ini tidak menyentuh cache katalog sama sekali.
// Invalidasinya berbasis versi (§12) — versi naik di dalam transaksi penulisan,
// kunci cache memuat versi, jadi entri lama tidak akan pernah terbaca lagi dan
// kedaluwarsa sendiri. Tidak ada key yang perlu dihapus, dan karena itu tidak
// ada jendela kosong yang memukul database.
type CardAdminService struct {
	repo CardAdminRepository

	// cache dipakai HANYA untuk menyegarkan penanda versi setelah penulisan.
	// Tanpa itu, penanda versi lama di Redis (TTL 1 menit) masih menentukan
	// kunci katalog yang dibaca, jadi perubahan admin tertunda selama TTL itu.
	cache CardCache
}

func NewCardAdminService(repo CardAdminRepository, cache CardCache) *CardAdminService {
	return &CardAdminService{repo: repo, cache: cache}
}

// ListCards mengembalikan seluruh katalog untuk layar admin.
func (s *CardAdminService) ListCards(ctx context.Context) ([]AdminCardRow, error) {
	rows, err := s.repo.ListAllCards(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all cards: %w", err)
	}
	return rows, nil
}

// UpdateCard menulis biaya, limit, pengiriman, dan status aktif satu kartu.
func (s *CardAdminService) UpdateCard(ctx context.Context, cardType string, w CardProductWrite, actor, ip string) (*CardCatalogWriteResult, error) {
	if strings.TrimSpace(cardType) == "" {
		return nil, apperr.CardTypeInvalid
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}

	result, err := s.repo.UpdateCardProduct(ctx, cardType, w, actor, ip)
	if err != nil {
		return nil, err
	}
	s.refreshVersion(ctx, result.CatalogVersion)

	slog.Info("katalog kartu diubah admin",
		"actor", actor, "card_type", cardType,
		"catalog_version", result.CatalogVersion, "is_active", w.IsActive)
	return result, nil
}

// UpdateProductCard menulis urutan, default, badge, dan stok satu kartu pada
// satu produk.
func (s *CardAdminService) UpdateProductCard(ctx context.Context, productType ProductType, cardType string, w ProductCardWrite, actor, ip string) (*CardCatalogWriteResult, error) {
	if !productType.Valid() {
		return nil, apperr.OnboardingProductUnknown
	}
	if strings.TrimSpace(cardType) == "" {
		return nil, apperr.CardTypeInvalid
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}

	result, err := s.repo.UpdateProductCard(ctx, productType, cardType, w, actor, ip)
	if err != nil {
		return nil, err
	}
	s.refreshVersion(ctx, result.CatalogVersion)

	slog.Info("penempatan kartu diubah admin",
		"actor", actor, "product_type", productType, "card_type", cardType,
		"catalog_version", result.CatalogVersion,
		"availability_status", string(w.AvailabilityStatus))
	return result, nil
}

// refreshVersion menyegarkan penanda versi di cache setelah penulisan.
//
// Kalau penulisannya gagal, penanda lama DIHAPUS, bukan dibiarkan: penanda
// versi lama membuat GetCatalog terus menyusun kunci lama dan menyajikan
// katalog sebelum perubahan. Menyisakannya adalah satu-satunya hasil yang tidak
// boleh terjadi — sama seperti pada CardService.BumpVersion.
func (s *CardAdminService) refreshVersion(ctx context.Context, version string) {
	if s.cache == nil {
		return
	}
	if err := s.cache.SetVersion(ctx, version); err != nil {
		slog.Warn("tulis versi katalog baru ke cache gagal; penanda lama dihapus",
			"version", version, "error", err)
		if err := s.cache.InvalidateVersion(ctx); err != nil {
			slog.Error("hapus penanda versi katalog gagal; katalog lama bisa tersaji sampai TTL habis",
				"error", err)
		}
	}
}
