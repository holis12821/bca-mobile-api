package onboarding

import (
	"time"

	"github.com/google/uuid"
)

// ProductType enumerates available account products.
type ProductType string

const (
	ProductTahapanBCA  ProductType = "TAHAPAN_BCA"
	ProductTahapanXpre ProductType = "TAHAPAN_XPRESI"
	ProductTabunganku  ProductType = "TABUNGANKU"
)

var validProducts = map[ProductType]bool{
	ProductTahapanBCA:  true,
	ProductTahapanXpre: true,
	ProductTabunganku:  true,
}

func (p ProductType) Valid() bool {
	return validProducts[p]
}

// Step represents a step in the onboarding flow.
type Step string

const (
	StepTNC          Step = "TNC"
	StepOCR          Step = "OCR"
	StepPersonalData Step = "PERSONAL_DATA"
	StepOTPVerify    Step = "OTP_VERIFY"
	StepBiometric    Step = "BIOMETRIC"
	StepVideoCall    Step = "VIDEO_CALL"
	StepCredentials  Step = "CREDENTIALS"
	StepReview       Step = "REVIEW"
	StepCompleted    Step = "COMPLETED"
)

// stepOrder defines the allowed progression. A step at index N can only
// transition to the step at index N+1.
var stepOrder = []Step{
	StepTNC,
	StepOCR,
	StepPersonalData,
	StepOTPVerify,
	StepBiometric,
	StepVideoCall,
	StepCredentials,
	StepReview,
	StepCompleted,
}

// CanTransition returns true if moving from `from` to `to` is the next valid step.
func CanTransition(from, to Step) bool {
	for i, s := range stepOrder {
		if s == from && i+1 < len(stepOrder) {
			return stepOrder[i+1] == to
		}
	}
	return false
}

// Session represents an onboarding session.
type Session struct {
	ID             uuid.UUID       `json:"-"`
	SessionID      string          `json:"session_id"`
	DeviceID       string          `json:"-"`
	ProductType    ProductType     `json:"product_type"`
	CurrentStep    Step            `json:"current_step"`
	TNCVersion     string          `json:"-"`
	StepsCompleted StepsCompleted  `json:"steps_completed"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"-"`
	ExpiresAt      time.Time       `json:"expires_at"`
	DeletedAt      *time.Time      `json:"-"`
}

// IsExpired checks whether the session has passed its expiration time.
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// StepsCompleted tracks which onboarding steps are done.
type StepsCompleted struct {
	TNCAccepted       bool `json:"tnc_accepted"`
	OCRVerified       bool `json:"ocr_verified"`
	PersonalDataSaved bool `json:"personal_data_saved"`
	OTPVerified       bool `json:"otp_verified"`
	BiometricVerified bool `json:"biometric_verified"`
	VideoCallVerified bool `json:"video_call_verified"`
	CredentialsSet    bool `json:"credentials_set"`
	Submitted         bool `json:"submitted"`
}

// ProductInfo holds static product details returned to the client.
type ProductInfo struct {
	Type              ProductType `json:"type"`
	Name              string      `json:"name"`
	Currency          string      `json:"currency"`
	MinInitialDeposit int64       `json:"min_initial_deposit"`
	Features          []string    `json:"features"`
}

// ProductCatalog maps product types to their info.
var ProductCatalog = map[ProductType]ProductInfo{
	ProductTahapanBCA: {
		Type:              ProductTahapanBCA,
		Name:              "Tahapan BCA",
		Currency:          "IDR",
		MinInitialDeposit: 500_000,
		Features:          []string{"Paspor BCA Mastercard Debit", "m-BCA", "KlikBCA"},
	},
	ProductTahapanXpre: {
		Type:              ProductTahapanXpre,
		Name:              "Tahapan Xpresi",
		Currency:          "IDR",
		MinInitialDeposit: 50_000,
		Features:          []string{"Kartu Debit Xpresi", "m-BCA"},
	},
	ProductTabunganku: {
		Type:              ProductTabunganku,
		Name:              "TabunganKu",
		Currency:          "IDR",
		MinInitialDeposit: 20_000,
		Features:          []string{"m-BCA"},
	},
}

// CreateSessionRequest is the decoded request body for POST /v1/onboarding/sessions.
type CreateSessionRequest struct {
	ProductType        string `json:"product_type"`
	DeviceID           string `json:"device_id"`
	AcceptedTNCVersion string `json:"accepted_tnc_version"`
}

