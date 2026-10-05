package onboarding

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
)

// initialDepositWindow is how long the nasabah has to fund the new account.
const initialDepositWindow = 30 * 24 * time.Hour

type SubmitService struct {
	sessions     SessionRepository
	cache        SessionCache
	personalData PersonalDataRepository
	credentials  CredentialRepository
	coreBanking  CoreBankingClient
	provisioner  AccountProvisioner
	idempotency  IdempotencyCache
	aes          *crypto.AES
	audit        AuditRepository

	// cardFlag menentukan apakah kartu wajib saat submit (§10). Dibaca saat
	// request: sisipan yang dimatikan harus mengembalikan flow lama utuh,
	// termasuk membiarkan sesi tanpa kartu lolos submit.
	cardFlag CardFeatureFlag

	// cards membaca katalog untuk nama kartu dan jendela pengiriman; issuance
	// menyimpan antrean permintaan cetak. Keduanya opsional: tanpa mereka
	// submit tetap berhasil, hanya tanpa objek `card` pada respons.
	cards     *CardService
	issuance  CardIssuanceRepository
	cardCodes CardCoreBankingCodes

	// accountCards mencatat kartu terbitan ke account_cards, tabel yang dibaca
	// layar Profil Saya. Opsional seperti dependensi kartu lainnya: tanpa itu
	// submit tetap berhasil dan kartunya tetap masuk antrean cetak, hanya belum
	// muncul di GET /account/cards.
	accountCards AccountCardRegistrar
}

// CardCoreBankingCodes memetakan card_type ke kode kartu milik core banking.
//
// Fungsi, bukan map: pemetaannya tinggal di konfigurasi, dan domain tidak perlu
// tahu apakah nilainya berasal dari environment, file, atau tabel.
type CardCoreBankingCodes func(cardType string) string

type SubmitServiceConfig struct {
	Sessions     SessionRepository
	Cache        SessionCache
	PersonalData PersonalDataRepository
	Credentials  CredentialRepository
	CoreBanking  CoreBankingClient
	Provisioner  AccountProvisioner
	Idempotency  IdempotencyCache
	AES          *crypto.AES
	Audit        AuditRepository
	CardFlag     CardFeatureFlag
	Cards        *CardService
	CardIssuance CardIssuanceRepository
	CardCodes    CardCoreBankingCodes
	AccountCards AccountCardRegistrar
}

func NewSubmitService(cfg SubmitServiceConfig) *SubmitService {
	return &SubmitService{
		sessions:     cfg.Sessions,
		cache:        cfg.Cache,
		personalData: cfg.PersonalData,
		credentials:  cfg.Credentials,
		coreBanking:  cfg.CoreBanking,
		provisioner:  cfg.Provisioner,
		idempotency:  cfg.Idempotency,
		aes:          cfg.AES,
		audit:        cfg.Audit,
		cardFlag:     cfg.CardFlag,
		cards:        cfg.Cards,
		issuance:     cfg.CardIssuance,
		cardCodes:    cfg.CardCodes,
		accountCards: cfg.AccountCards,
	}
}

