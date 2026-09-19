package onboarding

import (
	"context"
	"time"
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

	// IsBlocked checks if OTP verification is blocked for this session.
	IsBlocked(ctx context.Context, sessionID string) (bool, error)

	// Block blocks OTP verification for a session for the given duration.
	Block(ctx context.Context, sessionID string, duration time.Duration) error

	// BlockRemaining returns the remaining block duration. Returns 0 if not blocked.
	BlockRemaining(ctx context.Context, sessionID string) (time.Duration, error)
}

// SMSGateway sends OTP messages via SMS.
type SMSGateway interface {
	SendOTP(ctx context.Context, phone, otp string) error
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
}

// IdempotencyCache guards against duplicate onboarding submissions.
type IdempotencyCache interface {
	// Check returns the cached response JSON if the key exists. Returns "", nil if not found.
	Check(ctx context.Context, key string) (string, error)
	// Store caches the response JSON with TTL.
	Store(ctx context.Context, key, responseJSON string) error
}
