package onboarding

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
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
	StepTNC Step = "TNC"
	// StepCardSelection disisipkan tepat setelah TNC (§6 docs/08). Sesi hanya
	// berhenti di sini ketika sisipan pilih kartu memang menyala DAN nasabah
	// belum memilih kartu; selain itu sesi berangkat dari OCR persis seperti
	// sebelum sisipan ini ada. Lihat SessionService.CreateSession.
	StepCardSelection Step = "CARD_SELECTION"
	StepOCR           Step = "OCR"
	StepPersonalData  Step = "PERSONAL_DATA"
	StepOTPVerify     Step = "OTP_VERIFY"
	StepBiometric     Step = "BIOMETRIC"
	StepVideoCall     Step = "VIDEO_CALL"
	StepCredentials   Step = "CREDENTIALS"
	StepReview        Step = "REVIEW"
	StepCompleted     Step = "COMPLETED"
)

// stepOrder defines the allowed progression.
//
// CARD_SELECTION disisipkan tepat setelah TNC (§6 docs/08). Langkah lain tidak
// bergeser namanya maupun artinya.
var stepOrder = []Step{
	StepTNC,
	StepCardSelection,
	StepOCR,
	StepPersonalData,
	StepOTPVerify,
	StepBiometric,
	StepVideoCall,
	StepCredentials,
	StepReview,
	StepCompleted,
}

// skippableSteps adalah langkah yang boleh dilompati ketika datanya sudah
// diberikan lebih awal.
//
// CARD_SELECTION masuk ke sini karena §7 membolehkan client mengirim card_type
// langsung saat membuat sesi; sesi itu berangkat dari OCR dan tidak pernah
// singgah di CARD_SELECTION. Tanpa pengecualian ini, penyisipan langkah baru
// akan membuat TNC→OCR mendadak tidak sah dan memutus client yang sudah ada.
var skippableSteps = map[Step]bool{
	StepCardSelection: true,
}

func stepIndex(s Step) int {
	for i, candidate := range stepOrder {
		if candidate == s {
			return i
		}
	}
	return -1
}

// CanTransition reports whether moving from `from` to `to` is allowed.
//
// Maju satu langkah selalu boleh. Maju lebih dari satu hanya boleh bila SEMUA
// langkah yang dilewati memang boleh dilompati — jadi melewati OCR atau
// biometrik tetap ditolak, sementara melewati CARD_SELECTION tidak.
func CanTransition(from, to Step) bool {
	fromIdx, toIdx := stepIndex(from), stepIndex(to)
	if fromIdx < 0 || toIdx < 0 || toIdx <= fromIdx {
		return false
	}
	for i := fromIdx + 1; i < toIdx; i++ {
		if !skippableSteps[stepOrder[i]] {
			return false
		}
	}
	return true
}

// Session represents an onboarding session.
type Session struct {
	ID             uuid.UUID      `json:"-"`
	SessionID      string         `json:"session_id"`
	DeviceID       string         `json:"-"`
	ProductType    ProductType    `json:"product_type"`
	CurrentStep    Step           `json:"current_step"`
	TNCVersion     string         `json:"-"`
	StepsCompleted StepsCompleted `json:"steps_completed"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"-"`
	ExpiresAt      time.Time      `json:"expires_at"`
	DeletedAt      *time.Time     `json:"-"`

	// Pilihan kartu Paspor (§7, §8). Semua nullable: sesi yang dibuat client
	// lama tanpa card_type tetap sah (aturan wajib #7).
	CardType           string     `json:"-"`
	CardSelectedAt     *time.Time `json:"-"`
	CardCatalogVersion string     `json:"-"`
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

	// CardSelected menandai langkah CARD_SELECTION sudah lewat. Sesi lama
	// tidak punya kunci ini di JSONB-nya dan terbaca false — benar, karena
	// mereka memang dibuat sebelum sisipan ini ada. Submit mewajibkannya hanya
	// ketika sisipan menyala (§10), supaya sesi lama tidak ikut tertolak.
	CardSelected bool `json:"card_selected"`
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

	// CardType dan CardCatalogVersion keduanya opsional (§7). Kosong berarti
	// nasabah belum memilih kartu, dan sesi berangkat dari CARD_SELECTION.
	CardType           string `json:"card_type,omitempty"`
	CardCatalogVersion string `json:"card_catalog_version,omitempty"`

	// RegionCode ikut menentukan ketersediaan kartu. Tidak dikirim client
	// sebagai field body; diisi handler dari query yang sama dengan katalog.
	RegionCode string `json:"-"`

	// AppVersion dibaca dari header X-App-Version untuk fallback client lama.
	AppVersion string `json:"-"`
}

