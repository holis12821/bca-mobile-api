package onboarding

import (
	"context"
	"time"

	"github.com/holis12821/bca-mobile-api/internal/pkg/sms"
)

// SessionRepository defines data access for onboarding sessions in PostgreSQL.
type SessionRepository interface {
	// Create inserts a new onboarding session.
	Create(ctx context.Context, session *Session) error

	// FindBySessionID finds a non-deleted session by its session_id.
	// Returns nil, nil if not found.
	FindBySessionID(ctx context.Context, sessionID string) (*Session, error)

	// SoftDelete sets deleted_at on the session.
	SoftDelete(ctx context.Context, sessionID string) error

	// UpdateStep updates current_step and steps_completed.
	UpdateStep(ctx context.Context, sessionID string, step Step, completed StepsCompleted) error

	// UpdateCard menyimpan pilihan kartu bersama langkah sesi dalam satu tulis.
	// Tidak menyentuh kolom step lain (OCR, biometrik, kredensial).
	UpdateCard(ctx context.Context, sessionID string, upd SessionCardUpdate) error

	// CountActiveByDevice counts non-deleted, non-expired sessions for a device
	// created within the given window.
	CountActiveByDevice(ctx context.Context, deviceID string, since time.Time) (int, error)
}

// SessionCache defines onboarding session caching in Redis.
type SessionCache interface {
	// Store caches a session with TTL.
	Store(ctx context.Context, session *Session) error

	// Get retrieves a cached session. Returns nil, nil if not found.
	Get(ctx context.Context, sessionID string) (*Session, error)

	// Delete removes a cached session.
	Delete(ctx context.Context, sessionID string) error
}

// OCRResultRepository persists OCR extraction results.
type OCRResultRepository interface {
	// Create inserts a new OCR result row.
	Create(ctx context.Context, result *OCRResult) error

	// FindBySessionID returns the OCR result for a session.
	// Returns nil, nil if not found.
	FindBySessionID(ctx context.Context, sessionID string) (*OCRResult, error)
}

// OCREngine abstracts the text extraction engine (Google Cloud Vision, Tesseract, etc.).
type OCREngine interface {
	// ExtractText sends an image and returns the raw OCR text and confidence score.
	ExtractText(ctx context.Context, imageData []byte) (rawText string, confidence float64, err error)
}

// DukcapilClient verifies citizen identity against the Indonesian Dukcapil database.
type DukcapilClient interface {
	// VerifyNIK checks whether the NIK and name match in Dukcapil.
	VerifyNIK(ctx context.Context, nik, nama string) (match bool, err error)
}

// ObjectStorage abstracts S3-compatible file storage.
type ObjectStorage interface {
	// Upload stores a file and returns the object path/key.
	Upload(ctx context.Context, bucket, key string, data []byte, contentType string) (path string, err error)

	// Delete removes a file from storage.
	Delete(ctx context.Context, bucket, key string) error
}

// OCRRateLimiter checks OCR-specific rate limits per session.
type OCRRateLimiter interface {
	// CheckOCRAttempt checks and increments the OCR attempt counter.
	// Returns true if the request is allowed.
	CheckOCRAttempt(ctx context.Context, sessionID string) (allowed bool, err error)
}

// PersonalDataRepository persists onboarding personal data.
type PersonalDataRepository interface {
	// Create inserts a new personal data record.
	Create(ctx context.Context, data *PersonalData) error

	// Update replaces the personal data for an existing record.
	Update(ctx context.Context, data *PersonalData) error

	// FindBySessionID returns the personal data for a session.
	FindBySessionID(ctx context.Context, sessionID string) (*PersonalData, error)
}