// CreateSessionResponse is returned on successful session creation.
type CreateSessionResponse struct {
	SessionID   string      `json:"session_id"`
	Product     ProductInfo `json:"product"`
	CurrentStep Step        `json:"current_step"`
	ExpiresAt   time.Time   `json:"expires_at"`
}

// GetSessionResponse is returned on GET /v1/onboarding/sessions/{id}.
type GetSessionResponse struct {
	SessionID      string         `json:"session_id"`
	Product        ProductInfo    `json:"product"`
	CurrentStep    Step           `json:"current_step"`
	StepsCompleted StepsCompleted `json:"steps_completed"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`
}

// --- OCR Types ---

// OCRResult represents the stored OCR extraction for a session.
type OCRResult struct {
	ID             uuid.UUID    `json:"-"`
	OCRID          string       `json:"ocr_id"`
	SessionID      string       `json:"session_id"`
	PhotoPath      string       `json:"-"`
	AccuracyPct    float64      `json:"accuracy_percent"`
	Extracted      KTPData      `json:"extracted"`
	DukcapilMatch  bool         `json:"dukcapil_match"`
	PhotoQuality   PhotoQuality `json:"photo_quality"`
	CreatedAt      time.Time    `json:"created_at"`
	AutoDeleteAt   time.Time    `json:"-"`
}

// KTPData holds all fields extracted from an Indonesian e-KTP.
type KTPData struct {
	NIK              string `json:"nik"`
	NamaLengkap      string `json:"nama_lengkap"`
	TempatLahir      string `json:"tempat_lahir"`
	TanggalLahir     string `json:"tanggal_lahir"`
	JenisKelamin     string `json:"jenis_kelamin"`
	Alamat           string `json:"alamat"`
	RTRW             string `json:"rt_rw"`
	Kelurahan        string `json:"kelurahan"`
	Kecamatan        string `json:"kecamatan"`
	Kota             string `json:"kota"`
	Provinsi         string `json:"provinsi"`
	Agama            string `json:"agama"`
	StatusPerkawinan string `json:"status_perkawinan"`
}

// PhotoQuality holds quality assessment of the KTP photo.
type PhotoQuality struct {
	Sharpness         string `json:"sharpness"`
	GlareDetected     bool   `json:"glare_detected"`
	AllCornersVisible bool   `json:"all_corners_visible"`
}

// DeviceCaptureMeta contains metadata from the mobile camera capture.
type DeviceCaptureMeta struct {
	FlashUsed    bool   `json:"flash_used"`
	AutoCaptured bool   `json:"auto_captured"`
	Resolution   string `json:"resolution"`
}

// OCRResponse is returned on POST /v1/onboarding/ocr.
type OCRResponse struct {
	OCRID         string       `json:"ocr_id"`
	AccuracyPct   float64      `json:"accuracy_percent"`
	Extracted     KTPData      `json:"extracted"`
	DukcapilMatch bool         `json:"dukcapil_match"`
	PhotoQuality  PhotoQuality `json:"photo_quality"`
	CurrentStep   Step         `json:"current_step"`
}

// AuditEventType is the type of event recorded in the audit log.
type AuditEventType string

const (
	AuditSessionCreated       AuditEventType = "SESSION_CREATED"
	AuditSessionCancelled     AuditEventType = "SESSION_CANCELLED"
	AuditSessionExpired       AuditEventType = "SESSION_EXPIRED"
	AuditStepTransition       AuditEventType = "STEP_TRANSITION"
	AuditOCRUploaded          AuditEventType = "OCR_UPLOADED"
	AuditOCRVerified          AuditEventType = "OCR_VERIFIED"
	AuditPersonalDataSaved    AuditEventType = "PERSONAL_DATA_SAVED"
	AuditOTPSent              AuditEventType = "OTP_SENT"
	AuditOTPVerified          AuditEventType = "OTP_VERIFIED"
	AuditOTPFailed            AuditEventType = "OTP_FAILED"
	AuditBiometricUploaded    AuditEventType = "BIOMETRIC_UPLOADED"
	AuditBiometricVerified    AuditEventType = "BIOMETRIC_VERIFIED"
	AuditBiometricFailed      AuditEventType = "BIOMETRIC_FAILED"
	AuditVideoCallQueued      AuditEventType = "VIDEO_CALL_QUEUED"
	AuditVideoCallStarted     AuditEventType = "VIDEO_CALL_STARTED"
	AuditVideoCallEnded       AuditEventType = "VIDEO_CALL_ENDED"
	AuditCredentialsSet       AuditEventType = "CREDENTIALS_SET"
	AuditSubmitted            AuditEventType = "SUBMITTED"
	AuditAccountCreated       AuditEventType = "ACCOUNT_CREATED"
)

