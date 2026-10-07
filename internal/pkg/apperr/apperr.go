package apperr

import (
	"errors"
	"net/http"
)

type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e Error) Error() string {
	return e.Code + ": " + e.Message
}

// --- 400 ---

var ValidationError = Error{http.StatusBadRequest, "VALIDATION_ERROR", "Permintaan tidak valid.", nil}

// --- 401 ---

var (
	InvalidPIN               = Error{http.StatusUnauthorized, "AUTH_INVALID_PIN", "Kode akses salah. Silakan coba lagi.", nil}
	TokenExpired             = Error{http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "Sesi Anda telah berakhir. Silakan login kembali.", nil}
	TokenInvalid             = Error{http.StatusUnauthorized, "AUTH_TOKEN_INVALID", "Token tidak valid.", nil}
	BiometricNotRegistered   = Error{http.StatusUnauthorized, "AUTH_BIOMETRIC_NOT_REGISTERED", "Biometrik belum terdaftar pada perangkat ini.", nil}
	VerificationTokenInvalid = Error{http.StatusUnauthorized, "VERIFICATION_TOKEN_INVALID", "Token verifikasi tidak valid atau sudah digunakan.", nil}
)

// --- 403 ---

var (
	DeviceNotRecognized = Error{http.StatusForbidden, "AUTH_DEVICE_NOT_RECOGNIZED", "Perangkat tidak dikenali.", nil}

	// SourceAccountForbidden is returned when a request names an account the
	// caller does not own. It is deliberately a 403 with a generic message and
	// not a 404: the two used to be indistinguishable because no ownership
	// check existed at all, and a debit could be aimed at any account UUID.
	SourceAccountForbidden = Error{http.StatusForbidden, "ACCOUNT_FORBIDDEN", "Rekening tidak dapat digunakan untuk transaksi ini.", nil}

	// SessionRevoked is returned when a structurally valid access token belongs
	// to a session that has been logged out or revoked.
	SessionRevoked = Error{http.StatusUnauthorized, "AUTH_SESSION_REVOKED", "Sesi Anda sudah berakhir. Silakan login kembali.", nil}

	// OnboardingDeviceMismatch is returned when X-Device-ID does not match the
	// device that created the onboarding session. A session_id is a bearer
	// secret on its own: without this check, a leaked id could be continued
	// from any phone. 403 rather than 404 is deliberate and was asked for by
	// the Android client (docs/10-HANDOVER-BLOCKER-BACKEND.md butir 3): the id
	// is a v4 UUID, so nothing is enumerable, and a distinct code is the
	// difference between a one-line fix and a day of guessing.
	OnboardingDeviceMismatch = Error{http.StatusForbidden, "ONBOARDING_DEVICE_MISMATCH", "Sesi ini dibuat dari perangkat lain.", nil}
)

// --- 404 ---

