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
)

// --- 423 ---

var AccountLocked = Error{http.StatusLocked, "AUTH_ACCOUNT_LOCKED", "Akun terkunci karena terlalu banyak percobaan.", nil}

// --- 429 ---

var RateLimitExceeded = Error{http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Terlalu banyak permintaan. Coba lagi nanti.", nil}

// --- 500 ---

var InternalError = Error{http.StatusInternalServerError, "INTERNAL_ERROR", "Terjadi kesalahan pada sistem. Silakan coba lagi.", nil}

// --- 503 ---

var (
	EWalletProviderDown = Error{http.StatusServiceUnavailable, "EWALLET_PROVIDER_DOWN", "Layanan provider sedang tidak tersedia.", nil}
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