// OTPCache manages OTP storage and attempt tracking in Redis.
type OTPCache interface {
	// StoreOTP stores a hashed OTP with TTL. Returns expiry time.
	StoreOTP(ctx context.Context, sessionID, otpHash string, ttl time.Duration) (expiresAt time.Time, err error)

	// GetOTP retrieves the stored OTP hash. Returns "", nil if not found/expired.
	GetOTP(ctx context.Context, sessionID string) (otpHash string, err error)

	// DeleteOTP removes the stored OTP.
	DeleteOTP(ctx context.Context, sessionID string) error

	// IncrAttempt increments and returns the OTP attempt count for a session.
	// The counter auto-expires after the window.
	IncrAttempt(ctx context.Context, sessionID string) (attempts int64, err error)

	// IncrResend counts one nasabah-requested resend against the session's
	// hourly quota and returns the new total. Automatic regeneration after
	// repeated failures does not go through here — it is not the nasabah's
	// doing and must not eat their quota.
	IncrResend(ctx context.Context, sessionID string) (count int64, err error)

	// ResendWindowRemaining returns how long until the resend quota refills.
	ResendWindowRemaining(ctx context.Context, sessionID string) (time.Duration, error)

	// ResetResend clears the resend quota for a session.
	ResetResend(ctx context.Context, sessionID string) error

	// IncrPhoneSend counts one SMS against the destination number's hourly
	// budget and returns the new total.
	//
	// Keyed by the number, NOT by the session, and that is the whole point: the
	// resend quota above is per session, and a session costs an attacker
	// nothing to replace. Without a per-number ceiling, farming OTPs to one
	// handset is bounded only by the per-IP rate limit — the SMS bill and the
	// victim's inbox are ours either way.
	IncrPhoneSend(ctx context.Context, phone string) (count int64, err error)

	// PhoneSendWindowRemaining returns how long until the number's budget
	// refills. Feeds details.retry_after_seconds.
	PhoneSendWindowRemaining(ctx context.Context, phone string) (time.Duration, error)

	// IsBlocked checks if OTP verification is blocked for this session.
	IsBlocked(ctx context.Context, sessionID string) (bool, error)

	// Block blocks OTP verification for a session for the given duration.
	Block(ctx context.Context, sessionID string, duration time.Duration) error

	// BlockRemaining returns the remaining block duration. Returns 0 if not blocked.
	BlockRemaining(ctx context.Context, sessionID string) (time.Duration, error)

	// ResetAttempts clears the failed-attempt counter after a successful verify.
	ResetAttempts(ctx context.Context, sessionID string) error
}

// SMSGateway sends OTP messages via SMS.
type SMSGateway interface {
	SendOTP(ctx context.Context, phone, otp string) error
}

// OTPVerifier is a provider that owns the code instead of carrying ours.
//
// Only one of SMSGateway or OTPVerifier is active at a time, chosen in
// router.New from SMS_PROVIDER. With a verifier there is no local code to
// generate, hash or store: the provider makes it, keeps it, and is the only
// thing that can say whether the nasabah typed it right.
//
// What stays ours either way — and this is the point of keeping the seam here
// rather than swapping the whole flow — is every policy decision: step and device
// checks, the failure counter and 30-minute lockout, the per-session resend
// quota, the per-number hourly ceiling, and the audit trail. What we lose is
// otp_debug, because the code never passes through this process.
type OTPVerifier interface {
	// StartVerification asks the provider to generate and send a code over ch.
	//
	// An empty ch is the provider's default (SMS). A ch the deployment has not
	// enabled is sms.ErrChannelNotAllowed, and nothing is sent.
	StartVerification(ctx context.Context, phone string, ch sms.Channel) error

	// CheckVerification reports whether code matches. A wrong code is
	// (false, nil); an error means we could not ask.
	CheckVerification(ctx context.Context, phone, code string) (approved bool, err error)

	// CodeTTL is the provider's code lifetime, used for otp_expires_at.
	//
	// Asked of the provider rather than kept as a constant here: this service
	// does not set the expiry, Twilio's Verify service does, and a copy of the
	// number in this package is a second source of truth that drifts the first
	// time somebody changes it in the console.
	CodeTTL() time.Duration
}

// BiometricRepository persists biometric verification results.
type BiometricRepository interface {
	Create(ctx context.Context, result *BiometricResult) error
	FindBySessionID(ctx context.Context, sessionID string) (*BiometricResult, error)
}

// BiometricEngine abstracts face liveness and matching (Google Vision, AWS Rekognition, etc.).
type BiometricEngine interface {
	// Analyze performs liveness detection and face matching.
	// facePhoto is the main face photo, livenessFrames are challenge frames,
	// ktpPhoto is the KTP photo for face comparison.
	Analyze(ctx context.Context, facePhoto []byte, livenessFrames [][]byte, ktpPhoto []byte) (*FaceAnalysisResult, error)
}