var (
	NotFound                = Error{http.StatusNotFound, "NOT_FOUND", "Resource tidak ditemukan.", nil}
	AccountNotFound         = Error{http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Rekening tidak ditemukan.", nil}
	TransferAccountNotFound = Error{http.StatusNotFound, "TRANSFER_ACCOUNT_NOT_FOUND", "Rekening tujuan tidak ditemukan.", nil}
	EWalletAccountNotFound  = Error{http.StatusNotFound, "EWALLET_ACCOUNT_NOT_FOUND", "Akun e-wallet tidak ditemukan.", nil}
)

// --- 404 (onboarding) ---

var OnboardingNotFound = Error{http.StatusNotFound, "ONBOARDING_NOT_FOUND", "Sesi onboarding tidak ditemukan.", nil}

// --- 409 ---

var IdempotencyConflict = Error{http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Transaksi sedang diproses.", nil}

// --- 422 ---

var (
	OldPINMismatch = Error{http.StatusUnprocessableEntity, "AUTH_OLD_PIN_MISMATCH", "PIN lama tidak sesuai.", nil}

	// PINKeyUnknown is returned when a request names an encryption_key_id that
	// is not the key this server decrypts with. Without it a client encrypting
	// under a rotated-out key sees AUTH_INVALID_PIN — indistinguishable from a
	// wrong PIN, and the reason the handover document calls this the most
	// expensive class of bug to trace.
	PINKeyUnknown = Error{http.StatusUnprocessableEntity, "AUTH_PIN_KEY_UNKNOWN", "Kunci enkripsi PIN tidak dikenal. Perbarui kunci publik.", nil}

	// BiometricKeyUnsupported is returned at registration when the public key
	// is not EC P-256. Accepting it would push the failure to login time,
	// where it looks like a bad fingerprint instead of a bad key type.
	BiometricKeyUnsupported = Error{http.StatusUnprocessableEntity, "AUTH_BIOMETRIC_KEY_UNSUPPORTED", "Jenis kunci biometrik tidak didukung. Gunakan EC P-256.", nil}

	InquiryExpired             = Error{http.StatusUnprocessableEntity, "INQUIRY_EXPIRED", "Sesi transaksi sudah kedaluwarsa. Silakan ulangi.", nil}
	InquiryMismatch            = Error{http.StatusUnprocessableEntity, "INQUIRY_MISMATCH", "Data transaksi tidak sesuai dengan inquiry.", nil}
	InsufficientBalance        = Error{http.StatusUnprocessableEntity, "TRANSFER_INSUFFICIENT_BALANCE", "Saldo tidak mencukupi.", nil}
	TransferLimitExceeded      = Error{http.StatusUnprocessableEntity, "TRANSFER_LIMIT_EXCEEDED", "Transaksi melebihi limit harian.", nil}
	SelfTransfer               = Error{http.StatusUnprocessableEntity, "TRANSFER_SELF_TRANSFER", "Tidak dapat transfer ke rekening sendiri.", nil}
	EWalletInsufficientBalance = Error{http.StatusUnprocessableEntity, "EWALLET_INSUFFICIENT_BALANCE", "Saldo tidak mencukupi untuk top-up.", nil}
	QRISInvalidPayload         = Error{http.StatusBadRequest, "QRIS_INVALID_PAYLOAD", "Data QR code tidak valid.", nil}
	QRISInsufficientBalance    = Error{http.StatusUnprocessableEntity, "QRIS_INSUFFICIENT_BALANCE", "Saldo tidak mencukupi untuk pembayaran QRIS.", nil}
	QRISLimitExceeded          = Error{http.StatusUnprocessableEntity, "QRIS_LIMIT_EXCEEDED", "Pembayaran melebihi limit QRIS.", nil}
)

// --- Pilih Jenis Kartu Paspor (docs/08-PILIH-KARTU-API-SPEC.md) ---

var (
	// OnboardingProductUnknown: product_type di luar enum. 404, bukan 422 —
	// path-nya menunjuk sumber daya yang memang tidak ada.
	OnboardingProductUnknown = Error{http.StatusNotFound, "ONBOARDING_PRODUCT_UNKNOWN", "Jenis produk tidak dikenal.", nil}

	// CardCatalogEmpty juga dipakai saat feature flag pilih kartu dimatikan,
	// supaya client lama melihat keadaan yang sama dengan katalog kosong dan
	// jatuh kembali ke flow lama tanpa perlu tahu soal flag.
	CardCatalogEmpty = Error{http.StatusNotFound, "CARD_CATALOG_EMPTY", "Belum ada pilihan kartu untuk produk ini.", nil}

	CardTypeInvalid = Error{http.StatusUnprocessableEntity, "CARD_TYPE_INVALID", "Jenis kartu tidak dikenal untuk produk ini.", nil}

	// CardTypeUnavailable 409, bukan 422: kartunya sah, keadaannya yang
	// berubah sejak katalog dibaca (stok habis, ditarik).
	CardTypeUnavailable = Error{http.StatusConflict, "CARD_TYPE_UNAVAILABLE", "Kartu ini sedang tidak tersedia.", nil}

	CardNotEligible = Error{http.StatusUnprocessableEntity, "CARD_NOT_ELIGIBLE", "Anda belum memenuhi syarat untuk kartu ini.", nil}

	// CardLocked: kartu tidak bisa diubah setelah sesi disubmit.
	CardLocked = Error{http.StatusConflict, "CARD_LOCKED", "Kartu tidak dapat diubah setelah pengajuan dikirim.", nil}

	// CardCatalogInvalidValue dipakai admin API katalog kartu: nilai di luar
	// daftar yang dikenal client ditolak 422, bukan 400 — badannya bisa dibaca,
	// isinya yang tidak bisa diproses. Details selalu memuat `field`, dan
	// `allowed_values` bila memang ada daftar tertutupnya, supaya admin bisa
	// memperbaiki pada percobaan pertama tanpa membuka kode.
	CardCatalogInvalidValue = Error{http.StatusUnprocessableEntity, "CARD_CATALOG_INVALID_VALUE", "Nilai katalog kartu tidak valid.", nil}

	// --- Kartu MILIK nasabah (/account/cards) ---
	//
	// Terpisah dari blok di atas yang melayani katalog onboarding. Kartu milik
	// nasabah punya siklus hidup sendiri, jadi salah satunya tidak bisa
	// meminjam kode error yang lain tanpa membuat pesan di layar keliru.

	// CardNotFound 404 juga dipakai saat kartu ADA tapi milik orang lain.
	// Membedakan keduanya berarti mengonfirmasi bahwa sebuah card_id tebakan
	// itu nyata.
	CardNotFound = Error{http.StatusNotFound, "CARD_NOT_FOUND", "Kartu tidak ditemukan.", nil}

	// CardBlocked 409: sakelar kanal tidak bisa diubah pada kartu terblokir.
	// Membuka blokir bukan tombol di aplikasi — itu keputusan cabang.
	CardBlocked = Error{http.StatusConflict, "CARD_BLOCKED", "Kartu sedang diblokir. Hubungi Halo BCA untuk membukanya.", nil}

	// CardReplacementInProgress 409: sudah ada permintaan penggantian yang
	// belum selesai untuk kartu ini. Ditegakkan oleh unique index parsial di
	// migrasi 000021, bukan oleh pemeriksaan di aplikasi — dua request bersamaan
	// tidak boleh dua-duanya lolos.
	CardReplacementInProgress = Error{http.StatusConflict, "CARD_REPLACEMENT_IN_PROGRESS", "Permintaan penggantian kartu sebelumnya masih diproses.", nil}

	// CardDeliveryUnavailable 422: metode pengiriman yang diminta tidak
	// dilayani untuk jenis kartu ini menurut katalog.
	CardDeliveryUnavailable = Error{http.StatusUnprocessableEntity, "CARD_DELIVERY_UNAVAILABLE", "Metode pengiriman ini tidak tersedia untuk kartu Anda.", nil}
)

// --- Syarat & Ketentuan buka rekening (migrasi 000025) ---

var (
	// TNCVersionOutdated 409, bukan 422: versi yang dikirim client memang
	// pernah sah — keadaannya yang berubah sejak teks itu dibaca, persis
	// seperti CardTypeUnavailable.
	//
	// Details SELALU memuat `current_version` supaya aplikasi bisa memuat ulang
	// S&K dan menampilkan teks baru tanpa menebak. Tanpa penolakan ini, bank
	// mencatat persetujuan atas pasal yang sudah dicabut dan tidak pernah
	// dilihat nasabah — lihat komentar di migrasi 000025.
	TNCVersionOutdated = Error{http.StatusConflict, "TNC_VERSION_OUTDATED", "Syarat & Ketentuan telah diperbarui. Mohon baca dan setujui versi terbaru.", nil}

	// TNCVersionUnknown 422: versi yang tidak pernah ada di database. Dipisah
	// dari TNCVersionOutdated karena artinya berbeda — yang satu client lama,
	// yang satu client yang mengarang nilai (dan dulu diterima apa adanya).
	TNCVersionUnknown = Error{http.StatusUnprocessableEntity, "TNC_VERSION_UNKNOWN", "Versi Syarat & Ketentuan tidak dikenal.", nil}

	// TNCUnavailable 503: tidak ada satu pun versi aktif di database. Itu salah
	// konfigurasi server (migrasi belum jalan), bukan salah client — jadi 5xx,
	// dan jangan sampai terbaca sebagai "nasabah mengirim sesuatu yang salah".
	TNCUnavailable = Error{http.StatusServiceUnavailable, "TNC_UNAVAILABLE", "Syarat & Ketentuan belum tersedia. Silakan coba beberapa saat lagi.", nil}

	// OnboardingCatalogUnavailable 503, pola yang sama dengan TNCUnavailable: tidak
	// ada satu pun produk aktif, atau feature flag katalognya mati. Dua-duanya salah
	// konfigurasi server, bukan salah client.
	//
	// 503 dan BUKAN 404 atau daftar kosong: client jatuh ke fallback strings.xml saat
	// menerima ini, dan daftar kosong akan membuatnya menampilkan layar tanpa pilihan
	// — nasabah berhenti di layar pertama tanpa tahu kenapa.
	//
	// Dipisah dari ONBOARDING_PRODUCT_UNKNOWN (404) dan ONBOARDING_PRODUCT_UNAVAILABLE
	// (422) yang sudah ada: keduanya tentang SATU produk yang diminta client, ini
	// tentang katalognya yang tidak bisa dilayani sama sekali.
	OnboardingCatalogUnavailable = Error{http.StatusServiceUnavailable, "ONBOARDING_CATALOG_UNAVAILABLE", "Daftar jenis rekening belum tersedia. Silakan coba beberapa saat lagi.", nil}
)

// --- 422 (onboarding) ---

var (
	OnboardingSessionExpired     = Error{http.StatusUnprocessableEntity, "ONBOARDING_SESSION_EXPIRED", "Sesi onboarding sudah kedaluwarsa (24 jam).", nil}
	OnboardingProductUnavailable = Error{http.StatusUnprocessableEntity, "ONBOARDING_PRODUCT_UNAVAILABLE", "Produk tidak tersedia.", nil}
	OnboardingSessionLimit       = Error{http.StatusTooManyRequests, "ONBOARDING_SESSION_LIMIT", "Maksimal 3 sesi aktif per perangkat.", nil}
	OnboardingIncomplete         = Error{http.StatusUnprocessableEntity, "ONBOARDING_INCOMPLETE", "Belum semua langkah selesai.", nil}
)

// --- Petugas & terminal CS ---

var (
	// 401, bukan 403: kredensial login yang salah berbeda dari kewenangan yang kurang.
	// Jalur header petugas tetap 403 — di sana penyerang tidak boleh bisa membedakan
	// kunci sistem salah dari kunci petugas salah.
	AgentCredentialInvalid = Error{http.StatusUnauthorized, "AGENT_CREDENTIAL_INVALID", "NPP atau kata sandi salah.", nil}

	// Petugas yang belum pernah menyetel kata sandi tidak sedang salah mengetik —
	// menjawabnya "kata sandi salah" akan membuatnya mencoba lagi selamanya.
	AgentPasswordNotSet = Error{http.StatusUnprocessableEntity, "AGENT_PASSWORD_NOT_SET", "Kata sandi belum disetel. Hubungi supervisor untuk penyetelan awal.", nil}

	AgentLocked = Error{http.StatusLocked, "AGENT_LOCKED", "Akun terkunci sementara karena terlalu banyak percobaan gagal.", nil}

	AgentPasswordWeak = Error{http.StatusUnprocessableEntity, "AGENT_PASSWORD_WEAK", "Kata sandi minimal 12 karakter.", nil}

	AgentSessionInvalid = Error{http.StatusUnauthorized, "AGENT_SESSION_INVALID", "Sesi tidak berlaku. Silakan masuk kembali.", nil}

	AgentSessionNotFound = Error{http.StatusNotFound, "AGENT_SESSION_NOT_FOUND", "Tidak ada sesi aktif.", nil}

	TerminalNotFound = Error{http.StatusNotFound, "TERMINAL_NOT_FOUND", "Terminal tidak terdaftar.", nil}

	TerminalAlreadyRegistered = Error{http.StatusConflict, "TERMINAL_ALREADY_REGISTERED", "Terminal dengan ID itu sudah terdaftar.", nil}

	// Rule 3: aktivasi menuntut ketiga gerbang lolos dan belum kedaluwarsa.
	TerminalNotReady = Error{http.StatusUnprocessableEntity, "TERMINAL_NOT_READY", "Terminal belum siap. Selesaikan otorisasi supervisor, healthcheck perangkat, dan pakta integritas.", nil}

	// Rule 4: hanya petugas di terminal ONLINE yang boleh mengambil antrean.
	TerminalNotOnline = Error{http.StatusUnprocessableEntity, "TERMINAL_NOT_ONLINE", "Terminal belum aktif. Selesaikan kesiapan terminal lebih dulu.", nil}

	TerminalAgentBusy = Error{http.StatusConflict, "TERMINAL_AGENT_BUSY", "Anda masih aktif di terminal lain. Tutup giliran di sana lebih dulu.", nil}

	// Petugas yang sudah memegang panggilan tidak boleh mengambil yang kedua.
	AgentCallInProgress = Error{http.StatusConflict, "AGENT_CALL_IN_PROGRESS", "Anda masih menangani panggilan lain.", nil}

	SupervisorNotFound = Error{http.StatusNotFound, "SUPERVISOR_NOT_FOUND", "Supervisor tidak ditemukan.", nil}

	SupervisorTokenInvalid = Error{http.StatusUnauthorized, "SUPERVISOR_TOKEN_INVALID", "Token otorisasi supervisor tidak sah.", nil}

	// HRIS adalah sistem luar. 503, bukan 404: 404 akan terbaca sebagai "NPP tidak
	// terdaftar" dan membuat pegawai mengira dirinya salah ketik.
	HRISUnavailable = Error{http.StatusServiceUnavailable, "HRIS_UNAVAILABLE", "Direktori pegawai sedang tidak tersedia.", nil}

	// Tiga jawaban berbeda yang tidak boleh disatukan: ditemukan & aktif, ditemukan
	// tapi nonaktif, tidak ditemukan. Menyamakan dua yang terakhir membuat pegawai yang
	// statusnya dicabut mengira ia salah ketik.
	EmployeeNotFound = Error{http.StatusNotFound, "EMPLOYEE_NOT_FOUND", "NPP tidak ditemukan di direktori pegawai.", nil}

	EmployeeInactive = Error{http.StatusUnprocessableEntity, "EMPLOYEE_INACTIVE", "Status kepegawaian NPP ini tidak aktif.", nil}

	AgentAlreadyRegistered = Error{http.StatusConflict, "AGENT_ALREADY_REGISTERED", "NPP ini sudah terdaftar sebagai petugas.", nil}
)

// --- 422 (eskalasi verifikasi) ---

var (
	// Nasabah NEED_REVIEW tetap di langkah VIDEO_CALL, tapi tidak boleh mengantre lagi:
	// Tier 1 akan melayaninya dan menghasilkan keputusan yang sama.
	VideoCallUnderReview = Error{http.StatusUnprocessableEntity, "VIDEO_CALL_UNDER_REVIEW", "Verifikasi Anda sedang ditinjau petugas. Mohon tunggu, kami akan menghubungi Anda.", nil}

	VideoCallEscalationExists = Error{http.StatusConflict, "VIDEO_CALL_ESCALATION_EXISTS", "Sesi ini sudah dalam peninjauan.", nil}
)

// --- 422 (penjadwalan video call) ---

var (
	VideoCallAlreadyScheduled = Error{http.StatusUnprocessableEntity, "VIDEO_CALL_ALREADY_SCHEDULED", "Anda sudah punya jadwal video call. Batalkan dulu untuk menjadwalkan ulang.", nil}
	VideoCallScheduleInvalid  = Error{http.StatusUnprocessableEntity, "VIDEO_CALL_SCHEDULE_INVALID", "Waktu yang dipilih tidak tersedia. Pilih jam 06:00-22:00 WIB, maksimal 7 hari ke depan.", nil}
	VideoCallScheduleNotFound = Error{http.StatusNotFound, "VIDEO_CALL_SCHEDULE_NOT_FOUND", "Tidak ada jadwal video call yang bisa dibatalkan.", nil}
)

// --- 422 (OCR) ---

var (
	OCRNotKTP              = Error{http.StatusUnprocessableEntity, "OCR_NOT_KTP", "Dokumen bukan e-KTP yang valid.", nil}
	OCRPhotoBlurry         = Error{http.StatusUnprocessableEntity, "OCR_PHOTO_BLURRY", "Foto terlalu buram. Silakan ambil ulang dengan pencahayaan yang baik.", nil}
	OCRGlareDetected       = Error{http.StatusUnprocessableEntity, "OCR_GLARE_DETECTED", "Terdeteksi pantulan cahaya pada foto. Hindari flash dan cahaya langsung.", nil}
	OCRCornersMissing      = Error{http.StatusUnprocessableEntity, "OCR_CORNERS_MISSING", "Seluruh sudut KTP harus terlihat dalam foto.", nil}
	OCRDukcapilTimeout     = Error{http.StatusServiceUnavailable, "OCR_DUKCAPIL_TIMEOUT", "Verifikasi Dukcapil timeout. Silakan coba lagi.", nil}
	OCRDukcapilUnavailable = Error{http.StatusServiceUnavailable, "OCR_DUKCAPIL_UNAVAILABLE", "Layanan Dukcapil sedang tidak tersedia. Silakan coba beberapa saat lagi.", nil}
	OCRDukcapilMismatch    = Error{http.StatusUnprocessableEntity, "OCR_DUKCAPIL_MISMATCH", "Data NIK tidak cocok dengan data Dukcapil.", nil}
)

// --- 422 (personal data & OTP) ---

var (
	PersonalDataNIKMismatch  = Error{http.StatusUnprocessableEntity, "PERSONAL_DATA_NIK_MISMATCH", "NIK tidak sesuai dengan hasil OCR.", nil}
	PersonalDataNamaMismatch = Error{http.StatusUnprocessableEntity, "PERSONAL_DATA_NAMA_MISMATCH", "Nama tidak sesuai dengan hasil OCR.", nil}
	PersonalDataInvalidPhone = Error{http.StatusUnprocessableEntity, "PERSONAL_DATA_INVALID_PHONE", "Format nomor HP tidak valid. Gunakan format 08xx atau +628xx.", nil}
	PersonalDataInvalidEmail = Error{http.StatusUnprocessableEntity, "PERSONAL_DATA_INVALID_EMAIL", "Format email tidak valid.", nil}
	OTPInvalid               = Error{http.StatusUnprocessableEntity, "OTP_INVALID", "Kode OTP tidak valid.", nil}
	OTPExpired               = Error{http.StatusUnprocessableEntity, "OTP_EXPIRED", "Kode OTP sudah kedaluwarsa. OTP baru telah dikirim.", nil}
	OTPBlocked               = Error{http.StatusTooManyRequests, "OTP_BLOCKED", "Terlalu banyak percobaan OTP. Coba lagi dalam 30 menit.", nil}
)

// OTPDeliveryFailed is answered when the code was issued and stored but the
// SMS gateway refused it. Swallowing this used to answer 200 with an
// otp_expires_at for a code that was never sent: the nasabah sat waiting for
// an SMS that would never arrive, with nothing on screen suggesting a resend.
// The OTP stays valid — only the response says delivery failed.
var OTPDeliveryFailed = Error{http.StatusServiceUnavailable, "OTP_DELIVERY_FAILED", "Kode OTP gagal dikirim. Silakan coba kirim ulang.", nil}

// OTPChannelNotAllowed is answered when the request asked for a delivery channel
// this deployment has not enabled, or one the active provider cannot serve.
//
// 400 rather than 503: nothing failed. The channel was refused before anything
// was sent, so the nasabah's code, counters and send budget are all untouched and
// retrying on the default channel works immediately.
var OTPChannelNotAllowed = Error{http.StatusBadRequest, "OTP_CHANNEL_NOT_ALLOWED", "Metode pengiriman OTP tersebut tidak tersedia. Silakan gunakan SMS.", nil}

// --- 422 (biometric) ---

var (
	BioLivenessFailed = Error{http.StatusUnprocessableEntity, "BIO_LIVENESS_FAILED", "Gagal deteksi keaktifan. Silakan ulangi verifikasi wajah.", nil}
	BioFaceNotMatch   = Error{http.StatusUnprocessableEntity, "BIO_FACE_NOT_MATCH", "Wajah tidak cocok dengan foto KTP.", nil}
	BioMultipleFaces  = Error{http.StatusUnprocessableEntity, "BIO_MULTIPLE_FACES", "Lebih dari satu wajah terdeteksi. Pastikan hanya wajah Anda yang terlihat.", nil}
	BioLowQuality     = Error{http.StatusUnprocessableEntity, "BIO_LOW_QUALITY", "Kualitas foto tidak memadai. Pastikan pencahayaan baik.", nil}
	BioSpoofDetected  = Error{http.StatusUnprocessableEntity, "BIO_SPOOF_DETECTED", "Terdeteksi dugaan pemalsuan. Gunakan wajah asli tanpa foto/layar.", nil}
)

// --- 422 (submit / account creation) ---

var (
	AccountCreationFailed = Error{http.StatusUnprocessableEntity, "ACCOUNT_CREATION_FAILED", "Gagal membuat rekening. Silakan coba lagi.", nil}
)

// --- 422 (credentials) ---

var (
	CredWeakAccessCode   = Error{http.StatusUnprocessableEntity, "CRED_WEAK_ACCESS_CODE", "Kode akses terlalu lemah (berurutan atau berulang).", nil}
	CredWeakPIN          = Error{http.StatusUnprocessableEntity, "CRED_WEAK_PIN", "PIN terlalu lemah (berurutan atau berulang).", nil}
	CredSameAsAccessCode = Error{http.StatusUnprocessableEntity, "CRED_SAME_AS_ACCESS_CODE", "PIN tidak boleh sama dengan kode akses.", nil}
	CredDecryptionFailed = Error{http.StatusUnprocessableEntity, "CRED_DECRYPTION_FAILED", "Gagal mendekripsi kredensial. Pastikan key ID sesuai.", nil}
)

// --- 503 (provider not configured) ---

// ProviderNotConfigured is what an endpoint returns when the integration it
// depends on (OCR, Dukcapil, core banking, biometrics, object storage) has no
// real implementation in this environment. Previously those endpoints answered
// 200 with fabricated data in every environment, production included.
var ProviderNotConfigured = Error{http.StatusServiceUnavailable, "PROVIDER_NOT_CONFIGURED", "Layanan ini belum tersedia. Silakan coba beberapa saat lagi.", nil}

// --- 423 ---

var AccountLocked = Error{http.StatusLocked, "AUTH_ACCOUNT_LOCKED", "Akun terkunci karena terlalu banyak percobaan.", nil}

// --- 429 ---

var RateLimitExceeded = Error{http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Terlalu banyak permintaan. Coba lagi nanti.", nil}

// --- 500 ---

var InternalError = Error{http.StatusInternalServerError, "INTERNAL_ERROR", "Terjadi kesalahan pada sistem. Silakan coba lagi.", nil}

// --- 503 ---

var (
	EWalletProviderDown      = Error{http.StatusServiceUnavailable, "EWALLET_PROVIDER_DOWN", "Layanan provider sedang tidak tersedia.", nil}
	RegistrationNotFound     = Error{http.StatusNotFound, "REGISTRATION_NOT_FOUND", "Pendaftaran tidak ditemukan.", nil}
	RegistrationOTPInvalid   = Error{http.StatusUnprocessableEntity, "REGISTRATION_OTP_INVALID", "Kode OTP tidak valid.", nil}
	RegistrationOTPExpired   = Error{http.StatusUnprocessableEntity, "REGISTRATION_OTP_EXPIRED", "Kode OTP sudah kedaluwarsa.", nil}
	RegistrationTokenInvalid = Error{http.StatusUnauthorized, "REGISTRATION_TOKEN_INVALID", "Token registrasi tidak valid.", nil}
	RegistrationDuplicate    = Error{http.StatusConflict, "REGISTRATION_DUPLICATE", "NIK atau nomor telepon sudah terdaftar.", nil}
	RegistrationInvalidDoc   = Error{http.StatusBadRequest, "REGISTRATION_INVALID_DOCUMENT", "Format dokumen tidak valid. Gunakan JPEG atau PNG.", nil}
	MaintenanceMode          = Error{http.StatusServiceUnavailable, "MAINTENANCE_MODE", "Sistem sedang dalam pemeliharaan.", nil}
)

// From extracts an Error from err using errors.As.
// If err is not an Error, returns InternalError.
// INTERNAL_ERROR never carries the original error message to the client.
func From(err error) Error {
	var appErr Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return InternalError
}