// Submit validates all steps, creates the account via core banking, provisions
// the m-BCA user, and finalizes the session.
func (s *SubmitService) Submit(ctx context.Context, req SubmitRequest, idempotencyKey, ipAddress, userAgent string) (*SubmitResponse, error) {
	// 1. Idempotency. The slot is claimed atomically and scoped to this
	// session: the key comes from the client, so two sessions can easily send
	// the same one, and an unscoped slot would hand one nasabah another
	// nasabah's account number.
	claimed := false
	if idempotencyKey != "" && s.idempotency != nil {
		claim, err := s.idempotency.Claim(ctx, req.SessionID, idempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("claim idempotency: %w", err)
		}
		switch {
		case claim.StoredResponse != "":
			var resp SubmitResponse
			if err := json.Unmarshal([]byte(claim.StoredResponse), &resp); err != nil {
				return nil, fmt.Errorf("decode stored submit response: %w", err)
			}
			return &resp, nil
		case claim.StillProcessing:
			return nil, apperr.IdempotencyConflict
		case claim.AlreadyClaimed:
			return nil, apperr.IdempotencyConflict
		}
		claimed = true
	}

	// release frees the slot so a failed submit can be retried. Anything that
	// returns an error below has to go through it.
	release := func() {
		if !claimed {
			return
		}
		if err := s.idempotency.Release(ctx, req.SessionID, idempotencyKey); err != nil {
			slog.Error("release idempotency slot failed", "session_id", req.SessionID, "error", err)
		}
	}

	// 2. Validate session
	session, err := s.resolveSession(ctx, req.SessionID)
	if err != nil {
		release()
		return nil, err
	}
	if session.CurrentStep != StepReview {
		release()
		return nil, apperr.Error{
			Status:  422,
			Code:    "ONBOARDING_INVALID_STEP",
			Message: fmt.Sprintf("Langkah saat ini %s, bukan REVIEW.", session.CurrentStep),
		}
	}

	// 3. Validate agreement
	if !req.AgreementAccepted || req.AgreementVersion == "" {
		release()
		return nil, apperr.ValidationError
	}

	// 4. Validate all steps completed
	if err := validateAllStepsCompleted(session.StepsCompleted); err != nil {
		release()
		return nil, err
	}

	// 4b. Kartu wajib hanya ketika sisipan pilih kartu menyala (§10).
	//
	// Gerbangnya sengaja pada flag, bukan pada keberadaan kartu saja: sesi yang
	// dibuat sebelum sisipan ini ada tidak punya card_selected, dan menolaknya
	// akan menahan nasabah yang sudah sampai REVIEW tanpa jalan keluar. Sesi
	// yang tertahan saat flag menyala masih bisa memilih kartu lewat
	// PUT /sessions/{id}/card, yang berlaku selama submitted == false.
	if !session.StepsCompleted.CardSelected && s.cardSelectionRequired(ctx) {
		release()
		return nil, apperr.Error{
			Status:  apperr.OnboardingIncomplete.Status,
			Code:    apperr.OnboardingIncomplete.Code,
			Message: apperr.OnboardingIncomplete.Message,
			Details: map[string]any{"missing_step": string(StepCardSelection)},
		}
	}

	// 5. Fetch personal data for account holder name
	pd, err := s.personalData.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		release()
		return nil, fmt.Errorf("find personal data: %w", err)
	}
	if pd == nil {
		release()
		return nil, apperr.OnboardingIncomplete
	}

	// A failed decrypt must stop the submit, not travel onwards.
	//
	// decryptOrKeep used to return the stored value when decryption failed, and
	// this is the worst place in the codebase for that: these four go straight to
	// core banking as the account holder's name, NIK, phone and email. A key
	// rotation or one corrupted row would have opened a real account whose holder
	// name is a hex string — a permanently wrong record in a banking system, from
	// an error nobody was told about. Refusing costs the nasabah one retry; the
	// alternative costs an account that has to be closed by hand.
	holderName, err := s.decryptPII(pd.NamaLengkap, "nama_lengkap")
	if err != nil {
		release()
		return nil, err
	}
	nik, err := s.decryptPII(pd.NIK, "nik")
	if err != nil {
		release()
		return nil, err
	}
	phone, err := s.decryptPII(pd.NomorHP, "nomor_hp")
	if err != nil {
		release()
		return nil, err
	}
	email, err := s.decryptPII(pd.Email, "email")
	if err != nil {
		release()
		return nil, err
	}

	// 6. Verify credentials exist
	cred, err := s.credentials.FindBySessionID(ctx, req.SessionID)
	if err != nil {
		release()
		return nil, fmt.Errorf("find credentials: %w", err)
	}
	if cred == nil {
		release()
		return nil, apperr.OnboardingIncomplete
	}

	// 7. Call core banking to create account
	cbResult, err := s.coreBanking.CreateAccount(ctx, req.SessionID, session.ProductType, holderName, nik)
	if err != nil {
		slog.Error("core banking create account failed", "session_id", req.SessionID, "error", err)
		release()
		return nil, apperr.AccountCreationFailed
	}

	// 8. Provision the m-BCA user so the nasabah can actually log in with the
	// credentials they just set.
	//
	// A nil provisioner means AES_KEY or LOOKUP_HMAC_SECRET is missing. The old
	// code invented an "mbca_xxxx" id and returned 200, so the flow ended with
	// an account number, a congratulatory response, and no user row — the
	// nasabah could never log in and nothing said so. Refuse instead.
	if s.provisioner == nil {
		slog.Error("onboarding submit rejected: account provisioning is disabled "+
			"(AES_KEY and LOOKUP_HMAC_SECRET are both required)",
			"session_id", req.SessionID)
		release()
		return nil, apperr.ProviderNotConfigured
	}

	provisioned, provErr := s.provisioner.ProvisionAccount(ctx, ProvisionParams{
		SessionID:      req.SessionID,
		DeviceID:       session.DeviceID,
		AccountNumber:  cbResult.AccountNumber,
		ProductType:    session.ProductType,
		FullName:       holderName,
		NIK:            nik,
		PhoneNumber:    phone,
		Email:          email,
		AccessCodeHash: cred.AccessCodeHash,
		PINHash:        cred.PINHash,
	})
	if provErr != nil {
		slog.Error("provision m-BCA user failed", "session_id", req.SessionID, "error", provErr)
		release()
		return nil, apperr.AccountCreationFailed
	}
	mbcaUserID := provisioned.UserID
	if provisioned.AccountNumber != "" {
		cbResult.AccountNumber = provisioned.AccountNumber
	}

	// 9. Transition to COMPLETED
	completed := session.StepsCompleted
	completed.Submitted = true
	if err := s.sessions.UpdateStep(ctx, req.SessionID, StepCompleted, completed); err != nil {
		release()
		return nil, fmt.Errorf("update step: %w", err)
	}

	if s.cache != nil {
		session.CurrentStep = StepCompleted
		session.StepsCompleted = completed
		if cacheErr := s.cache.Store(ctx, session); cacheErr != nil {
			slog.Error("cache step update failed", "session_id", req.SessionID, "error", cacheErr)
		}
	}

	// 9b. Minta core banking mencetak kartu.
	//
	// SETELAH rekening jadi dan sesi ditandai COMPLETED, dan kegagalannya tidak
	// pernah dikembalikan ke pemanggil: rekening yang sudah ACTIVE tidak boleh
	// dibatalkan karena pencetakan kartu gagal (§10 butir 4). Yang gagal masuk
	// antrean retry.
	card := s.issueCard(ctx, session, cbResult.AccountNumber, holderName,
		cardOwner{UserID: mbcaUserID, AccountID: provisioned.AccountID})

	// 10. Build response
	product := ProductCatalog[session.ProductType]
	now := time.Now().UTC()

	resp := &SubmitResponse{
		Account: AccountInfo{
			AccountNumber:          cbResult.AccountNumber,
			AccountType:            string(session.ProductType),
			AccountHolder:          holderName,
			Branch:                 cbResult.Branch,
			BranchCode:             cbResult.BranchCode,
			Currency:               product.Currency,
			Status:                 "ACTIVE",
			MinInitialDeposit:      product.MinInitialDeposit,
			InitialDepositDeadline: now.Add(initialDepositWindow),
		},
		MBCA: MBCAInfo{
			UserID:        mbcaUserID,
			AccessCodeSet: true,
			PINSet:        true,
		},
		CreatedAt: now,
		Card:      card,
	}

	// 11. Store the response against the idempotency slot
	if claimed {
		respJSON, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			slog.Error("marshal submit response failed", "session_id", req.SessionID, "error", marshalErr)
		} else if err := s.idempotency.Persist(ctx, req.SessionID, idempotencyKey, string(respJSON)); err != nil {
			slog.Error("persist idempotency response failed", "session_id", req.SessionID, "error", err)
		}
	}

	// 12. Audit
	s.writeAudit(ctx, req.SessionID, AuditAccountCreated, "system", map[string]any{
		"account_number": cbResult.AccountNumber,
		"product_type":   string(session.ProductType),
		"branch_code":    cbResult.BranchCode,
		"mbca_user_id":   mbcaUserID,
	}, ipAddress, userAgent)

	return resp, nil
}