// BiometricRateLimiter checks biometric-specific rate limits per session.
type BiometricRateLimiter interface {
	CheckBiometricAttempt(ctx context.Context, sessionID string) (allowed bool, err error)
}

// VideoCallRepository persists video call records.
type VideoCallRepository interface {
	Create(ctx context.Context, vc *VideoCall) error
	FindByQueueID(ctx context.Context, queueID string) (*VideoCall, error)
	FindBySessionID(ctx context.Context, sessionID string) (*VideoCall, error)
	UpdateResult(ctx context.Context, queueID string, result VideoCallResult, agentEmployeeID, agentName, notes, recordingID string, ktpShown, identityConfirmed bool, durationSeconds int) error

	// MarkActive menandai panggilan sedang berlangsung dan mencatat siapa agennya.
	//
	// Dipanggil saat CS mengambil panggilan lewat /video-call/agent-token. Sebelum ini
	// status tidak pernah meninggalkan QUEUED dan kolom agent_name selalu NULL, sehingga
	// panggilan yang sedang berjalan tidak bisa dibedakan dari yang masih mengantre.
	MarkActive(ctx context.Context, queueID, agentEmployeeID, agentName string) error

	// Cancel membebaskan panggilan yang masih QUEUED atau ACTIVE.
	//
	// Wajib ada karena `idx_vc_session_active_unique` (migrasi 000017) adalah unique
	// partial index pada `session_id WHERE status IN ('QUEUED','ACTIVE')`: satu baris
	// ACTIVE yang agennya hilang tanpa menyubmit hasil **mengurung sesi itu selamanya**
	// — ia tidak bisa mengantre lagi karena baris barunya akan menabrak indeks itu.
	// CANCELLED sebelumnya hanya sebuah konstanta yang tidak pernah ditulis siapa pun.
	//
	// Mengembalikan false kalau tidak ada baris yang berubah (sudah COMPLETED, sudah
	// CANCELLED, atau tidak ada) — bukan error, karena semua pemanggilnya adalah jalur
	// pembersihan yang harus idempoten.
	//
	// Alasan pembatalan TIDAK diteruskan ke sini: ia tidak punya kolom, dan yang menulisnya
	// ke audit trail adalah service. Parameter yang diabaikan implementasinya hanya membuat
	// pemanggil berikutnya mengira ia tersimpan di suatu tempat.
	Cancel(ctx context.Context, queueID string) (bool, error)
}

// VideoCallCanceller membatalkan panggilan hidup milik sebuah sesi.
//
// Antarmuka sempit, bukan `*VideoCallService` langsung, mengikuti pola yang sudah dipakai
// `SignalingNotifier` dan `SignalingLifecycle`: `SessionService` hanya perlu memberi tahu
// satu hal saat sesi dibatalkan, dan tidak ada alasan ia mengenal repository panggilan,
// sorted set antrean, maupun hub signaling untuk itu.
type VideoCallCanceller interface {
	CancelForSession(ctx context.Context, sessionID string) error
}

// VideoCallQueueCache manages the video call queue in Redis sorted set.
type VideoCallQueueCache interface {
	// Add adds a session to the queue. Score = join timestamp.
	Add(ctx context.Context, queueID string, score float64) error
	// Remove removes a session from the queue.
	Remove(ctx context.Context, queueID string) error
	// Position returns 1-based position in the queue. 0 if not found.
	Position(ctx context.Context, queueID string) (int64, error)
	// Length returns the total queue size.
	Length(ctx context.Context) (int64, error)
	// List returns queued queue_ids ordered front-first.
	List(ctx context.Context) ([]string, error)
	// IncrDailyCounter increments and returns a daily sequential counter for queue numbers.
	IncrDailyCounter(ctx context.Context) (int64, error)
}

// CredentialRepository persists hashed credentials.
type CredentialRepository interface {
	Create(ctx context.Context, cred *Credential) error
	FindBySessionID(ctx context.Context, sessionID string) (*Credential, error)
}

// AuditRepository inserts and queries onboarding audit log entries (append-only).
type AuditRepository interface {
	Insert(ctx context.Context, log *AuditLog) error
	FindBySessionID(ctx context.Context, sessionID string) ([]*AuditLog, error)
}