// CreateSessionResponse is returned on successful session creation.
type CreateSessionResponse struct {
	SessionID   string      `json:"session_id"`
	Product     ProductInfo `json:"product"`
	CurrentStep Step        `json:"current_step"`
	ExpiresAt   time.Time   `json:"expires_at"`

	// Card hanya hadir bila kartu sudah terpilih. Sesi yang berangkat dari
	// CARD_SELECTION tidak membawanya.
	Card *SessionCard `json:"card,omitempty"`

	// CatalogOutdated menandai client mengirim card_catalog_version yang bukan
	// versi terkini. Diteruskan handler ke meta.catalog_outdated (§7).
	CatalogOutdated bool `json:"-"`
}

// GetSessionResponse is returned on GET /v1/onboarding/sessions/{id}.
type GetSessionResponse struct {
	SessionID      string         `json:"session_id"`
	Product        ProductInfo    `json:"product"`
	CurrentStep    Step           `json:"current_step"`
	StepsCompleted StepsCompleted `json:"steps_completed"`
	CreatedAt      time.Time      `json:"created_at"`
	ExpiresAt      time.Time      `json:"expires_at"`

	// Card null bila sesi berhenti sebelum kartu dipilih (§9). Dikirim eksplisit
	// sebagai null, bukan dihilangkan: client memakai kehadiran field ini untuk
	// membedakan "belum memilih" dari "server belum mengenal kartu".
	Card *SessionCard `json:"card"`
}

// --- OCR Types ---

