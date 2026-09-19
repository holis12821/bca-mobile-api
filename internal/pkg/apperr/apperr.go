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
	InvalidPIN              = Error{http.StatusUnauthorized, "AUTH_INVALID_PIN", "Kode akses salah. Silakan coba lagi.", nil}
	TokenExpired            = Error{http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED", "Sesi Anda telah berakhir. Silakan login kembali.", nil}
	TokenInvalid            = Error{http.StatusUnauthorized, "AUTH_TOKEN_INVALID", "Token tidak valid.", nil}
	BiometricNotRegistered  = Error{http.StatusUnauthorized, "AUTH_BIOMETRIC_NOT_REGISTERED", "Biometrik belum terdaftar pada perangkat ini.", nil}
	VerificationTokenInvalid = Error{http.StatusUnauthorized, "VERIFICATION_TOKEN_INVALID", "Token verifikasi tidak valid atau sudah digunakan.", nil}
)

// --- 403 ---

var DeviceNotRecognized = Error{http.StatusForbidden, "AUTH_DEVICE_NOT_RECOGNIZED", "Perangkat tidak dikenali.", nil}

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
	OldPINMismatch          = Error{http.StatusUnprocessableEntity, "AUTH_OLD_PIN_MISMATCH", "PIN lama tidak sesuai.", nil}
	InquiryExpired          = Error{http.StatusUnprocessableEntity, "INQUIRY_EXPIRED", "Sesi transaksi sudah kedaluwarsa. Silakan ulangi.", nil}
	InquiryMismatch         = Error{http.StatusUnprocessableEntity, "INQUIRY_MISMATCH", "Data transaksi tidak sesuai dengan inquiry.", nil}
	InsufficientBalance     = Error{http.StatusUnprocessableEntity, "TRANSFER_INSUFFICIENT_BALANCE", "Saldo tidak mencukupi.", nil}
	TransferLimitExceeded   = Error{http.StatusUnprocessableEntity, "TRANSFER_LIMIT_EXCEEDED", "Transaksi melebihi limit harian.", nil}
	SelfTransfer            = Error{http.StatusUnprocessableEntity, "TRANSFER_SELF_TRANSFER", "Tidak dapat transfer ke rekening sendiri.", nil}
	EWalletInsufficientBalance = Error{http.StatusUnprocessableEntity, "EWALLET_INSUFFICIENT_BALANCE", "Saldo tidak mencukupi untuk top-up.", nil}
	QRISInvalidPayload         = Error{http.StatusBadRequest, "QRIS_INVALID_PAYLOAD", "Data QR code tidak valid.", nil}
	QRISInsufficientBalance    = Error{http.StatusUnprocessableEntity, "QRIS_INSUFFICIENT_BALANCE", "Saldo tidak mencukupi untuk pembayaran QRIS.", nil}
	QRISLimitExceeded          = Error{http.StatusUnprocessableEntity, "QRIS_LIMIT_EXCEEDED", "Pembayaran melebihi limit QRIS.", nil}
)

// --- 422 (onboarding) ---

var (
	OnboardingSessionExpired    = Error{http.StatusUnprocessableEntity, "ONBOARDING_SESSION_EXPIRED", "Sesi onboarding sudah kedaluwarsa (24 jam).", nil}
	OnboardingProductUnavailable = Error{http.StatusUnprocessableEntity, "ONBOARDING_PRODUCT_UNAVAILABLE", "Produk tidak tersedia.", nil}
	OnboardingSessionLimit      = Error{http.StatusTooManyRequests, "ONBOARDING_SESSION_LIMIT", "Maksimal 3 sesi aktif per perangkat.", nil}
	OnboardingIncomplete        = Error{http.StatusUnprocessableEntity, "ONBOARDING_INCOMPLETE", "Belum semua langkah selesai.", nil}
)

// --- 422 (OCR) ---