// CoreBankingClient abstracts the core banking system for account creation.
type CoreBankingClient interface {
	CreateAccount(ctx context.Context, sessionID string, productType ProductType, holderName, nik string) (*CoreBankingResult, error)

	// IssueCard meminta core banking mencetak kartu. Kegagalannya tidak boleh
	// membatalkan rekening yang sudah jadi — pemanggil memasukkannya ke antrean
	// retry, bukan mengembalikan error ke nasabah (§10).
	IssueCard(ctx context.Context, req CardIssuanceRequest) (*CardIssuanceResult, error)
}

// CardIssuanceRepository menyimpan antrean permintaan cetak kartu.
type CardIssuanceRepository interface {
	// Claim menyisipkan permintaan cetak untuk satu sesi, sekali saja.
	//
	// Mengembalikan false bila baris untuk sesi itu sudah ada. Di situlah
	// jaminan "submit ulang tidak mencetak dua kartu" benar-benar ditegakkan:
	// Idempotency-Key menjaga di Redis, yang bisa hilang karena eviction;
	// UNIQUE session_id di Postgres tidak bisa.
	Claim(ctx context.Context, issuance CardIssuance) (bool, error)

	// MarkResult menyimpan hasil penerbitan yang berhasil.
	MarkResult(ctx context.Context, sessionID string, result CardIssuanceResult) error

	// MarkFailed mencatat kegagalan dan menjadwalkan percobaan berikutnya.
	MarkFailed(ctx context.Context, sessionID, reason string, nextRetryAt time.Time) error

	// FindBySessionID mengembalikan permintaan cetak satu sesi. nil bila tidak ada.
	FindBySessionID(ctx context.Context, sessionID string) (*CardIssuance, error)

	// DueForRetry mengembalikan permintaan yang sudah waktunya diulang.
	DueForRetry(ctx context.Context, now time.Time, limit int) ([]CardIssuance, error)
}

// AccountCardRegistrar mencatat kartu terbitan sebagai kartu milik nasabah.
//
// Interface-nya didefinisikan di sini, bukan mengimpor domain/card: submit hanya
// perlu satu operasi tulis, dan domain tidak boleh saling bergantung untuk itu.
// Implementasinya repository/postgres.AccountCardRepo, yang sama dengan pembaca
// layar Profil Saya.
type AccountCardRegistrar interface {
	// RegisterIssuedCard menyisipkan kartu, dan diam bila kartu dengan nomor
	// tersamar yang sama sudah tercatat pada rekening itu: retry antrean cetak
	// tidak boleh melahirkan kartu kedua.
	RegisterIssuedCard(ctx context.Context, issued IssuedCard) error
}

// IdempotencyCache guards against duplicate onboarding submissions.
//
// Keys are scoped by session_id: an Idempotency-Key is chosen by the client and
// two sessions may well pick the same one, so an unscoped key would hand one
// nasabah another nasabah's account number.
type IdempotencyCache interface {
	// Claim atomically reserves the slot (SETNX, never GET-then-SET). A claim
	// that was already taken reports whether the first caller is still running
	// or has stored its response.
	Claim(ctx context.Context, sessionID, key string) (IdempotencyClaim, error)
	// Persist stores the successful response JSON with TTL.
	Persist(ctx context.Context, sessionID, key, responseJSON string) error
	// Release frees the slot after a failure so the client can retry.
	Release(ctx context.Context, sessionID, key string) error
}

// AccountProvisioner turns a completed onboarding session into the rows the
// rest of the API needs: an m-BCA user, the device binding, the account, and
// its default transaction limits — all in one transaction.
type AccountProvisioner interface {
	ProvisionAccount(ctx context.Context, params ProvisionParams) (*ProvisionResult, error)
}

// --- Pilih Jenis Kartu Paspor ---