// decryptPII decrypts a stored PII field, or fails.
//
// No key configured means the field was never encrypted, so the stored value IS
// the plaintext — that path stays. What is gone is the silent fallback when a key
// IS configured and decryption fails: the only thing that can be returned then is
// ciphertext, and there is no caller in this file for which that is a sensible
// account-holder detail. The field name travels into the log; the value never
// does.
func (s *SubmitService) decryptPII(value, field string) (string, error) {
	if s.aes == nil {
		return value, nil
	}
	decrypted, err := s.decryptField(value)
	if err != nil {
		slog.Error("decrypt personal data for submit failed",
			"field", field, "error", err)
		return "", apperr.InternalError
	}
	return decrypted, nil
}

// validateAllStepsCompleted checks that every required step is done.
// cardSelectionRequired melaporkan apakah submit harus menuntut kartu.
// Tanpa flag yang terpasang, sisipan dianggap mati — sama seperti CardService.
func (s *SubmitService) cardSelectionRequired(ctx context.Context) bool {
	return s.cardFlag != nil && s.cardFlag.CardSelectionEnabled(ctx)
}

func validateAllStepsCompleted(sc StepsCompleted) error {
	if !sc.TNCAccepted || !sc.OCRVerified || !sc.PersonalDataSaved ||
		!sc.OTPVerified || !sc.BiometricVerified || !sc.VideoCallVerified ||
		!sc.CredentialsSet {
		return apperr.OnboardingIncomplete
	}
	return nil
}