// AuditLog represents an onboarding audit entry.
type AuditLog struct {
	ID        uuid.UUID      `json:"-"`
	SessionID string         `json:"session_id"`
	EventType AuditEventType `json:"event_type"`
	Actor     string         `json:"actor"`
	Details   map[string]any `json:"details,omitempty"`
	IPAddress string         `json:"-"`
	UserAgent string         `json:"-"`
	CreatedAt time.Time      `json:"created_at"`
}

// AuditLogResponse is a single audit entry in the API response.
type AuditLogResponse struct {
	EventType string         `json:"event_type"`
	Actor     string         `json:"actor"`
	Details   map[string]any `json:"details,omitempty"`
	IPAddress string         `json:"ip_address,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// GetAuditTrailResponse is returned by GET /v1/onboarding/sessions/{id}/audit.
type GetAuditTrailResponse struct {
	SessionID string             `json:"session_id"`
	Events    []AuditLogResponse `json:"events"`
	Count     int                `json:"count"`
}

// --- Monitoring Types ---

// MonitoringAlert represents an alert rule evaluation result.
type MonitoringAlert struct {
	Rule      string `json:"rule"`
	Severity  string `json:"severity"` // "WARNING", "CRITICAL"
	Message   string `json:"message"`
	Triggered bool   `json:"triggered"`
}

// MonitoringStatus holds the current system health metrics.
type MonitoringStatus struct {
	ActiveSessions    int               `json:"active_sessions"`
	StuckSessions     int               `json:"stuck_sessions"`
	QueueLength       int64             `json:"queue_length"`
	Alerts            []MonitoringAlert `json:"alerts"`
}

// --- Personal Data Types ---

// Pekerjaan enums
type Pekerjaan string

const (
	PekerjaanKaryawanSwasta  Pekerjaan = "KARYAWAN_SWASTA"
	PekerjaanPNS             Pekerjaan = "PNS"
	PekerjaanTNIPolri        Pekerjaan = "TNI_POLRI"
	PekerjaanWiraswasta      Pekerjaan = "WIRASWASTA"
	PekerjaanProfesional     Pekerjaan = "PROFESIONAL"
	PekerjaanPelajarMahasiswa Pekerjaan = "PELAJAR_MAHASISWA"
	PekerjaanIbuRumahTangga  Pekerjaan = "IBU_RUMAH_TANGGA"
	PekerjaanLainnya         Pekerjaan = "LAINNYA"
)

var validPekerjaan = map[Pekerjaan]bool{
	PekerjaanKaryawanSwasta: true, PekerjaanPNS: true, PekerjaanTNIPolri: true,
	PekerjaanWiraswasta: true, PekerjaanProfesional: true, PekerjaanPelajarMahasiswa: true,
	PekerjaanIbuRumahTangga: true, PekerjaanLainnya: true,
}

func (p Pekerjaan) Valid() bool { return validPekerjaan[p] }

// Penghasilan enums
type Penghasilan string

const (
	PenghasilanDibawah5Juta Penghasilan = "DIBAWAH_5_JUTA"
	Penghasilan5_10Juta     Penghasilan = "5_10_JUTA"
	Penghasilan10_20Juta    Penghasilan = "10_20_JUTA"
	Penghasilan20_50Juta    Penghasilan = "20_50_JUTA"
	PenghasilanDiatas50Juta Penghasilan = "DIATAS_50_JUTA"
)

var validPenghasilan = map[Penghasilan]bool{
	PenghasilanDibawah5Juta: true, Penghasilan5_10Juta: true, Penghasilan10_20Juta: true,
	Penghasilan20_50Juta: true, PenghasilanDiatas50Juta: true,
}

func (p Penghasilan) Valid() bool { return validPenghasilan[p] }

// SumberDana enums
type SumberDana string

const (
	SumberDanaGaji      SumberDana = "GAJI"
	SumberDanaUsaha     SumberDana = "USAHA"
	SumberDanaInvestasi SumberDana = "INVESTASI"
	SumberDanaWarisan   SumberDana = "WARISAN"
	SumberDanaLainnya   SumberDana = "LAINNYA"
)

var validSumberDana = map[SumberDana]bool{
	SumberDanaGaji: true, SumberDanaUsaha: true, SumberDanaInvestasi: true,
	SumberDanaWarisan: true, SumberDanaLainnya: true,
}

func (s SumberDana) Valid() bool { return validSumberDana[s] }

// AlamatKTP holds address from KTP.
type AlamatKTP struct {
	AlamatLengkap string `json:"alamat_lengkap"`
	RTRW          string `json:"rt_rw"`
	KodePos       string `json:"kode_pos"`
	Kelurahan     string `json:"kelurahan"`
	Kecamatan     string `json:"kecamatan"`
	Kota          string `json:"kota"`
	Provinsi      string `json:"provinsi"`
}

// PersonalData stores the personal data submitted by the customer.
type PersonalData struct {
	ID                  uuid.UUID   `json:"-"`
	PersonalDataID      string      `json:"personal_data_id"`
	SessionID           string      `json:"session_id"`
	NIK                 string      `json:"-"` // encrypted at rest
	NamaLengkap         string      `json:"-"` // encrypted at rest
	TempatLahir         string      `json:"tempat_lahir"`
	TanggalLahir        string      `json:"tanggal_lahir"`
	JenisKelamin        string      `json:"jenis_kelamin"`
	AlamatKTP           AlamatKTP   `json:"alamat_ktp"`
	AlamatDomisiliSama  bool        `json:"alamat_domisili_sama"`
	Pekerjaan           Pekerjaan   `json:"pekerjaan"`
	PenghasilanPerBulan Penghasilan `json:"penghasilan_per_bulan"`
	SumberDanaUtama     SumberDana  `json:"sumber_dana_utama"`
	NomorHP             string      `json:"-"` // encrypted at rest
	Email               string      `json:"-"` // encrypted at rest
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"-"`
}