// OCRResult represents the stored OCR extraction for a session.
type OCRResult struct {
	ID            uuid.UUID    `json:"-"`
	OCRID         string       `json:"ocr_id"`
	SessionID     string       `json:"session_id"`
	PhotoPath     string       `json:"-"`
	AccuracyPct   float64      `json:"accuracy_percent"`
	Extracted     KTPData      `json:"extracted"`
	DukcapilMatch bool         `json:"dukcapil_match"`
	PhotoQuality  PhotoQuality `json:"photo_quality"`
	CreatedAt     time.Time    `json:"created_at"`
	AutoDeleteAt  time.Time    `json:"-"`
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
//
// The three *Score / *Reported fields carry what the capture SDK on the device
// already measured. They are pointers so "the client said nothing" stays
// distinguishable from "the client said zero" — assessPhotoQuality treats a
// missing signal as unknown rather than as a pass.
type DeviceCaptureMeta struct {
	FlashUsed    bool   `json:"flash_used"`
	AutoCaptured bool   `json:"auto_captured"`
	Resolution   string `json:"resolution"`

	SharpnessScore  *float64 `json:"sharpness_score,omitempty"`  // 0-100, higher is sharper
	GlareScore      *float64 `json:"glare_score,omitempty"`      // 0-100, higher means more glare
	CornersDetected *int     `json:"corners_detected,omitempty"` // how many of the 4 KTP corners were found
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
	AuditSessionCreated    AuditEventType = "SESSION_CREATED"
	AuditSessionCancelled  AuditEventType = "SESSION_CANCELLED"
	AuditSessionExpired    AuditEventType = "SESSION_EXPIRED"
	AuditStepTransition    AuditEventType = "STEP_TRANSITION"
	AuditOCRUploaded       AuditEventType = "OCR_UPLOADED"
	AuditOCRVerified       AuditEventType = "OCR_VERIFIED"
	AuditPersonalDataSaved AuditEventType = "PERSONAL_DATA_SAVED"
	AuditOTPSent           AuditEventType = "OTP_SENT"
	AuditOTPVerified       AuditEventType = "OTP_VERIFIED"
	AuditOTPFailed         AuditEventType = "OTP_FAILED"
	AuditBiometricUploaded AuditEventType = "BIOMETRIC_UPLOADED"
	AuditBiometricVerified AuditEventType = "BIOMETRIC_VERIFIED"
	AuditBiometricFailed   AuditEventType = "BIOMETRIC_FAILED"
	AuditVideoCallQueued   AuditEventType = "VIDEO_CALL_QUEUED"
	AuditVideoCallStarted  AuditEventType = "VIDEO_CALL_STARTED"
	AuditVideoCallEnded    AuditEventType = "VIDEO_CALL_ENDED"
	AuditCredentialsSet    AuditEventType = "CREDENTIALS_SET"
	AuditSubmitted         AuditEventType = "SUBMITTED"
	AuditAccountCreated    AuditEventType = "ACCOUNT_CREATED"

	// AuditCardSelected mencatat pemilihan maupun penggantian kartu Paspor.
	// Terpisah dari onboarding_card_selection_log: tabel itu menyimpan biaya
	// yang dilihat nasabah untuk keperluan sengketa, sementara baris ini
	// membuat pilihan kartu ikut terlihat di lini masa audit sesi.
	AuditCardSelected AuditEventType = "CARD_SELECTED"
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
	ActiveSessions int               `json:"active_sessions"`
	StuckSessions  int               `json:"stuck_sessions"`
	QueueLength    int64             `json:"queue_length"`
	Alerts         []MonitoringAlert `json:"alerts"`
}

// --- Personal Data Types ---

// Pekerjaan enums
type Pekerjaan string

const (
	PekerjaanKaryawanSwasta   Pekerjaan = "KARYAWAN_SWASTA"
	PekerjaanPNS              Pekerjaan = "PNS"
	PekerjaanTNIPolri         Pekerjaan = "TNI_POLRI"
	PekerjaanWiraswasta       Pekerjaan = "WIRASWASTA"
	PekerjaanProfesional      Pekerjaan = "PROFESIONAL"
	PekerjaanPelajarMahasiswa Pekerjaan = "PELAJAR_MAHASISWA"
	PekerjaanIbuRumahTangga   Pekerjaan = "IBU_RUMAH_TANGGA"
	PekerjaanLainnya          Pekerjaan = "LAINNYA"
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
	// OTPDebug carries the code itself ONLY when APP_ENV=development, where
	// the SMS gateway just logs. Without it an Android build cannot finish
	// onboarding against a local server without someone reading the log.
	// Never populated in any other environment.
	OTPDebug string `json:"otp_debug,omitempty"`
}

// VerifyOTPRequest is the body for POST /v1/onboarding/verify-otp.
type VerifyOTPRequest struct {
	SessionID string `json:"session_id"`
	OTPCode   string `json:"otp_code"`
	// DeviceID comes from the X-Device-ID header, not the body — a device
	// binding a caller can set in the JSON binds nothing.
	DeviceID string `json:"-"`
}

// VerifyOTPResponse is returned on successful OTP verification.
type VerifyOTPResponse struct {
	Verified    bool `json:"verified"`
	CurrentStep Step `json:"current_step"`
}

// ResendOTPRequest is the body for POST /v1/onboarding/resend-otp.
type ResendOTPRequest struct {
	SessionID string `json:"session_id"`
	// DeviceID comes from the X-Device-ID header. See VerifyOTPRequest.
	DeviceID string `json:"-"`
}

// ResendOTPResponse is returned on successful OTP resend.
type ResendOTPResponse struct {
	OTPSentTo    string    `json:"otp_sent_to"`
	OTPExpiresAt time.Time `json:"otp_expires_at"`
	// OTPDebug: development only, see SavePersonalDataResponse.
	OTPDebug string `json:"otp_debug,omitempty"`
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
	FaceCount      int
	LivenessScore  float64
	FaceMatchScore float64
	ISOCompliant   bool
	SpoofDetected  bool
	Quality        string // "HIGH", "MEDIUM", "LOW"
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

// Signaling roles. These are the only two sides of a video call, and the role
// is carried inside the signaling token — never taken from a query parameter.
const (
	RoleNasabah = "nasabah"
	RoleAgent   = "agent"
)

// ValidSignalingRole reports whether r is a role this service issues tokens for.
func ValidSignalingRole(r string) bool {
	return r == RoleNasabah || r == RoleAgent
}

// AgentSignalingResponse is returned to the CS backend when an agent picks up
// a queued call.
type AgentSignalingResponse struct {
	QueueID      string    `json:"queue_id"`
	SessionID    string    `json:"session_id"`
	QueueNumber  string    `json:"queue_number"`
	SignalingURL string    `json:"signaling_url"`
	ExpiresAt    time.Time `json:"expires_at"`
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
	Position             int64 `json:"position,omitempty"`
	EstimatedWaitSeconds int64 `json:"estimated_wait_seconds,omitempty"`

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
	SessionID           string `json:"session_id"`
	AccessCodeEncrypted string `json:"access_code_encrypted"`
	PINEncrypted        string `json:"pin_encrypted"`
	EncryptionKeyID     string `json:"encryption_key_id"`
}

// SetCredentialsResponse is returned on successful credential storage.
type SetCredentialsResponse struct {
	CredentialID            string `json:"credential_id"`
	BiometricLoginAvailable bool   `json:"biometric_login_available"`
	CurrentStep             Step   `json:"current_step"`
}

// --- Submit & Account Creation Types ---

// SubmitRequest is the body for POST /v1/onboarding/submit.
type SubmitRequest struct {
	SessionID         string `json:"session_id"`
	AgreementAccepted bool   `json:"agreement_accepted"`
	AgreementVersion  string `json:"agreement_version"`
}

// SubmitResponse is returned on successful account creation.
type SubmitResponse struct {
	Account   AccountInfo `json:"account"`
	MBCA      MBCAInfo    `json:"m_bca"`
	CreatedAt time.Time   `json:"created_at"`

	// Card null bila sesi tidak memilih kartu — sesi lama, atau sisipan pilih
	// kartu sedang mati. Penerbitan yang gagal TIDAK membuat field ini null:
	// kartunya tetap dilaporkan REQUESTED dan kegagalannya masuk antrean retry,
	// karena rekeningnya sendiri sudah jadi (§10).
	Card *SubmitCard `json:"card"`
}

// AccountInfo holds the newly created account details.
type AccountInfo struct {
	AccountNumber          string    `json:"account_number"`
	AccountType            string    `json:"account_type"`
	AccountHolder          string    `json:"account_holder"`
	Branch                 string    `json:"branch"`
	BranchCode             string    `json:"branch_code"`
	Currency               string    `json:"currency"`
	Status                 string    `json:"status"`
	MinInitialDeposit      int64     `json:"min_initial_deposit"`
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

// IdempotencyClaim is the outcome of claiming a submit slot.
type IdempotencyClaim struct {
	AlreadyClaimed  bool
	StillProcessing bool
	StoredResponse  string
}

// ProvisionParams carries everything needed to turn a finished onboarding
// session into a real m-BCA user with a usable account.
type ProvisionParams struct {
	SessionID      string
	DeviceID       string
	AccountNumber  string
	ProductType    ProductType
	FullName       string
	NIK            string
	PhoneNumber    string
	Email          string
	AccessCodeHash string
	PINHash        string
}

// ProvisionResult identifies the rows created by ProvisionAccount.
type ProvisionResult struct {
	UserID        string
	AccountID     string
	AccountNumber string
}

// --- Pilih Jenis Kartu Paspor ---
//
// Kontrak: docs/08-PILIH-KARTU-API-SPEC.md §4 dan §6.
//
// Tidak ada satu pun field visual di sini. Client memetakan Style ke design
// token-nya sendiri; hex warna dan URL gambar dilarang meninggalkan server
// (aturan wajib #1 skill buka-rekening-kartu). Seluruh nominal adalah integer
// rupiah penuh, bukan string terformat.

// CardStyle adalah petunjuk visual yang dikenal client. Hanya tiga nilai ini.
type CardStyle string

const (
	CardStyleBlue     CardStyle = "BLUE"
	CardStyleGold     CardStyle = "GOLD"
	CardStylePlatinum CardStyle = "PLATINUM"
)

// CardAvailabilityStatus menentukan apakah kartu bisa dipilih.
type CardAvailabilityStatus string

const (
	CardAvailable   CardAvailabilityStatus = "AVAILABLE"
	CardOutOfStock  CardAvailabilityStatus = "OUT_OF_STOCK"
	CardDisabled    CardAvailabilityStatus = "DISABLED"
	CardNotEligible CardAvailabilityStatus = "NOT_ELIGIBLE"
)

// Selectable melaporkan apakah kartu boleh dipilih nasabah.
//
// Kartu yang tidak tersedia tetap DITAMPILKAN, hanya tidak bisa dipilih —
// menyembunyikannya membuat nasabah bertanya ke call center kenapa Platinum
// hilang (lihat references/verification.md).
func (s CardAvailabilityStatus) Selectable() bool { return s == CardAvailable }

// CardFees adalah biaya-biaya kartu, dalam rupiah penuh.
type CardFees struct {
	MonthlyAdmin    int64 `json:"monthly_admin"`
	CardIssuance    int64 `json:"card_issuance"`
	CardReplacement int64 `json:"card_replacement"`
}

// CardLimits adalah keempat limit kartu debit, dalam rupiah penuh.
type CardLimits struct {
	CashWithdrawal    int64 `json:"cash_withdrawal"`
	TransferBCA       int64 `json:"transfer_bca"`
	TransferInterbank int64 `json:"transfer_interbank"`
	DebitPurchase     int64 `json:"debit_purchase"`
}

// CardAvailability menjelaskan status kartu beserta alasannya.
// ReasonKey wajib terisi bila status bukan AVAILABLE — client menampilkan
// alasan itu, dan tanpanya nasabah hanya melihat kartu mati tanpa penjelasan.
type CardAvailability struct {
	Status    CardAvailabilityStatus `json:"status"`
	ReasonKey *string                `json:"reason_key"`
}

// CardDelivery adalah informasi pengiriman kartu fisik.
type CardDelivery struct {
	PhysicalCardAvailable bool `json:"physical_card_available"`
	EstimatedDaysMin      *int `json:"estimated_days_min"`
	EstimatedDaysMax      *int `json:"estimated_days_max"`
	BranchPickupAvailable bool `json:"branch_pickup_available"`
}

// CardEligibility adalah syarat minimum untuk mengambil kartu.
type CardEligibility struct {
	MinAge            int   `json:"min_age"`
	MinInitialDeposit int64 `json:"min_initial_deposit"`
}

// CardOption adalah satu kartu sebagaimana ditawarkan untuk satu produk.
type CardOption struct {
	CardType     string           `json:"card_type"`
	Name         string           `json:"name"`
	Network      string           `json:"network"`
	TierKey      string           `json:"tier_key"`
	Style        CardStyle        `json:"style"`
	BadgeKey     *string          `json:"badge_key"`
	IsPopular    bool             `json:"is_popular"`
	DisplayOrder int              `json:"display_order"`
	Fees         CardFees         `json:"fees"`
	Limits       CardLimits       `json:"limits"`
	Availability CardAvailability `json:"availability"`
	Delivery     CardDelivery     `json:"delivery"`
	Eligibility  CardEligibility  `json:"eligibility"`

	// IsDefault tidak ikut ke payload: client membaca Catalog.DefaultCardType,
	// yang sudah diturunkan server ke kartu tersedia pertama bila default
	// aslinya kebetulan habis.
	IsDefault bool `json:"-"`

	// Currency disimpan per kartu di database, tapi dikirim sekali di tingkat
	// katalog (§4). Tidak ikut ke payload kartu.
	Currency string `json:"-"`
}

// CardCatalog adalah respons GET /v1/onboarding/products/{type}/cards.
type CardCatalog struct {
	CatalogVersion  string       `json:"catalog_version"`
	ProductType     ProductType  `json:"product_type"`
	DefaultCardType string       `json:"default_card_type"`
	Currency        string       `json:"currency"`
	Cards           []CardOption `json:"cards"`
}

// SessionCard adalah objek `card` yang menyertai respons sesi (§7, §8).
type SessionCard struct {
	CardType   string     `json:"card_type"`
	Name       string     `json:"name"`
	Style      CardStyle  `json:"style"`
	Fees       CardFees   `json:"fees"`
	Limits     CardLimits `json:"limits"`
	SelectedAt *time.Time `json:"selected_at,omitempty"`
}

// SetCardRequest adalah body PUT /v1/onboarding/sessions/{session_id}/card.
type SetCardRequest struct {
	SessionID string `json:"-"`
	CardType  string `json:"card_type"`

	// CardCatalogVersion adalah versi katalog yang DILIHAT nasabah saat menekan
	// pilih. Dicatat di jejak audit; tidak dipakai untuk menolak permintaan.
	CardCatalogVersion string `json:"card_catalog_version,omitempty"`

	// RegionCode dan AppVersion tidak datang dari body; diisi handler dari
	// query dan header, sama seperti pada CreateSessionRequest.
	RegionCode string `json:"-"`
}

// SetCardResponse dikembalikan setelah kartu tersimpan pada sesi.
type SetCardResponse struct {
	Card        SessionCard    `json:"card"`
	CurrentStep Step           `json:"current_step"`
	Steps       StepsCompleted `json:"steps_completed"`
}

// SessionCardUpdate adalah satu penulisan pilihan kartu ke baris sesi.
//
// Kartu, langkah, dan steps_completed ditulis bersama dalam satu UPDATE: ketiga
// nilai itu harus sepakat, dan menulisnya terpisah membuka jendela di mana sesi
// sudah maju ke OCR tapi kartunya belum tersimpan.
type SessionCardUpdate struct {
	CardType       string
	CatalogVersion string
	SelectedAt     time.Time
	CurrentStep    Step
	StepsCompleted StepsCompleted
}

// --- Penerbitan kartu (§10) ---

// CardIssuanceStatus adalah status permintaan cetak kartu.
type CardIssuanceStatus string

const (
	CardIssuanceRequested CardIssuanceStatus = "REQUESTED"
	CardIssuancePrinting  CardIssuanceStatus = "PRINTING"
	CardIssuanceShipped   CardIssuanceStatus = "SHIPPED"

	// CardIssuanceFailed tidak pernah dikirim ke client. Nasabah melihat
	// REQUESTED selama permintaan masih diulang; status ini hanya untuk
	// operator yang membaca antrean retry.
	CardIssuanceFailed CardIssuanceStatus = "FAILED"
)

// CardDeliveryMethod adalah cara kartu sampai ke nasabah.
type CardDeliveryMethod string

const (
	CardDeliveryCourier      CardDeliveryMethod = "COURIER"
	CardDeliveryBranchPickup CardDeliveryMethod = "BRANCH_PICKUP"
)

// CardIssuanceRequest adalah permintaan cetak yang dikirim ke core banking.
//
// CoreBankingCode, bukan CardType, yang benar-benar dikirim: kode kartu milik
// core banking belum tentu sama dengan enum di dokumen ini (§10).
type CardIssuanceRequest struct {
	SessionID       string
	AccountNumber   string
	CardType        string
	CoreBankingCode string
	HolderName      string
}

// CardIssuanceResult adalah balasan core banking atas permintaan cetak.
type CardIssuanceResult struct {
	MaskedNumber   string
	Status         CardIssuanceStatus
	TrackingNumber *string
}

// CardIssuance adalah baris antrean permintaan cetak kartu.
type CardIssuance struct {
	SessionID       string
	AccountNumber   string
	CardType        string
	CoreBankingCode string
	Status          CardIssuanceStatus
	MaskedNumber    *string
	DeliveryMethod  CardDeliveryMethod
	EstimatedFrom   *time.Time
	EstimatedTo     *time.Time
	TrackingNumber  *string
	Attempts        int
	LastError       *string
	NextRetryAt     *time.Time
}

// SubmitCard adalah objek `card` pada respons submit (§10).
type SubmitCard struct {
	CardType     string             `json:"card_type"`
	Name         string             `json:"name"`
	MaskedNumber *string            `json:"masked_number"`
	Status       CardIssuanceStatus `json:"status"`
	Delivery     SubmitCardDelivery `json:"delivery"`
}

// SubmitCardDelivery adalah estimasi pengiriman kartu fisik.
//
// Tanggalnya string YYYY-MM-DD, bukan timestamp: yang dijanjikan ke nasabah
// adalah HARI, dan mengirim jam beserta zona waktunya hanya mengundang client
// menampilkan "25 September 07:00" untuk sesuatu yang tidak sepresisi itu.
type SubmitCardDelivery struct {
	Method               CardDeliveryMethod `json:"method"`
	EstimatedArrivalFrom *string            `json:"estimated_arrival_from"`
	EstimatedArrivalTo   *string            `json:"estimated_arrival_to"`
	TrackingNumber       *string            `json:"tracking_number"`
}

// CardSelectionLogEntry adalah satu baris jejak audit pemilihan kartu.
//
// MonthlyAdminFeeShown sengaja disalin dari katalog versi yang dilihat nasabah,
// bukan di-join saat query: yang perlu dibuktikan saat sengketa adalah biaya
// yang DILIHAT nasabah saat itu, bukan biaya hari ini.
type CardSelectionLogEntry struct {
	SessionID            string
	FromCardType         *string
	ToCardType           string
	CatalogVersion       string
	MonthlyAdminFeeShown int64
	Actor                string
	IPAddress            string

	// ProductType TIDAK disimpan ke onboarding_card_selection_log — kolomnya
	// tidak ada di sana, dan produk sesi sudah bisa di-join lewat session_id.
	// Field ini hanya dipakai sebagai label metrik dan isi log terstruktur.
	ProductType string
}

// --- Admin katalog kartu (§3, §13) ---

// Nilai-nilai yang dikenal client. Admin API menolak apa pun di luar daftar ini
// dengan menyebut nilai yang sah: katalog yang memuat badge atau style yang
// tidak dikenal akan tampil sebagai kartu tanpa label di layar nasabah, dan
// kesalahan itu baru ketahuan setelah tayang.
var (
	validCardStyles = []CardStyle{CardStyleBlue, CardStyleGold, CardStylePlatinum}

	validBadgeKeys = []string{
		"RECOMMENDED_BEGINNER",
		"FLEXIBLE_TRANSACTION",
		"MAX_LIMIT",
	}

	validAvailabilityStatuses = []CardAvailabilityStatus{
		CardAvailable, CardOutOfStock, CardDisabled, CardNotEligible,
	}

	validReasonKeys = []string{
		"STOCK_EMPTY_IN_REGION",
		"TEMPORARILY_DISABLED",
		"PRODUCT_MISMATCH",
		"AGE_REQUIREMENT",
	}

	validTierKeys = []string{"DEBIT", "PLATINUM_DEBIT"}
)

// CardProductWrite adalah badan PUT /internal/v1/cards/{card_type}.
//
// Semantik PUT, bukan PATCH: seluruh field yang boleh diubah wajib hadir.
// Pilihan itu sengaja — patch dengan field nullable menuntut pembeda antara
// "tidak dikirim" dan "dikosongkan", dan pembeda itu adalah sumber salah
// tafsir yang tidak sebanding dengan kenyamanannya.
type CardProductWrite struct {
	Name        string          `json:"name"`
	TierKey     string          `json:"tier_key"`
	Style       CardStyle       `json:"style"`
	Fees        CardFees        `json:"fees"`
	Limits      CardLimits      `json:"limits"`
	Delivery    CardDelivery    `json:"delivery"`
	Eligibility CardEligibility `json:"eligibility"`
	IsActive    bool            `json:"is_active"`

	// NewDefaultCardType menyertakan default pengganti ketika penulisan ini
	// akan menonaktifkan kartu yang sedang menjadi default suatu produk.
	// Tanpa itu, penulisannya ditolak (§Prompt 7).
	NewDefaultCardType string `json:"new_default_card_type,omitempty"`
}

// ProductCardWrite adalah badan
// PUT /internal/v1/products/{product_type}/cards/{card_type}.
type ProductCardWrite struct {
	DisplayOrder          int                    `json:"display_order"`
	IsDefault             bool                   `json:"is_default"`
	IsPopular             bool                   `json:"is_popular"`
	BadgeKey              *string                `json:"badge_key"`
	AvailabilityStatus    CardAvailabilityStatus `json:"availability_status"`
	AvailabilityReasonKey *string                `json:"availability_reason_key"`
	RegionCode            *string                `json:"region_code"`

	NewDefaultCardType string `json:"new_default_card_type,omitempty"`
}

// CardCatalogWriteResult dikembalikan setiap penulisan admin.
type CardCatalogWriteResult struct {
	CatalogVersion string         `json:"catalog_version"`
	CardType       string         `json:"card_type"`
	ProductType    *ProductType   `json:"product_type,omitempty"`
	OldValue       map[string]any `json:"old_value"`
	NewValue       map[string]any `json:"new_value"`
}

// AdminCardRow adalah satu baris pada GET /internal/v1/cards.
//
// Berbeda dari CardOption: admin melihat kartu yang tidak aktif juga, dan
// melihat penempatannya per produk beserta wilayahnya.
type AdminCardRow struct {
	CardType    string          `json:"card_type"`
	Name        string          `json:"name"`
	Network     string          `json:"network"`
	TierKey     string          `json:"tier_key"`
	Style       CardStyle       `json:"style"`
	Currency    string          `json:"currency"`
	Fees        CardFees        `json:"fees"`
	Limits      CardLimits      `json:"limits"`
	Delivery    CardDelivery    `json:"delivery"`
	Eligibility CardEligibility `json:"eligibility"`
	IsActive    bool            `json:"is_active"`

	Placements []AdminCardPlacement `json:"placements"`
}

// AdminCardPlacement adalah penempatan satu kartu pada satu produk/wilayah.
type AdminCardPlacement struct {
	ProductType           ProductType            `json:"product_type"`
	RegionCode            *string                `json:"region_code"`
	DisplayOrder          int                    `json:"display_order"`
	IsDefault             bool                   `json:"is_default"`
	IsPopular             bool                   `json:"is_popular"`
	BadgeKey              *string                `json:"badge_key"`
	AvailabilityStatus    CardAvailabilityStatus `json:"availability_status"`
	AvailabilityReasonKey *string                `json:"availability_reason_key"`
}

// CardCatalogAuditEntry adalah satu baris jejak perubahan katalog.
type CardCatalogAuditEntry struct {
	Actor          string
	Action         string
	CardType       string
	ProductType    *string
	OldValue       map[string]any
	NewValue       map[string]any
	CatalogVersion string
	IPAddress      string
}

const (
	AuditActionCardUpdated        = "CARD_UPDATED"
	AuditActionProductCardUpdated = "PRODUCT_CARD_UPDATED"
)

// Validate memeriksa badan PUT kartu terhadap nilai yang dikenal client.
func (w CardProductWrite) Validate() error {
	if strings.TrimSpace(w.Name) == "" {
		return invalidCardField("name", "nama kartu wajib diisi", nil)
	}
	if !containsString(validTierKeys, w.TierKey) {
		return invalidCardField("tier_key", "tier_key tidak dikenal", validTierKeys)
	}
	if !containsStyle(validCardStyles, w.Style) {
		return invalidCardField("style", "style tidak dikenal", styleStrings())
	}
	if err := validateNonNegative(w.Fees, w.Limits, w.Eligibility); err != nil {
		return err
	}
	return validateDeliveryWindow(w.Delivery)
}

// Validate memeriksa badan PUT penempatan kartu pada produk.
func (w ProductCardWrite) Validate() error {
	if w.DisplayOrder < 0 {
		return invalidCardField("display_order", "display_order tidak boleh negatif", nil)
	}
	if !containsStatus(validAvailabilityStatuses, w.AvailabilityStatus) {
		return invalidCardField("availability_status", "availability_status tidak dikenal", statusStrings())
	}
	if w.BadgeKey != nil && !containsString(validBadgeKeys, *w.BadgeKey) {
		return invalidCardField("badge_key", "badge_key tidak dikenal", validBadgeKeys)
	}
	if w.AvailabilityReasonKey != nil && !containsString(validReasonKeys, *w.AvailabilityReasonKey) {
		return invalidCardField("availability_reason_key", "availability_reason_key tidak dikenal", validReasonKeys)
	}

	// Cermin dari CONSTRAINT card_option_reason_required di migrasi 000019.
	// Ditolak di sini supaya pesannya menyebut field-nya, bukan melempar
	// pelanggaran constraint Postgres sebagai 500.
	if w.AvailabilityStatus != CardAvailable &&
		(w.AvailabilityReasonKey == nil || strings.TrimSpace(*w.AvailabilityReasonKey) == "") {
		return invalidCardField("availability_reason_key",
			"status selain AVAILABLE wajib menyebut alasannya", validReasonKeys)
	}

	// Kartu yang tidak bisa dipilih tidak boleh sekaligus menjadi default:
	// layar pilih kartu akan berangkat dengan pilihan awal yang mati.
	if w.IsDefault && w.AvailabilityStatus != CardAvailable {
		return invalidCardField("is_default",
			"kartu yang tidak AVAILABLE tidak bisa dijadikan default", nil)
	}
	return nil
}

func validateNonNegative(fees CardFees, limits CardLimits, elig CardEligibility) error {
	negatives := map[string]int64{
		"fees.monthly_admin":            fees.MonthlyAdmin,
		"fees.card_issuance":            fees.CardIssuance,
		"fees.card_replacement":         fees.CardReplacement,
		"limits.cash_withdrawal":        limits.CashWithdrawal,
		"limits.transfer_bca":           limits.TransferBCA,
		"limits.transfer_interbank":     limits.TransferInterbank,
		"limits.debit_purchase":         limits.DebitPurchase,
		"eligibility.min_initial_depos": elig.MinInitialDeposit,
		"eligibility.min_age":           int64(elig.MinAge),
	}
	// Urutan map tidak tentu, jadi field yang dilaporkan harus deterministik —
	// pesan error yang berubah-ubah membuat test rapuh dan laporan bug bingung.
	keys := make([]string, 0, len(negatives))
	for k := range negatives {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if negatives[k] < 0 {
			return invalidCardField(k, "nilai tidak boleh negatif", nil)
		}
	}
	return nil
}

func validateDeliveryWindow(d CardDelivery) error {
	if d.EstimatedDaysMin == nil || d.EstimatedDaysMax == nil {
		return nil
	}
	if *d.EstimatedDaysMin < 0 || *d.EstimatedDaysMax < 0 {
		return invalidCardField("delivery", "estimasi hari tidak boleh negatif", nil)
	}
	if *d.EstimatedDaysMin > *d.EstimatedDaysMax {
		return invalidCardField("delivery",
			"estimated_days_min tidak boleh lebih besar dari estimated_days_max", nil)
	}
	return nil
}

// invalidCardField membentuk 422 yang MENYEBUTKAN nilai yang sah.
//
// Pesan "nilai tidak valid" memaksa admin menebak atau membuka kode; daftar
// nilai yang sah membuat kesalahannya bisa diperbaiki pada percobaan pertama.
func invalidCardField(field, message string, allowed []string) error {
	details := map[string]any{"field": field}
	if len(allowed) > 0 {
		details["allowed_values"] = allowed
	}
	return apperr.Error{
		Status:  apperr.CardCatalogInvalidValue.Status,
		Code:    apperr.CardCatalogInvalidValue.Code,
		Message: message,
		Details: details,
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsStyle(list []CardStyle, want CardStyle) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsStatus(list []CardAvailabilityStatus, want CardAvailabilityStatus) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func styleStrings() []string {
	out := make([]string, 0, len(validCardStyles))
	for _, s := range validCardStyles {
		out = append(out, string(s))
	}
	return out
}

func statusStrings() []string {
	out := make([]string, 0, len(validAvailabilityStatuses))
	for _, s := range validAvailabilityStatuses {
		out = append(out, string(s))
	}
	return out
}