func (s *SubmitService) resolveSession(ctx context.Context, sessionID string) (*Session, error) {
	if s.cache != nil {
		session, err := s.cache.Get(ctx, sessionID)
		if err != nil {
			slog.Error("cache get session failed", "error", err)
		}
		if session != nil {
			if session.IsExpired() {
				return nil, apperr.OnboardingSessionExpired
			}
			return session, nil
		}
	}
	session, err := s.sessions.FindBySessionID(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("find session: %w", err)
	}
	if session == nil {
		return nil, apperr.OnboardingNotFound
	}
	if session.IsExpired() {
		return nil, apperr.OnboardingSessionExpired
	}
	if s.cache != nil {
		_ = s.cache.Store(ctx, session)
	}
	return session, nil
}

func (s *SubmitService) decryptField(ciphertextHex string) (string, error) {
	if s.aes == nil || ciphertextHex == "" {
		return ciphertextHex, nil
	}
	ct, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", err
	}
	pt, err := s.aes.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func (s *SubmitService) writeAudit(ctx context.Context, sessionID string, eventType AuditEventType, actor string, details map[string]any, ip, ua string) {
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
		slog.Error("submit audit failed", "session_id", sessionID, "error", err)
	}
}

// --- Penerbitan kartu (§10) ---

// cardIssuanceRetryDelay adalah jeda sebelum permintaan cetak yang gagal
// diulang. Pencetakan kartu tidak mendesak — nasabah sudah punya rekening yang
// aktif — jadi jedanya lapang, bukan hitungan detik.
const cardIssuanceRetryDelay = 15 * time.Minute

// issueCard meminta core banking mencetak kartu dan mengembalikan objek `card`
// untuk respons submit.
//
// TIDAK pernah mengembalikan error. Setiap kegagalan di sini — katalog tidak
// terbaca, pemetaan kode kosong, core banking menolak — berakhir sebagai baris
// antrean retry dan, bila memungkinkan, kartu berstatus REQUESTED di respons.
// Rekening nasabah sudah jadi; menggagalkan submit di titik ini akan menukar
// satu kartu yang terlambat dengan satu rekening yang hilang.
// cardOwner adalah pemilik rekening yang baru dibuat, untuk dilekatkan pada
// kartunya di account_cards. Dipisah sebagai tipe supaya dua UUID yang bentuknya
// sama tidak bisa tertukar posisinya di daftar argumen.
type cardOwner struct {
	UserID    string
	AccountID string
}