// PersonalDataInput is the incoming JSON structure from the client.
type PersonalDataInput struct {
	NIK                 string    `json:"nik"`
	NamaLengkap         string    `json:"nama_lengkap"`
	TempatLahir         string    `json:"tempat_lahir"`
	TanggalLahir        string    `json:"tanggal_lahir"`
	JenisKelamin        string    `json:"jenis_kelamin"`
	AlamatKTP           AlamatKTP `json:"alamat_ktp"`
	AlamatDomisiliSama  bool      `json:"alamat_domisili_sama"`
	Pekerjaan           string    `json:"pekerjaan"`
	PenghasilanPerBulan string    `json:"penghasilan_per_bulan"`
	SumberDanaUtama     string    `json:"sumber_dana_utama"`
	NomorHP             string    `json:"nomor_hp"`
	Email               string    `json:"email"`
}

// SavePersonalDataRequest is the decoded body for POST /v1/onboarding/personal-data.
type SavePersonalDataRequest struct {
	SessionID    string            `json:"session_id"`
	OCRID        string            `json:"ocr_id"`
	PersonalData PersonalDataInput `json:"personal_data"`
}

// SavePersonalDataResponse is returned on successful personal data save.
type SavePersonalDataResponse struct {
	PersonalDataID string    `json:"personal_data_id"`
	OTPSentTo      string    `json:"otp_sent_to"`
	OTPExpiresAt   time.Time `json:"otp_expires_at"`
	CurrentStep    Step      `json:"current_step"`
}

// VerifyOTPRequest is the body for POST /v1/onboarding/verify-otp.
type VerifyOTPRequest struct {
	SessionID string `json:"session_id"`
	OTPCode   string `json:"otp_code"`
}

// VerifyOTPResponse is returned on successful OTP verification.
type VerifyOTPResponse struct {
	Verified    bool `json:"verified"`
	CurrentStep Step `json:"current_step"`
}

// ResendOTPRequest is the body for POST /v1/onboarding/resend-otp.
type ResendOTPRequest struct {
	SessionID string `json:"session_id"`
}

