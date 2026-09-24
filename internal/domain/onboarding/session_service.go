package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
)

const (
	sessionTTL           = 24 * time.Hour
	maxSessionsPerDevice = 3
	sessionRateWindow    = 1 * time.Hour
)

type SessionService struct {
	sessions SessionRepository
	cache    SessionCache
	audit    AuditRepository

	// cards opsional: tanpa katalog, sisipan pilih kartu dianggap mati dan
	// sesi berangkat dari OCR seperti sebelum sisipan ini ada.
	cards *CardService

	// legacyAppVersion adalah ambang X-App-Version untuk fallback client lama
	// (§7 butir 6). Kosong berarti fallback dimatikan — default yang aman,
	// karena memaksa kartu default ke nasabah yang tidak memilihnya adalah
	// keputusan produk, bukan bawaan teknis.
	legacyAppVersion string
}

type SessionServiceConfig struct {
	Sessions         SessionRepository
	Cache            SessionCache
	Audit            AuditRepository
	Cards            *CardService
	LegacyAppVersion string
}

func NewSessionService(cfg SessionServiceConfig) *SessionService {
	return &SessionService{
		sessions:         cfg.Sessions,
		cache:            cfg.Cache,
		audit:            cfg.Audit,
		cards:            cfg.Cards,
		legacyAppVersion: cfg.LegacyAppVersion,
	}
}

// CreateSession creates a new onboarding session after validating product_type
// and enforcing the per-device rate limit.
func (s *SessionService) CreateSession(ctx context.Context, req CreateSessionRequest, ipAddress, userAgent string) (*CreateSessionResponse, error) {
	// Validate product type
	pt := ProductType(req.ProductType)
	if !pt.Valid() {
		return nil, apperr.OnboardingProductUnavailable
	}

	if req.DeviceID == "" {
		return nil, apperr.ValidationError
	}

	if req.AcceptedTNCVersion == "" {
		return nil, apperr.ValidationError
	}

	// Rate limit: max 3 sessions per device per hour
	count, err := s.sessions.CountActiveByDevice(ctx, req.DeviceID, time.Now().Add(-sessionRateWindow))
	if err != nil {
		return nil, fmt.Errorf("count active sessions: %w", err)
	}
	if count >= maxSessionsPerDevice {
		return nil, apperr.OnboardingSessionLimit
	}

	// Generate session_id with prefix "onb_"
	shortID, err := generateShortID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}
	sessionID := "onb_" + shortID

	// Resolusi kartu menentukan langkah awal sesi (§7). Dikerjakan SEBELUM
	// baris sesi ditulis: kartu yang ditolak tidak boleh meninggalkan sesi
	// setengah jadi yang menghabiskan jatah 3 sesi per perangkat.
	card, outdated, err := s.resolveSessionCard(ctx, pt, req)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	// Sesi HANYA berhenti di CARD_SELECTION ketika sisipan menyala dan nasabah
	// memang belum memilih kartu. Ketika sisipan mati, CARD_SELECTION adalah
	// jalan buntu: tidak ada katalog untuk dibaca, dan OCR service memanggil
	// TransitionStep ke PERSONAL_DATA yang ditolak karena OCR terlewat. Pernah
	// jadi bug: seluruh flow buka rekening mati untuk setiap sesi baru.
	currentStep := StepOCR
	steps := StepsCompleted{TNCAccepted: true}
	switch {
	case card != nil:
		steps.CardSelected = true
	case s.cards.HasSelectableCatalog(ctx, pt, req.RegionCode):
		currentStep = StepCardSelection
	}

	session := &Session{
		ID:             uuid.New(),
		SessionID:      sessionID,
		DeviceID:       req.DeviceID,
		ProductType:    pt,
		CurrentStep:    currentStep,
		TNCVersion:     req.AcceptedTNCVersion,
		StepsCompleted: steps,
		CreatedAt:      now,
		UpdatedAt:      now,
		ExpiresAt:      now.Add(sessionTTL),
	}
	if card != nil {
		session.CardType = card.CardType
		session.CardSelectedAt = &now
		session.CardCatalogVersion = req.CardCatalogVersion
	}

	// Store in PostgreSQL
	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Cache in Redis
	if s.cache != nil {
		if err := s.cache.Store(ctx, session); err != nil {
			slog.Error("cache onboarding session failed", "error", err)
		}
	}

	// Audit
	auditDetails := map[string]any{
		"product_type": req.ProductType,
		"tnc_version":  req.AcceptedTNCVersion,
	}
	if card != nil {
		auditDetails["card_type"] = card.CardType
		auditDetails["card_catalog_version"] = req.CardCatalogVersion
	}
	if outdated {
		// Dicatat supaya selisih versi bisa ditelusuri saat nasabah mengaku
		// melihat biaya yang berbeda dari yang tertagih.
		auditDetails["catalog_outdated"] = true
	}
	s.writeAudit(ctx, sessionID, AuditSessionCreated, "nasabah:"+req.DeviceID,
		auditDetails, ipAddress, userAgent)

	// Jejak pemilihan kartu terpisah dari audit sesi: §11 menuntut biaya YANG
	// DILIHAT nasabah ikut tersimpan, dan itu tidak ada di audit umum.
	if card != nil && s.cards != nil {
		s.cards.LogSelection(ctx, CardSelectionLogEntry{
			SessionID:            sessionID,
			ProductType:          string(pt),
			ToCardType:           card.CardType,
			CatalogVersion:       s.cards.VersionForLog(ctx, req.CardCatalogVersion),
			MonthlyAdminFeeShown: card.Fees.MonthlyAdmin,
			Actor:                "CUSTOMER",
			IPAddress:            ipAddress,
		})
	}

	resp := &CreateSessionResponse{
		SessionID:       sessionID,
		Product:         ProductCatalog[pt],
		CurrentStep:     currentStep,
		ExpiresAt:       session.ExpiresAt,
		CatalogOutdated: outdated,
	}
	if card != nil {
		resp.Card = toSessionCard(card, &now)
	}
	return resp, nil
}