func (s *SubmitService) issueCard(ctx context.Context, session *Session, accountNumber, holderName string, owner cardOwner) *SubmitCard {
	if session.CardType == "" || s.issuance == nil {
		return nil
	}

	// Detail kartu dibaca dari katalog: nama untuk ditampilkan, jendela
	// pengiriman untuk menghitung estimasi tanggal.
	option, err := s.cards.LookupCard(ctx, session.ProductType, session.CardType)
	if err != nil || option == nil {
		slog.Error("kartu sesi tidak terbaca dari katalog saat submit; permintaan cetak dilewati",
			"session_id", session.SessionID, "card_type", session.CardType, "error", err)
		return nil
	}

	code := ""
	if s.cardCodes != nil {
		code = s.cardCodes(session.CardType)
	}
	if code == "" {
		// Seharusnya tidak pernah terjadi: pemetaan diverifikasi saat startup.
		// Kalau toh terjadi (kartu baru ditambahkan admin tanpa memperbarui
		// konfigurasi), permintaan tetap diantrekan supaya tidak hilang diam-diam.
		slog.Error("card_type tanpa pemetaan kode core banking; permintaan cetak diantrekan untuk retry",
			"session_id", session.SessionID, "card_type", session.CardType)
	}

	from, to := estimatedArrival(time.Now().UTC(), option.Delivery)
	method := CardDeliveryCourier
	if !option.Delivery.PhysicalCardAvailable && option.Delivery.BranchPickupAvailable {
		method = CardDeliveryBranchPickup
	}

	issuance := CardIssuance{
		SessionID:       session.SessionID,
		AccountNumber:   accountNumber,
		CardType:        option.CardType,
		CoreBankingCode: code,
		Status:          CardIssuanceRequested,
		DeliveryMethod:  method,
		EstimatedFrom:   from,
		EstimatedTo:     to,
	}

	// Klaim menentukan apakah permintaan cetak BOLEH dikirim. Submit ulang
	// dengan Idempotency-Key yang sudah habis masa simpannya tetap sampai ke
	// sini; baris UNIQUE inilah yang menahannya mencetak kartu kedua.
	fresh, err := s.issuance.Claim(ctx, issuance)
	if err != nil {
		slog.Error("klaim antrean cetak kartu gagal", "session_id", session.SessionID, "error", err)
		return s.submitCard(option, issuance, nil)
	}
	if !fresh {
		// Sudah pernah diminta. Kembalikan keadaan yang tersimpan, jangan
		// mencetak lagi.
		existing, findErr := s.issuance.FindBySessionID(ctx, session.SessionID)
		if findErr != nil || existing == nil {
			slog.Error("permintaan cetak kartu sudah ada tapi tidak terbaca",
				"session_id", session.SessionID, "error", findErr)
			return s.submitCard(option, issuance, nil)
		}
		return s.submitCard(option, *existing, existing.MaskedNumber)
	}

	result, err := s.coreBanking.IssueCard(ctx, CardIssuanceRequest{
		SessionID:       session.SessionID,
		AccountNumber:   accountNumber,
		CardType:        option.CardType,
		CoreBankingCode: code,
		HolderName:      holderName,
	})
	if err != nil {
		slog.Error("penerbitan kartu gagal; rekening tetap aktif, permintaan masuk antrean retry",
			"session_id", session.SessionID, "card_type", option.CardType, "error", err)
		if markErr := s.issuance.MarkFailed(ctx, session.SessionID, err.Error(),
			time.Now().UTC().Add(cardIssuanceRetryDelay)); markErr != nil {
			slog.Error("tandai kegagalan cetak kartu gagal", "session_id", session.SessionID, "error", markErr)
		}
		s.countIssuance(option.CardType, "failed")
		// Nasabah tetap melihat REQUESTED: permintaannya memang masih berjalan.
		return s.submitCard(option, issuance, nil)
	}

	if err := s.issuance.MarkResult(ctx, session.SessionID, *result); err != nil {
		slog.Error("simpan hasil cetak kartu gagal", "session_id", session.SessionID, "error", err)
	}

	s.registerOwnedCard(ctx, session, option.CardType, holderName, owner, *result)

	s.countIssuance(option.CardType, "ok")

	issuance.Status = result.Status
	issuance.TrackingNumber = result.TrackingNumber
	return s.submitCard(option, issuance, &result.MaskedNumber)
}

// registerOwnedCard mencatat kartu terbitan ke account_cards.
//
// Kegagalannya TIDAK pernah dikembalikan ke pemanggil, sama seperti kegagalan
// permintaan cetak: rekening sudah ACTIVE dan tidak boleh dibatalkan karena
// pencatatan kartu gagal (§10 butir 4). Yang hilang hanya barisnya di layar
// Profil Saya, dan itu dicatat sebagai error supaya bisa dibereskan menyusul.
func (s *SubmitService) registerOwnedCard(ctx context.Context, session *Session,
	cardType, holderName string, owner cardOwner, result CardIssuanceResult) {

	if s.accountCards == nil {
		return
	}
	if owner.UserID == "" || owner.AccountID == "" {
		slog.Error("kartu terbitan tidak bisa dicatat: pemilik rekening tidak diketahui",
			"session_id", session.SessionID)
		return
	}
	// Tanpa nomor tersamar tidak ada yang bisa ditampilkan di layar, dan
	// account_cards mewajibkan kolomnya. Kartunya tetap di antrean cetak.
	if result.MaskedNumber == "" {
		slog.Error("kartu terbitan tidak bisa dicatat: core banking tidak mengembalikan masked_number",
			"session_id", session.SessionID)
		return
	}
	// Masa berlaku juga NOT NULL, dan mengarangnya berarti menuliskan tanggal
	// yang tidak tercetak di kartu fisiknya.
	if result.ValidThruMonth == 0 || result.ValidThruYear == 0 {
		slog.Error("kartu terbitan tidak bisa dicatat: core banking tidak mengembalikan masa berlaku",
			"session_id", session.SessionID)
		return
	}

	if err := s.accountCards.RegisterIssuedCard(ctx, IssuedCard{
		UserID:         owner.UserID,
		AccountID:      owner.AccountID,
		CardType:       cardType,
		MaskedNumber:   result.MaskedNumber,
		CardholderName: holderName,
		ValidThruMonth: result.ValidThruMonth,
		ValidThruYear:  result.ValidThruYear,
	}); err != nil {
		slog.Error("catat kartu milik nasabah gagal; kartu tetap terbit tapi belum muncul di Profil Saya",
			"session_id", session.SessionID, "card_type", cardType, "error", err)
	}
}