// ResendOTPResponse is returned on successful OTP resend.
type ResendOTPResponse struct {
	OTPSentTo    string    `json:"otp_sent_to"`
	OTPExpiresAt time.Time `json:"otp_expires_at"`
}

// --- Biometric Types ---

// LivenessMeta holds metadata from the mobile liveness challenge.
type LivenessMeta struct {
	ChallengeType    string  `json:"challenge_type"`
	CompletedActions int     `json:"completed_actions"`
	PrecisionScore   float64 `json:"precision_score"`
}

// BiometricResult stores the biometric verification outcome.
type BiometricResult struct {
	ID                uuid.UUID `json:"-"`
	BiometricID       string    `json:"biometric_id"`
	SessionID         string    `json:"session_id"`
	FacePhotoPath     string    `json:"-"`
	LivenessVerified  bool      `json:"liveness_verified"`
	LivenessScore     float64   `json:"liveness_score"`
	FaceMatchVerified bool      `json:"face_match_with_ktp"`
	FaceMatchScore    float64   `json:"face_match_score"`
	ISOCompliant      bool      `json:"iso_30107_compliant"`
	SpoofDetected     bool      `json:"-"`
	FrameCount        int       `json:"-"`
	CreatedAt         time.Time `json:"created_at"`
	AutoDeleteAt      time.Time `json:"-"`
}

// BiometricResponse is returned on POST /v1/onboarding/biometric.
type BiometricResponse struct {
	BiometricID      string  `json:"biometric_id"`
	LivenessVerified bool    `json:"liveness_verified"`
	LivenessScore    float64 `json:"liveness_score"`
	FaceMatchWithKTP bool    `json:"face_match_with_ktp"`
	FaceMatchScore   float64 `json:"face_match_score"`
	ISOCompliant     bool    `json:"iso_30107_compliant"`
	CurrentStep      Step    `json:"current_step"`
}

// FaceAnalysisResult is returned by the biometric engine.
type FaceAnalysisResult struct {
	FaceCount        int
	LivenessScore    float64
	FaceMatchScore   float64
	ISOCompliant     bool
	SpoofDetected    bool
	Quality          string // "HIGH", "MEDIUM", "LOW"
}

// --- Video Call Types ---

// VideoCallStatus tracks the lifecycle of a video call entry.
type VideoCallStatus string

const (
	VCStatusQueued    VideoCallStatus = "QUEUED"
	VCStatusActive    VideoCallStatus = "ACTIVE"
	VCStatusCompleted VideoCallStatus = "COMPLETED"
	VCStatusCancelled VideoCallStatus = "CANCELLED"
)

// VideoCallResult is the CS agent's verification decision.
type VideoCallResult string

const (
	VCResultApproved VideoCallResult = "APPROVED"
	VCResultRejected VideoCallResult = "REJECTED"
)

// VideoCall represents a video call session stored in the DB.
type VideoCall struct {
	ID                  uuid.UUID       `json:"-"`
	QueueID             string          `json:"queue_id"`
	SessionID           string          `json:"session_id"`
	QueueNumber         string          `json:"queue_number"`
	Status              VideoCallStatus `json:"status"`
	AgentEmployeeID     string          `json:"agent_employee_id,omitempty"`
	AgentName           string          `json:"agent_name,omitempty"`
	Result              VideoCallResult `json:"result,omitempty"`
	KTPShownLive        bool            `json:"ktp_shown_live,omitempty"`
	IdentityConfirmed   bool            `json:"identity_confirmed,omitempty"`
	Notes               string          `json:"-"`
	CallDurationSeconds int             `json:"call_duration_seconds,omitempty"`
	RecordingID         string          `json:"-"`
	JoinedAt            time.Time       `json:"joined_at"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	EndedAt             *time.Time      `json:"ended_at,omitempty"`
}

// OperatingHours defines when video call is available.
type OperatingHours struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

// JoinQueueRequest is the body for POST /v1/onboarding/video-call/queue.
type JoinQueueRequest struct {
	SessionID string `json:"session_id"`
}

// JoinQueueResponse is returned when a customer joins the queue.
type JoinQueueResponse struct {
	QueueID              string         `json:"queue_id"`
	QueueNumber          string         `json:"queue_number"`
	Position             int64          `json:"position"`
	EstimatedWaitSeconds int64          `json:"estimated_wait_seconds"`
	OperatingHours       OperatingHours `json:"operating_hours"`
	SignalingURL         string         `json:"signaling_url"`
}

// SubmitVideoCallResultRequest is sent by the CS backend after a call.
type SubmitVideoCallResultRequest struct {
	SessionID           string `json:"session_id"`
	QueueID             string `json:"queue_id"`
	AgentEmployeeID     string `json:"agent_employee_id"`
	Result              string `json:"result"`
	KTPShownLive        bool   `json:"ktp_shown_live"`
	IdentityConfirmed   bool   `json:"identity_confirmed"`
	Notes               string `json:"notes"`
	CallDurationSeconds int    `json:"call_duration_seconds"`
	RecordingID         string `json:"recording_id"`
}

// SubmitVideoCallResultResponse is returned after the CS backend submits a result.
type SubmitVideoCallResultResponse struct {
	SessionID   string `json:"session_id"`
	Result      string `json:"result"`
	CurrentStep Step   `json:"current_step"`
}

// SignalMessage is the JSON envelope for WebSocket signaling messages.
type SignalMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	QueueID   string `json:"queue_id,omitempty"`

	// WebRTC fields
	SDP       string `json:"sdp,omitempty"`
	Candidate any    `json:"candidate,omitempty"`

	// Queue update fields
	Position             int64  `json:"position,omitempty"`
	EstimatedWaitSeconds int64  `json:"estimated_wait_seconds,omitempty"`

	// Agent fields
	Agent *AgentInfo `json:"agent,omitempty"`

	// Instruction
	Text string `json:"text,omitempty"`

	// Call ended
	Result          string `json:"result,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`

	// Media control
	Action string `json:"action,omitempty"`
}