var (
	OCRNotKTP           = Error{http.StatusUnprocessableEntity, "OCR_NOT_KTP", "Dokumen bukan e-KTP yang valid.", nil}
	OCRPhotoBlurry      = Error{http.StatusUnprocessableEntity, "OCR_PHOTO_BLURRY", "Foto terlalu buram. Silakan ambil ulang dengan pencahayaan yang baik.", nil}
	OCRGlareDetected    = Error{http.StatusUnprocessableEntity, "OCR_GLARE_DETECTED", "Terdeteksi pantulan cahaya pada foto. Hindari flash dan cahaya langsung.", nil}
	OCRCornersMissing   = Error{http.StatusUnprocessableEntity, "OCR_CORNERS_MISSING", "Seluruh sudut KTP harus terlihat dalam foto.", nil}
	OCRDukcapilTimeout  = Error{http.StatusServiceUnavailable, "OCR_DUKCAPIL_TIMEOUT", "Verifikasi Dukcapil timeout. Silakan coba lagi.", nil}
	OCRDukcapilMismatch = Error{http.StatusUnprocessableEntity, "OCR_DUKCAPIL_MISMATCH", "Data NIK tidak cocok dengan data Dukcapil.", nil}
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
	CredWeakAccessCode    = Error{http.StatusUnprocessableEntity, "CRED_WEAK_ACCESS_CODE", "Kode akses terlalu lemah (berurutan atau berulang).", nil}
	CredWeakPIN           = Error{http.StatusUnprocessableEntity, "CRED_WEAK_PIN", "PIN terlalu lemah (berurutan atau berulang).", nil}
	CredSameAsAccessCode  = Error{http.StatusUnprocessableEntity, "CRED_SAME_AS_ACCESS_CODE", "PIN tidak boleh sama dengan kode akses.", nil}
	CredDecryptionFailed  = Error{http.StatusUnprocessableEntity, "CRED_DECRYPTION_FAILED", "Gagal mendekripsi kredensial. Pastikan key ID sesuai.", nil}
)

// --- 423 ---

var AccountLocked = Error{http.StatusLocked, "AUTH_ACCOUNT_LOCKED", "Akun terkunci karena terlalu banyak percobaan.", nil}

// --- 429 ---

var RateLimitExceeded = Error{http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Terlalu banyak permintaan. Coba lagi nanti.", nil}

// --- 500 ---

var InternalError = Error{http.StatusInternalServerError, "INTERNAL_ERROR", "Terjadi kesalahan pada sistem. Silakan coba lagi.", nil}

// --- 503 ---

var (
	EWalletProviderDown       = Error{http.StatusServiceUnavailable, "EWALLET_PROVIDER_DOWN", "Layanan provider sedang tidak tersedia.", nil}
	RegistrationNotFound      = Error{http.StatusNotFound, "REGISTRATION_NOT_FOUND", "Pendaftaran tidak ditemukan.", nil}
	RegistrationOTPInvalid    = Error{http.StatusUnprocessableEntity, "REGISTRATION_OTP_INVALID", "Kode OTP tidak valid.", nil}
	RegistrationOTPExpired    = Error{http.StatusUnprocessableEntity, "REGISTRATION_OTP_EXPIRED", "Kode OTP sudah kedaluwarsa.", nil}
	RegistrationTokenInvalid  = Error{http.StatusUnauthorized, "REGISTRATION_TOKEN_INVALID", "Token registrasi tidak valid.", nil}
	RegistrationDuplicate     = Error{http.StatusConflict, "REGISTRATION_DUPLICATE", "NIK atau nomor telepon sudah terdaftar.", nil}
	RegistrationInvalidDoc    = Error{http.StatusBadRequest, "REGISTRATION_INVALID_DOCUMENT", "Format dokumen tidak valid. Gunakan JPEG atau PNG.", nil}
	MaintenanceMode     = Error{http.StatusServiceUnavailable, "MAINTENANCE_MODE", "Sistem sedang dalam pemeliharaan.", nil}
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