// CardRepository membaca katalog kartu dari penyimpanan tahan lama.
//
// Implementasinya ada di repository/postgres. Domain tidak boleh tahu SQL —
// lihat aturan arah impor di CLAUDE.md.
type CardRepository interface {
	// ListCards mengembalikan kartu yang ditawarkan untuk satu produk, terurut
	// display_order. Baris dengan region_code cocok menang atas baris nasional
	// (region_code NULL) untuk kartu yang sama.
	ListCards(ctx context.Context, productType ProductType, regionCode string) ([]CardOption, error)

	// GetCard mengembalikan satu kartu pada satu produk. nil bila tidak ada.
	GetCard(ctx context.Context, productType ProductType, cardType string, regionCode string) (*CardOption, error)

	// CatalogVersion mengembalikan versi katalog saat ini.
	CatalogVersion(ctx context.Context) (string, error)

	// BumpCatalogVersion menaikkan versi katalog satu kali, format YYYY-MM-DD.n
	BumpCatalogVersion(ctx context.Context) (string, error)

	// LogCardSelection menulis jejak audit pemilihan/perubahan kartu.
	LogCardSelection(ctx context.Context, entry CardSelectionLogEntry) error
}

// CardCache menyimpan katalog yang sudah jadi, berkunci versi.
//
// Cache miss bukan kegagalan: pemanggil jatuh ke database. Redis mati tidak
// boleh membuat layar pilih kartu ikut mati.
type CardCache interface {
	// GetCatalog mengembalikan katalog untuk (produk, wilayah, versi).
	// nil, nil bila tidak ada di cache.
	GetCatalog(ctx context.Context, productType ProductType, regionCode, version string) (*CardCatalog, error)

	// SetCatalog menyimpan katalog dengan TTL.
	SetCatalog(ctx context.Context, productType ProductType, regionCode, version string, catalog *CardCatalog) error

	// GetVersion membaca versi katalog yang di-cache. "" bila tidak ada.
	GetVersion(ctx context.Context) (string, error)

	// SetVersion menyimpan versi katalog.
	SetVersion(ctx context.Context, version string) error

	// InvalidateVersion menghapus penanda versi dari cache.
	//
	// Dipakai saat penulisan katalog: tanpa ini, versi lama yang ter-cache
	// membuat GetCatalog terus membaca entri katalog lama, dan seluruh premis
	// "naikkan versi maka cache otomatis terlewat" tidak berlaku.
	InvalidateVersion(ctx context.Context) error
}

// CardFeatureFlag membaca status sisipan pilih kartu saat request berjalan.
//
// §13 mensyaratkan flag ini dibaca dari konfigurasi runtime, bukan environment
// variable yang butuh restart: gunanya justru sebagai jalan keluar ketika
// sisipan bermasalah di produksi, dan jalan keluar yang menuntut deploy ulang
// bukan jalan keluar.
type CardFeatureFlag interface {
	CardSelectionEnabled(ctx context.Context) bool
}

// TNCRepository membaca dua tabel S&K dari migrasi 000025.
// Diimplementasikan postgres.OnboardingTNCRepo — domain ini tidak tahu SQL.
type TNCRepository interface {
	// ActiveTNC mengembalikan versi yang sedang berlaku beserta pasal-pasalnya.
	//
	// Tidak ada versi aktif adalah apperr.TNCUnavailable, bukan nil: layar S&K
	// tanpa teks bukan layar kosong yang sah, dan sesi tidak boleh lahir dengan
	// persetujuan atas apa pun.
	ActiveTNC(ctx context.Context) (*TNCDocument, error)

	// TNCByVersion mengembalikan satu versi, aktif maupun sudah dicabut.
	// Dipakai ?version= untuk menampilkan kembali teks yang pernah disetujui.
	// Versi yang tidak ada mengembalikan apperr.TNCVersionUnknown.
	TNCByVersion(ctx context.Context, version string) (*TNCDocument, error)
}

// TNCCache adalah lapisan Redis opsional untuk S&K.
//
// Isinya sama untuk semua nasabah dan berubah beberapa kali setahun — kasus
// langka di mana TTL panjang benar, sama seperti content.Cache.
//
// Setiap method boleh gagal: galat cache harus turun ke pembacaan database,
// bukan ke halaman error. Service mencatat dan melanjutkan.
type TNCCache interface {
	// GetTNC membaca satu versi dari cache. nil, nil bila tidak ada.
	// version "" berarti entri versi aktif.
	GetTNC(ctx context.Context, version string) (*TNCDocument, error)

	// SetTNC menyimpan satu versi dengan TTL.
	SetTNC(ctx context.Context, version string, doc *TNCDocument) error
}