// resolveSessionCard menerjemahkan card_type pada permintaan menjadi kartu yang
// tersimpan, mengikuti keempat cabang §7.
//
// Mengembalikan (nil, _, nil) berarti sesi berangkat dari CARD_SELECTION.
func (s *SessionService) resolveSessionCard(ctx context.Context, pt ProductType, req CreateSessionRequest) (*CardOption, bool, error) {
	// Tanpa katalog atau dengan sisipan dimatikan, perilaku lama berlaku utuh:
	// sesi langsung ke OCR tanpa pernah menyentuh kartu.
	if s.cards == nil || !s.cards.Enabled(ctx) {
		return nil, false, nil
	}

	cardType := req.CardType

	// Fallback client lama (§7 butir 6): build yang belum mengenal
	// CARD_SELECTION akan berhenti di layar kosong kalau diberi step yang tidak
	// dikenalnya, jadi kartu default produk dipakai dan sesi lanjut ke OCR.
	// Hanya aktif bila ambang versinya memang dikonfigurasi.
	if cardType == "" && s.isLegacyApp(req.AppVersion) {
		catalog, err := s.cards.GetCatalog(ctx, pt, req.RegionCode)
		if err != nil {
			// Katalog bermasalah tidak boleh menggagalkan pembuatan sesi untuk
			// client lama — mereka memang tidak meminta kartu.
			slog.Warn("legacy fallback: katalog tidak terbaca, sesi lanjut tanpa kartu",
				"product_type", pt, "app_version", req.AppVersion, "error", err)
			return nil, false, nil
		}
		cardType = catalog.DefaultCardType
		if cardType == "" {
			return nil, false, nil
		}
		slog.Info("legacy app fallback: kartu default dipakai",
			"app_version", req.AppVersion, "card_type", cardType)
	}

	if cardType == "" {
		return nil, false, nil
	}

	// Divalidasi terhadap katalog PRODUK sesi ini, bukan daftar kartu global.
	card, err := s.cards.FindCard(ctx, pt, cardType, req.RegionCode)
	if err != nil {
		return nil, false, err
	}

	outdated := s.catalogOutdated(ctx, req.CardCatalogVersion)
	return card, outdated, nil
}