// countIssuance mencatat hasil permintaan cetak kartu.
//
// Lewat CardService supaya registry-nya cuma satu dan submit tidak perlu
// memegang sendiri dependensi metrik.
func (s *SubmitService) countIssuance(cardType, outcome string) {
	s.cards.CountIssuance(cardType, outcome)
}

// submitCard menyusun objek `card` untuk respons submit.
func (s *SubmitService) submitCard(option *CardOption, iss CardIssuance, masked *string) *SubmitCard {
	status := iss.Status
	if status == "" || status == CardIssuanceFailed {
		// FAILED adalah keadaan operasional, bukan kabar untuk nasabah: kartunya
		// masih dalam antrean, jadi yang dilaporkan tetap REQUESTED (§10).
		status = CardIssuanceRequested
	}
	return &SubmitCard{
		CardType:     option.CardType,
		Name:         option.Name,
		MaskedNumber: masked,
		Status:       status,
		Delivery: SubmitCardDelivery{
			Method:               iss.DeliveryMethod,
			EstimatedArrivalFrom: formatArrival(iss.EstimatedFrom),
			EstimatedArrivalTo:   formatArrival(iss.EstimatedTo),
			TrackingNumber:       iss.TrackingNumber,
		},
	}
}

// estimatedArrival menghitung jendela tanggal tiba dari delivery_days katalog.
//
// Kartu tanpa jendela yang dikonfigurasi mengembalikan (nil, nil): estimasi
// yang ditebak lebih buruk daripada tidak ada estimasi, karena nasabah akan
// menelepon call center pada hari yang kita karang sendiri.
func estimatedArrival(now time.Time, d CardDelivery) (*time.Time, *time.Time) {
	if d.EstimatedDaysMin == nil || d.EstimatedDaysMax == nil {
		return nil, nil
	}
	from := now.AddDate(0, 0, *d.EstimatedDaysMin)
	to := now.AddDate(0, 0, *d.EstimatedDaysMax)
	return &from, &to
}

// formatArrival mengubah tanggal menjadi YYYY-MM-DD, atau nil bila tidak ada.
func formatArrival(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format("2006-01-02")
	return &formatted
}

// VerifyCardCoreBankingMapping memastikan setiap kartu aktif di katalog punya
// kode core banking.
//
// Dipanggil saat startup (§10): konfigurasi yang bolong harus ketahuan saat
// deploy, bukan saat nasabah sudah menekan submit dan rekeningnya terlanjur
// jadi tanpa kartu. Katalog kosong bukan kesalahan — artinya sisipan ini belum
// dipakai sama sekali.
func VerifyCardCoreBankingMapping(ctx context.Context, cards CardRepository, codeFor CardCoreBankingCodes) error {
	if cards == nil || codeFor == nil {
		return nil
	}

	var missing []string
	for productType := range validProducts {
		options, err := cards.ListCards(ctx, productType, "")
		if err != nil {
			return fmt.Errorf("baca katalog kartu %s: %w", productType, err)
		}
		for _, option := range options {
			if codeFor(option.CardType) == "" {
				missing = append(missing, string(productType)+"/"+option.CardType)
			}
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing) // urutan map tidak tentu; pesan startup harus bisa dibandingkan
		return fmt.Errorf(
			"card_type berikut tidak punya pemetaan kode core banking di ONBOARDING_CARD_CORE_BANKING_CODES: %s",
			strings.Join(missing, ", "))
	}
	return nil
}