// AgentInfo identifies a CS agent.
type AgentInfo struct {
	Name       string `json:"name"`
	EmployeeID string `json:"employee_id"`
	PhotoURL   string `json:"photo_url,omitempty"`
}

// --- Credential Types ---

// Credential stores hashed access code and PIN for onboarding.
type Credential struct {
	ID              uuid.UUID `json:"-"`
	CredentialID    string    `json:"credential_id"`
	SessionID       string    `json:"session_id"`
	AccessCodeHash  string    `json:"-"`
	PINHash         string    `json:"-"`
	EncryptionKeyID string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
}

// SetCredentialsRequest is the body for POST /v1/onboarding/credentials.
type SetCredentialsRequest struct {
	SessionID            string `json:"session_id"`
	AccessCodeEncrypted  string `json:"access_code_encrypted"`
	PINEncrypted         string `json:"pin_encrypted"`
	EncryptionKeyID      string `json:"encryption_key_id"`
}

// SetCredentialsResponse is returned on successful credential storage.
type SetCredentialsResponse struct {
	CredentialID           string `json:"credential_id"`
	BiometricLoginAvailable bool  `json:"biometric_login_available"`
	CurrentStep            Step   `json:"current_step"`
}

// --- Submit & Account Creation Types ---

// SubmitRequest is the body for POST /v1/onboarding/submit.
type SubmitRequest struct {
	SessionID        string `json:"session_id"`
	AgreementAccepted bool  `json:"agreement_accepted"`
	AgreementVersion string `json:"agreement_version"`
}

// SubmitResponse is returned on successful account creation.
type SubmitResponse struct {
	Account   AccountInfo `json:"account"`
	MBCA      MBCAInfo    `json:"m_bca"`
	CreatedAt time.Time   `json:"created_at"`
}

// AccountInfo holds the newly created account details.
type AccountInfo struct {
	AccountNumber         string    `json:"account_number"`
	AccountType           string    `json:"account_type"`
	AccountHolder         string    `json:"account_holder"`
	Branch                string    `json:"branch"`
	BranchCode            string    `json:"branch_code"`
	Currency              string    `json:"currency"`
	Status                string    `json:"status"`
	MinInitialDeposit     int64     `json:"min_initial_deposit"`
	InitialDepositDeadline time.Time `json:"initial_deposit_deadline"`
}

// MBCAInfo holds the m-BCA user details.
type MBCAInfo struct {
	UserID        string `json:"user_id"`
	AccessCodeSet bool   `json:"access_code_set"`
	PINSet        bool   `json:"pin_set"`
}

// CoreBankingResult is returned by the CoreBankingClient.
type CoreBankingResult struct {
	AccountNumber string
	Branch        string
	BranchCode    string
}