// catalogOutdated melaporkan apakah versi katalog yang dikirim client sudah
// tertinggal. Versi yang tidak dikirim bukan versi yang usang.
func (s *SessionService) catalogOutdated(ctx context.Context, clientVersion string) bool {
	if clientVersion == "" {
		return false
	}
	current, err := s.cards.CurrentVersion(ctx)
	if err != nil {
		// Tidak bisa memastikan bukan berarti usang; sesi tetap dibuat dan
		// penanda dibiarkan mati daripada menakuti client tanpa dasar.
		slog.Warn("tidak bisa membaca versi katalog untuk perbandingan", "error", err)
		return false
	}
	return current != clientVersion
}

// isLegacyApp membandingkan X-App-Version dengan ambang yang dikonfigurasi.
//
// Perbandingan string biasa sengaja dipakai: format versi aplikasi di sini
// belum disepakati, dan membandingkan semver secara naif ("1.10" < "1.9")
// justru salah. Selama ambang belum diisi, fallback ini mati total.
func (s *SessionService) isLegacyApp(appVersion string) bool {
	return s.legacyAppVersion != "" && appVersion != "" && appVersion < s.legacyAppVersion
}

// toSessionCard memadatkan kartu katalog menjadi objek `card` pada respons sesi.
func toSessionCard(card *CardOption, selectedAt *time.Time) *SessionCard {
	return &SessionCard{
		CardType:   card.CardType,
		Name:       card.Name,
		Style:      card.Style,
		Fees:       card.Fees,
		Limits:     card.Limits,
		SelectedAt: selectedAt,
	}
}

// GetSession retrieves a session by ID, checking cache first then DB.
// Returns an expired-session error if the session has passed its TTL.
func (s *SessionService) GetSession(ctx context.Context, sessionID string) (*GetSessionResponse, error) {
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	return &GetSessionResponse{
		SessionID:      session.SessionID,
		Product:        ProductCatalog[session.ProductType],
		CurrentStep:    session.CurrentStep,
		StepsCompleted: session.StepsCompleted,
		CreatedAt:      session.CreatedAt,
		ExpiresAt:      session.ExpiresAt,
		Card:           s.sessionCard(ctx, session),
	}, nil
}

// sessionCard menyusun objek `card` pada GET session (§9).
//
// Detail kartu (nama, gaya, biaya, limit) tidak disalin ke baris sesi — hanya
// card_type yang disimpan — jadi katalog dibaca ulang di sini. Katalog yang
// tidak terbaca mengembalikan nil, bukan error: nasabah yang ingin melanjutkan
// draf tidak boleh kehilangan seluruh sesinya karena layar kartu bermasalah.
func (s *SessionService) sessionCard(ctx context.Context, session *Session) *SessionCard {
	if session.CardType == "" || s.cards == nil {
		return nil
	}

	card, err := s.cards.LookupCard(ctx, session.ProductType, session.CardType)
	if err != nil || card == nil {
		slog.Warn("kartu sesi tidak terbaca dari katalog; respons dikirim tanpa card",
			"session_id", session.SessionID, "card_type", session.CardType, "error", err)
		return nil
	}
	return toSessionCard(card, session.CardSelectedAt)
}

// CancelSession soft-deletes a session and writes an audit log.
func (s *SessionService) CancelSession(ctx context.Context, sessionID, ipAddress, userAgent string) error {
	// Verify session exists
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return err
	}

	// Soft-delete in DB
	if err := s.sessions.SoftDelete(ctx, sessionID); err != nil {
		return fmt.Errorf("soft delete session: %w", err)
	}

	// Remove from cache
	if s.cache != nil {
		if err := s.cache.Delete(ctx, sessionID); err != nil {
			slog.Error("delete onboarding cache failed", "error", err)
		}
	}

	// Audit
	s.writeAudit(ctx, sessionID, AuditSessionCancelled, "nasabah:"+session.DeviceID, map[string]any{
		"last_step": string(session.CurrentStep),
	}, ipAddress, userAgent)

	return nil
}

// TransitionStep advances the session to the next step. Used by other services.
func (s *SessionService) TransitionStep(ctx context.Context, sessionID string, to Step, ipAddress, userAgent string) error {
	session, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return err
	}

	fromStep := session.CurrentStep

	if !CanTransition(session.CurrentStep, to) {
		return apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Tidak dapat pindah dari %s ke %s.", session.CurrentStep, to),
		}
	}

	completed := session.StepsCompleted
	switch to {
	case StepOCR:
		completed.TNCAccepted = true
	case StepPersonalData:
		completed.OCRVerified = true
	case StepOTPVerify:
		completed.PersonalDataSaved = true
	case StepBiometric:
		completed.OTPVerified = true
	case StepVideoCall:
		completed.BiometricVerified = true
	case StepCredentials:
		completed.VideoCallVerified = true
	case StepReview:
		completed.CredentialsSet = true
	case StepCompleted:
		completed.Submitted = true
	}

	if err := s.sessions.UpdateStep(ctx, sessionID, to, completed); err != nil {
		return fmt.Errorf("update step: %w", err)
	}

	// Update cache. ExpiresAt is deliberately left alone: the session lives 24h
	// from creation (§ONBOARDING_SESSION_EXPIRED), and refreshing it on every
	// step would make a session that never expires as long as it keeps moving.
	if s.cache != nil {
		session.CurrentStep = to
		session.StepsCompleted = completed
		if err := s.cache.Store(ctx, session); err != nil {
			slog.Error("cache step update failed", "error", err)
		}
	}

	// Audit — "from" is captured before the in-memory session is mutated above.
	s.writeAudit(ctx, sessionID, AuditStepTransition, "system", map[string]any{
		"from": string(fromStep),
		"to":   string(to),
	}, ipAddress, userAgent)

	return nil
}

// resolveSession loads a session from cache or DB.
func (s *SessionService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
	return resolveOnboardingSession(ctx, s.sessions, s.cache, sessionID)
}

// resolveOnboardingSession memuat sesi dari cache, lalu database.
//
// Dipakai bersama oleh SessionService dan CardService. Satu salinan, bukan dua:
// keduanya harus memperlakukan sesi kedaluwarsa dan sesi terhapus dengan cara
// yang persis sama, dan dua salinan yang melenceng berarti satu jalur masih
// melayani sesi yang jalur lain sudah tolak.
func resolveOnboardingSession(ctx context.Context, sessions SessionRepository, cache SessionCache, sessionID string) (*Session, error) {
	// Try cache first
	if cache != nil {
		session, err := cache.Get(ctx, sessionID)
		if err != nil {
			slog.Error("cache get onboarding session failed", "error", err)
		}
		if session != nil {
			if session.IsExpired() {
				return nil, apperr.OnboardingSessionExpired
			}
			return session, nil
		}
	}

	// Fallback to DB
	session, err := sessions.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if session == nil {
		return nil, apperr.OnboardingNotFound
	}
	if session.IsExpired() {
		return nil, apperr.OnboardingSessionExpired
	}

	// Backfill cache
	if cache != nil {
		if err := cache.Store(ctx, session); err != nil {
			slog.Error("backfill cache failed", "error", err)
		}
	}

	return session, nil
}

func (s *SessionService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
	if s.audit == nil {
		return
	}
	entry := &AuditLog{
		ID:        uuid.New(),
		SessionID: sessionID,
		EventType: eventType,
		Actor:     actor,
		Details:   details,
		IPAddress: ip,
		UserAgent: ua,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.audit.Insert(ctx, entry); err != nil {
		slog.Error("onboarding audit insert failed",
			"session_id", sessionID,
			"event_type", eventType,
			"error", err,
		)
	}
}

// generateShortID returns 16 hex chars (8 random bytes).
//
// This is the only bearer credential for the whole onboarding flow — no token,
// no cookie — so it has to be wide enough that guessing one is hopeless: 64
// bits, not the 48 it used to be. A crypto/rand failure is returned, never
// panicked: a request that cannot get randomness fails as a 500, it does not
// take the process down.
func generateShortID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}
