package config

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
)

type Config struct {
	App              App
	DB               DB
	RedisSession     Redis
	RedisCache       Redis
	JWT              JWT
	PIN              PIN
	Crypto           Crypto
	Argon2           Argon2
	UploadDir        string `env:"UPLOAD_DIR" envDefault:"uploads"`
	SignalingBaseURL string `env:"SIGNALING_BASE_URL" envDefault:"ws://localhost:8080"`

	// InternalAPIKey guards the CS/monitoring endpoints. There is deliberately
	// no default: a shipped default is a published password, and the previous
	// "dev-internal-key" fallback meant a deployment that forgot the variable
	// exposed the video-call result and audit-trail endpoints to anyone who
	// had read the repository. Validate() rejects an empty or well-known value
	// outside development.
	InternalAPIKey string `env:"INTERNAL_API_KEY"`

	// TrustedProxies lists the CIDRs (or bare IPs) of proxies allowed to set
	// X-Forwarded-For / X-Real-IP. Empty means the headers are ignored and the
	// peer address is used — the safe default for a service exposed directly.
	TrustedProxies []string `env:"TRUSTED_PROXIES" envSeparator:","`

	// CORSAllowedOrigins is empty by default: no browser origin is trusted.
	// Set CORS_ALLOWED_ORIGINS (comma-separated, exact scheme+host+port) only
	// for the web frontends that must reach this API from a browser.
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS" envSeparator:","`

	WebRTC WebRTC

	Push Push

	Liveness Liveness

	SMS SMS

	Client Client

	KTP KTP
}

// KTP holds the e-KTP verification policy for onboarding.
type KTP struct {
	// StorageDriver selects where KTP photos are kept: "local" or "mock".
	//
	// Default "local", which writes real files under StorageLocalPath. "mock"
	// keeps nothing and exists only for tests — with it, the photo appears not to
	// attach and biometric face match has no reference to compare against.
	StorageDriver string `env:"KTP_STORAGE_DRIVER" envDefault:"local"`

	// StorageLocalPath is the root directory for the local driver.
	StorageLocalPath string `env:"KTP_STORAGE_LOCAL_PATH" envDefault:"uploads"`

	// DukcapilMode decides whether NIK is checked against a population registry:
	// "off", "mock" or "real".
	//
	// Default "off". Not "mock", because the mock refuses every NIK outside its
	// four test identities — so a real e-KTP is rejected with
	// OCR_DUKCAPIL_MISMATCH, which reads as if the customer's card were bad. With
	// "off" the card is still fully validated (e-KTP layout, NIK structure, and
	// the NIK cross-checked against the parsed birth date and sex); what is
	// skipped is only the registry lookup, and the response says so via
	// dukcapil_checked=false.
	DukcapilMode string `env:"DUKCAPIL_MODE" envDefault:"off"`
}

// Push holds the Firebase Cloud Messaging credentials.
//
// Empty CredentialsFile is a supported state, not a misconfiguration: without it
// the service still writes every in-app notification row, and the process says
// plainly at boot that nothing is being delivered to handsets. What is NOT
// supported is a credentials file that exists but cannot be used — that fails
// the boot, because a server which looks healthy while silently delivering
// nothing is the failure this transport was added to remove.
// Liveness holds the active face-liveness policy and the Play Integrity identity.
//
// All of it is configuration rather than Go constants because the numbers are
// policy (three failures, five minutes, two rounds per 24 hours, the score
// thresholds), and policy changes without a release.
type Liveness struct {
	// Provider selects the LivenessProvider: "internal" or "stub".
	//
	// "stub" accepts any submission that survives the deterministic checks and is
	// refused outright unless APP_ENV=development — two independent conditions,
	// because one misread variable in production would otherwise turn liveness
	// into a formality.
	Provider string `env:"LIVENESS_PROVIDER" envDefault:"internal"`

	ChallengeTTL time.Duration `env:"LIVENESS_CHALLENGE_TTL" envDefault:"60s"`

	// ActionCount is how many movements one challenge asks for. BLINK is always
	// one of them.
	ActionCount int `env:"LIVENESS_ACTION_COUNT" envDefault:"3"`

	MaxFailures       int           `env:"LIVENESS_MAX_FAILURES" envDefault:"3"`
	CooldownDuration  time.Duration `env:"LIVENESS_COOLDOWN" envDefault:"5m"`
	MaxCooldownRounds int           `env:"LIVENESS_MAX_COOLDOWN_ROUNDS" envDefault:"2"`
	BlockWindow       time.Duration `env:"LIVENESS_BLOCK_WINDOW" envDefault:"24h"`

	LivenessThreshold  float64 `env:"LIVENESS_SCORE_THRESHOLD" envDefault:"90"`
	FaceMatchThreshold float64 `env:"LIVENESS_FACE_MATCH_THRESHOLD" envDefault:"85"`

	// MinFrameDistance is the minimum perceptual-hash Hamming distance between a
	// head-pose frame and the neutral frame. Too low and a still image passes;
	// too high and honest customers are refused.
	MinFrameDistance int `env:"LIVENESS_MIN_FRAME_DISTANCE" envDefault:"6"`

	MaxClockSkew time.Duration `env:"LIVENESS_MAX_CLOCK_SKEW" envDefault:"2m"`

	// IntegrityLogOnly records a failing Play Integrity verdict without refusing
	// the attempt. Validate() rejects it outside development: a production server
	// that only logs a failed device verdict is not enforcing one.
	IntegrityLogOnly bool `env:"LIVENESS_INTEGRITY_LOG_ONLY" envDefault:"false"`

	// PlayIntegrityCredentialsFile is a Google service account JSON with the
	// playintegrity scope. The file must never be committed.
	PlayIntegrityCredentialsFile string `env:"PLAY_INTEGRITY_CREDENTIALS_FILE"`

	// PlayIntegrityPackageName must equal the Android applicationId.
	PlayIntegrityPackageName string `env:"PLAY_INTEGRITY_PACKAGE_NAME" envDefault:"id.bca.bcamobile"`

	// PlayIntegrityCertSHA256 is the base64url SHA-256 of the app signing
	// certificate as Play reports it. Empty skips the digest check, which leaves
	// a repackaged APK claiming the right package name — acceptable in dev only.
	PlayIntegrityCertSHA256 string `env:"PLAY_INTEGRITY_CERT_SHA256"`

	PlayIntegrityTimeout time.Duration `env:"PLAY_INTEGRITY_TIMEOUT" envDefault:"10s"`
}

type Push struct {
	// CredentialsFile is the path to the Google service account JSON, the same
	// file GOOGLE_APPLICATION_CREDENTIALS would point at. The project id is read
	// from inside it; there is deliberately no FCM_PROJECT_ID, because a second
	// variable could disagree with the credential and the symptom would be
	// "messages accepted, nothing delivered".
	//
	// The file itself must never be committed — see .env.example.
	CredentialsFile string `env:"FCM_CREDENTIALS_FILE"`

	// Timeout bounds a single FCM call. The push happens after money has already
	// been committed, so a slow Google is cut off rather than allowed to hold the
	// handler open.
	Timeout time.Duration `env:"FCM_TIMEOUT" envDefault:"10s"`
}

// SMS holds the OTP delivery provider credentials.
//
// Empty Provider is supported only in development, where the gateway logs the
// code instead of sending it. Outside development Validate() rejects it: a
// process without a provider still generates and stores every OTP, so the
// metrics, the audit trail and the step transitions all look healthy while no
// nasabah can finish buka rekening. That has to be a failed boot, not a
// discovery made from a support ticket.
type SMS struct {
	// Provider names the transport. "twilio" is the one implemented; see
	// internal/pkg/sms.NewProvider. An unknown value fails the boot rather
	// than falling back, because a typo here costs every nasabah their OTP.
	Provider string `env:"SMS_PROVIDER"`

	// AccountSID is Twilio's "AC…" account identifier (the basic-auth user).
	AccountSID string `env:"SMS_ACCOUNT_SID"`

	// AuthToken is the account auth token. It is a password: never logged,
	// never echoed into an error, and never committed — keep it in .env.
	AuthToken string `env:"SMS_AUTH_TOKEN"`

	// Sender is a Twilio number in E.164 ("+1555…") or, preferred for
	// Indonesian traffic, a Messaging Service SID ("MG…") that picks the route
	// and sender id per destination operator.
	Sender string `env:"SMS_SENDER"`

	// BaseURL overrides the API host. It exists for tests and for an
	// on-premise aggregator later; leave it unset in every real deployment.
	BaseURL string `env:"SMS_BASE_URL"`

	// Timeout bounds one send. The nasabah is watching a spinner, so a slow
	// aggregator is cut off rather than allowed to hold the handler open.
	Timeout time.Duration `env:"SMS_TIMEOUT" envDefault:"10s"`

	// VerifyServiceSID names a Twilio Verify service ("VA…"), required when
	// Provider is "twilio_verify".
	//
	// Verify owns the code: it generates, stores and checks it, so Sender above
	// is unused on that path. It exists because a Twilio trial account cannot
	// send a custom message body at all — the Messages API answers 572006 — and
	// Verify is the one channel that works there.
	VerifyServiceSID string `env:"SMS_VERIFY_SERVICE_SID"`

	// VerifyBaseURL overrides the Verify host. Deliberately NOT BaseURL above:
	// Verify lives on verify.twilio.com, Messages on api.twilio.com. Verify used
	// to read BaseURL, so setting SMS_BASE_URL pointed verifications at the
	// Messages host, which answers 404 — and a 404 from Verify means "nothing
	// pending for this number", so every nasabah saw OTP_EXPIRED while the
	// credentials were perfectly fine. Tests only; leave unset everywhere else.
	VerifyBaseURL string `env:"SMS_VERIFY_BASE_URL"`

	// VerifyChannels is the allowlist of Verify channels this deployment may
	// use: "sms", "call", or both. Empty means SMS only.
	//
	// It is a deployment setting rather than a request parameter because each
	// channel has its own Geo Permissions checkbox in the Twilio console —
	// Messaging for sms, Voice for call — and a channel the console has not
	// opened fails at the provider no matter who asked for it.
	VerifyChannels []string `env:"SMS_VERIFY_CHANNELS" envSeparator:","`

	// VerifyLocale is the language Verify renders the code in. Empty means "id".
	//
	// Honoured for sms. Verify's voice template does not cover Indonesian, so a
	// call falls back to English and the transport omits the parameter rather
	// than pretending otherwise.
	VerifyLocale string `env:"SMS_VERIFY_LOCALE"`

	// VerifyCodeTTL must match the code expiry set on the Verify service in the
	// console. Nothing here enforces it — it is what the app is told, so a
	// mismatch shows up as a countdown that disagrees with the code the nasabah
	// is holding.
	VerifyCodeTTL time.Duration `env:"SMS_VERIFY_CODE_TTL" envDefault:"10m"`
}

// Client holds the values GET /health/config serves to the mobile app. They
// used to be string literals inside the handler, which meant the force-update
// gate and the maintenance switch could not actually be operated.
type Client struct {
	// MaintenanceMode makes the app show its maintenance screen.
	MaintenanceMode bool `env:"MAINTENANCE_MODE" envDefault:"false"`

	// MinAppVersion is the oldest build allowed to talk to this API.
	MinAppVersion string `env:"MIN_APP_VERSION" envDefault:"1.0.0"`

	// Feature flags the app reads to hide screens it must not offer.
	FeatureBiometricLogin bool `env:"FEATURE_BIOMETRIC_LOGIN" envDefault:"true"`
	FeatureQRISPayment    bool `env:"FEATURE_QRIS_PAYMENT" envDefault:"true"`
	FeatureEWalletTopUp   bool `env:"FEATURE_EWALLET_TOPUP" envDefault:"true"`
	FeatureOnboarding     bool `env:"FEATURE_ONBOARDING" envDefault:"true"`

	// MaxRequestBodyBytes caps any JSON request body. Endpoints that take an
	// upload set their own, larger, limit.
	MaxRequestBodyBytes int64 `env:"MAX_REQUEST_BODY_BYTES" envDefault:"1048576"`

	// RequestTimeout bounds how long a handler may run.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"30s"`

	// CardSelectionEnabled adalah feature flag sisipan pilih kartu
	// (docs/08-PILIH-KARTU-API-SPEC.md §13). Mati berarti katalog menjawab
	// CARD_CATALOG_EMPTY dan flow lama kembali utuh tanpa rollback deployment.
	//
	// Default false: integrasi kartu belum punya angka fee/limit resmi (§17),
	// jadi menyalakannya adalah keputusan sadar, bukan bawaan.
	CardSelectionEnabled bool `env:"FEATURE_CARD_SELECTION" envDefault:"false"`

	// ProductCatalogEnabled adalah feature flag katalog jenis rekening. Mati berarti
	// GET /v1/onboarding/products menjawab 503 ONBOARDING_CATALOG_UNAVAILABLE dan
	// client jatuh ke daftar bawaannya di strings.xml — layar tetap berfungsi penuh,
	// tanpa rollback deployment.
	//
	// Matinya katalog TIDAK BOLEH mematikan POST /sessions: validasi product_type di
	// sana tetap pt.Valid() + cek maintenance, dan tidak pernah menuntut baris
	// onboarding_products ada.
	//
	// Default true, berbeda dari CardSelectionEnabled: isi katalog ini disalin apa
	// adanya dari strings.xml, jadi menyalakannya tidak mengubah satu kata pun yang
	// dilihat nasabah. Yang belum resmi adalah ANGKA setoran awalnya — itu ditandai di
	// komentar migrasi 000039, bukan dengan mematikan endpointnya.
	ProductCatalogEnabled bool `env:"FEATURE_ONBOARDING_PRODUCT_CATALOG" envDefault:"true"`

	// CardLegacyAppVersion adalah ambang X-App-Version untuk fallback client
	// lama (§7 butir 6): build di bawah ambang ini tidak mengenal langkah
	// CARD_SELECTION, jadi sesinya dibuatkan kartu default produk dan langsung
	// lanjut ke OCR alih-alih berhenti di layar yang tidak bisa dirender.
	//
	// Kosong berarti fallback mati total. Itu default yang disengaja: memaksa
	// kartu default — beserta biaya bulanannya — kepada nasabah yang tidak
	// pernah memilihnya adalah keputusan produk, bukan bawaan teknis.
	CardLegacyAppVersion string `env:"ONBOARDING_CARD_LEGACY_APP_VERSION"`

	// CardCoreBankingCodes memetakan card_type ke kode kartu milik core
	// banking (§10). Format: "PASPOR_BLUE:CB-BLUE,PASPOR_GOLD:CB-GOLD".
	//
	// Disimpan di konfigurasi runtime, bukan ditanam di kode: kode ini milik
	// tim core banking dan bisa berubah tanpa rilis aplikasi. card_type yang
	// tidak punya pemetaan ditolak SAAT STARTUP, bukan saat ada nasabah submit
	// — konfigurasi bolong lebih baik ketahuan saat deploy daripada saat
	// rekening sudah jadi tapi kartunya gagal dicetak.
	CardCoreBankingCodes map[string]string `env:"ONBOARDING_CARD_CORE_BANKING_CODES"`

	// ProductsUnderMaintenance menyebut produk yang sedang tidak melayani
	// pembukaan rekening. §4 mendokumentasikan 422
	// ONBOARDING_PRODUCT_UNAVAILABLE untuk keadaan ini tapi tidak menyebut dari
	// mana status maintenance dibaca — tidak ada kolom maupun flag untuk itu di
	// mana pun. Daftar ini membuat error tersebut bisa dioperasikan, bukan
	// sekadar tertulis di dokumen.
	ProductsUnderMaintenance []string `env:"ONBOARDING_PRODUCTS_MAINTENANCE" envSeparator:","`
}

// CardCoreBankingCode mengembalikan kode core banking untuk satu card_type.
// String kosong berarti belum dipetakan.
func (c Client) CardCoreBankingCode(cardType string) string {
	return c.CardCoreBankingCodes[strings.ToUpper(strings.TrimSpace(cardType))]
}

// ProductUnderMaintenance melaporkan apakah satu product_type sedang ditutup.
func (c Client) ProductUnderMaintenance(productType string) bool {
	for _, p := range c.ProductsUnderMaintenance {
		if strings.EqualFold(strings.TrimSpace(p), productType) {
			return true
		}
	}
	return false
}

// IsProduction reports whether this process runs outside development.
func (c *Config) IsProduction() bool {
	env := strings.ToLower(strings.TrimSpace(c.App.Env))
	return env != "" && env != "development" && env != "test"
}

type App struct {
	Env             string        `env:"APP_ENV" envDefault:"development"`
	Host            string        `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	Port            int           `env:"SERVER_PORT" envDefault:"8080"`
	ReadTimeout     time.Duration `env:"SERVER_READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout    time.Duration `env:"SERVER_WRITE_TIMEOUT" envDefault:"15s"`
	IdleTimeout     time.Duration `env:"SERVER_IDLE_TIMEOUT" envDefault:"60s"`
	ShutdownTimeout time.Duration `env:"SERVER_SHUTDOWN_TIMEOUT" envDefault:"10s"`
}

type DB struct {
	Host            string        `env:"DB_HOST" envDefault:"localhost"`
	Port            int           `env:"DB_PORT" envDefault:"5432"`
	User            string        `env:"DB_USER" envDefault:"bcamobile"`
	Password        string        `env:"DB_PASSWORD" envDefault:"localdev_password_123"`
	Name            string        `env:"DB_NAME" envDefault:"bcamobile"`
	SSLMode         string        `env:"DB_SSLMODE" envDefault:"disable"`
	MaxOpenConns    int           `env:"DB_MAX_OPEN_CONNS" envDefault:"25"`
	MaxIdleConns    int           `env:"DB_MAX_IDLE_CONNS" envDefault:"10"`
	ConnMaxLifetime time.Duration `env:"DB_CONN_MAX_LIFETIME" envDefault:"30m"`
}

type Redis struct {
	Host     string `env:"HOST" envDefault:"localhost"`
	Port     int    `env:"PORT" envDefault:"6379"`
	Password string `env:"PASSWORD" envDefault:""`
	DB       int    `env:"DB" envDefault:"0"`
}

type JWT struct {
	PrivateKeyPath  string        `env:"JWT_PRIVATE_KEY_PATH" envDefault:"keys/private.pem"`
	PublicKeyPath   string        `env:"JWT_PUBLIC_KEY_PATH" envDefault:"keys/public.pem"`
	AccessTokenTTL  time.Duration `env:"JWT_ACCESS_TOKEN_TTL" envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"JWT_REFRESH_TOKEN_TTL" envDefault:"168h"`
}

type PIN struct {
	PrivateKeyPath string `env:"PIN_PRIVATE_KEY_PATH" envDefault:"keys/pin_private.pem"`
	PublicKeyPath  string `env:"PIN_PUBLIC_KEY_PATH" envDefault:"keys/pin_public.pem"`

	// KeyID menamai pasangan kunci yang sedang aktif. Client mengirimkannya
	// balik sebagai `encryption_key_id`, jadi rotasi kunci bisa dilacak:
	// ciphertext yang dibuat dengan kunci lama ditolak dengan sebab yang jelas
	// alih-alih muncul sebagai "PIN selalu salah".
	KeyID string `env:"PIN_KEY_ID" envDefault:"pin-key-v1"`
}

// WebRTC memuat daftar ICE server yang dikirim ke aplikasi bersama
// signaling_url. Kosong berarti tidak ada TURN: panggilan masih jadi di
// jaringan yang ramah, dan gagal di seluler ber-NAT ketat. Kredensialnya milik
// penyedia TURN, jadi tempatnya di environment — bukan di kode.
type WebRTC struct {
	STUNURLs       []string `env:"STUN_URLS" envSeparator:","`
	TURNURLs       []string `env:"TURN_URLS" envSeparator:","`
	TURNUsername   string   `env:"TURN_USERNAME"`
	TURNCredential string   `env:"TURN_CREDENTIAL"`

	// TURNTTL adalah masa berlaku kredensial TURN yang dilaporkan ke client,
	// supaya aplikasi tahu kapan harus meminta yang baru.
	TURNTTL time.Duration `env:"TURN_CREDENTIAL_TTL" envDefault:"12h"`
}

type Crypto struct {
	AESKey        string `env:"AES_KEY"`
	LookupHMACKey string `env:"LOOKUP_HMAC_SECRET"`
}

type Argon2 struct {
	Time          uint32 `env:"ARGON2_TIME" envDefault:"3"`
	Memory        uint32 `env:"ARGON2_MEMORY" envDefault:"65536"`
	Threads       uint8  `env:"ARGON2_THREADS" envDefault:"4"`
	KeyLength     uint32 `env:"ARGON2_KEY_LENGTH" envDefault:"32"`
	SaltLength    uint32 `env:"ARGON2_SALT_LENGTH" envDefault:"16"`
	MaxConcurrent int    `env:"ARGON2_MAX_CONCURRENT" envDefault:"8"`
}

func (db DB) DSN() string {
	return "host=" + db.Host +
		" port=" + itoa(db.Port) +
		" user=" + db.User +
		" password=" + db.Password +
		" dbname=" + db.Name +
		" sslmode=" + db.SSLMode
}

func Load() (*Config, error) {
	var cfg Config

	// Whole-struct parse first: top-level fields, plus every nested struct whose
	// tags are already unprefixed (App, DB, JWT, PIN, Crypto, Argon2).
	//
	// It has to come FIRST because it also walks RedisSession and RedisCache,
	// whose tags are bare names (HOST, PORT, ...) meant to be read under a
	// prefix. Unprefixed, those names are unset, so this pass writes their
	// envDefaults — localhost:6379 — over anything already there. Running it
	// last (as it used to) therefore silently discarded REDIS_CACHE_PORT and
	// pointed both clients at the session instance: the cache Redis sat empty
	// and both workloads shared one server with one eviction policy.
	if err := env.Parse(&cfg); err != nil {
		return nil, err
	}

	// Now the prefixed instances, which must win over those defaults.
	if err := env.ParseWithOptions(&cfg.RedisSession, env.Options{Prefix: "REDIS_SESSION_"}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.RedisCache, env.Options{Prefix: "REDIS_CACHE_"}); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// wellKnownInternalKeys are the placeholder values that ship in the example
// env files. Accepting one in production would be the same as accepting none.
var wellKnownInternalKeys = map[string]bool{
	"dev-internal-key": true,
	"changeme":         true,
	"secret":           true,
}

// Validate refuses to start a production process with development defaults.
//
// Every check here is a setting whose absence is silently insecure rather than
// loudly broken: the service would boot happily and only the audit log, or an
// attacker, would know.
func (c *Config) Validate() error {
	if !c.IsProduction() {
		return nil
	}

	var problems []string

	if strings.TrimSpace(c.InternalAPIKey) == "" {
		problems = append(problems, "INTERNAL_API_KEY is required outside development (it guards the CS and monitoring endpoints)")
	} else if wellKnownInternalKeys[strings.ToLower(strings.TrimSpace(c.InternalAPIKey))] {
		problems = append(problems, "INTERNAL_API_KEY is set to a well-known placeholder value")
	}

	if strings.TrimSpace(c.Crypto.AESKey) == "" {
		problems = append(problems, "AES_KEY is required outside development (PII at rest is stored unencrypted without it)")
	}
	if strings.TrimSpace(c.Crypto.LookupHMACKey) == "" {
		problems = append(problems, "LOOKUP_HMAC_SECRET is required outside development (phone/email lookups depend on it)")
	}

	if strings.TrimSpace(c.SMS.Provider) == "" {
		problems = append(problems, "SMS_PROVIDER is required outside development (without it every OTP is generated, stored and never delivered, so buka rekening stops at OTP_VERIFY)")
	}

	if !strings.HasPrefix(strings.ToLower(c.SignalingBaseURL), "wss://") {
		problems = append(problems, "SIGNALING_BASE_URL must use wss:// outside development")
	}

	if strings.EqualFold(c.DB.SSLMode, "disable") {
		problems = append(problems, "DB_SSLMODE must not be \"disable\" outside development")
	}

	// The stub liveness provider is refused twice: ProvidersWith ignores it
	// outside development, and the boot refuses to start with it configured. One
	// variable set by mistake must not be able to make liveness a formality.
	if strings.EqualFold(strings.TrimSpace(c.Liveness.Provider), "stub") {
		problems = append(problems, "LIVENESS_PROVIDER=stub is only permitted when APP_ENV=development (it accepts any submission that passes the deterministic checks)")
	}
	if c.Liveness.IntegrityLogOnly {
		problems = append(problems, "LIVENESS_INTEGRITY_LOG_ONLY must be false outside development (a failing Play Integrity verdict has to refuse the attempt, not just be logged)")
	}
	if !strings.EqualFold(strings.TrimSpace(c.KTP.DukcapilMode), "real") {
		problems = append(problems, "DUKCAPIL_MODE must be \"real\" outside development (\"off\" skips the population registry check, and \"mock\" only recognises four test identities)")
	}
	if strings.EqualFold(strings.TrimSpace(c.KTP.StorageDriver), "mock") {
		problems = append(problems, "KTP_STORAGE_DRIVER=mock discards the KTP photo, which also leaves biometric face match with no reference")
	}

	if strings.TrimSpace(c.Liveness.PlayIntegrityCredentialsFile) == "" {
		problems = append(problems, "PLAY_INTEGRITY_CREDENTIALS_FILE is required outside development (without it no device verdict is verified and every liveness attempt is refused)")
	}
	if strings.TrimSpace(c.Liveness.PlayIntegrityCertSHA256) == "" {
		problems = append(problems, "PLAY_INTEGRITY_CERT_SHA256 is required outside development (without it a repackaged APK claiming the right package name is accepted)")
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration for APP_ENV=%s:\n  - %s", c.App.Env, strings.Join(problems, "\n  - "))
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf) - 1
	for n > 0 {
		buf[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(buf[i+1:])
}

// ToDomain maps the liveness policy onto the domain's own configuration type.
//
// Two types rather than one so the domain does not import the env-tag struct, in
// line with the import direction in CLAUDE.md.
func (l Liveness) ToDomain() onboarding.LivenessConfig {
	cfg := onboarding.DefaultLivenessConfig()
	if l.ChallengeTTL > 0 {
		cfg.ChallengeTTL = l.ChallengeTTL
	}
	if l.ActionCount >= 2 {
		cfg.ActionCount = l.ActionCount
	}
	if l.MaxFailures > 0 {
		cfg.MaxFailures = l.MaxFailures
	}
	if l.CooldownDuration > 0 {
		cfg.CooldownDuration = l.CooldownDuration
	}
	if l.MaxCooldownRounds > 0 {
		cfg.MaxCooldownRounds = l.MaxCooldownRounds
	}
	if l.BlockWindow > 0 {
		cfg.BlockWindow = l.BlockWindow
	}
	if l.LivenessThreshold > 0 {
		cfg.LivenessThreshold = l.LivenessThreshold
	}
	if l.FaceMatchThreshold > 0 {
		cfg.FaceMatchThreshold = l.FaceMatchThreshold
	}
	if l.MinFrameDistance > 0 {
		cfg.MinFrameDistance = l.MinFrameDistance
	}
	if l.MaxClockSkew > 0 {
		cfg.MaxClockSkew = l.MaxClockSkew
	}
	cfg.IntegrityLogOnly = l.IntegrityLogOnly
	return cfg
}

// PlayIntegrityConfig loads the service account and returns the verifier config.
//
// A credentials file that cannot be read is logged and skipped rather than
// panicking: the result is a verifier that produces an unacceptable verdict, so
// production refuses every attempt. That is noisy and safe, which is the right
// way round — unlike FCM, where a broken credential fails the boot because a
// server delivering nothing silently is the worse outcome there.
func (l Liveness) PlayIntegrityConfig() onboarding.PlayIntegrityConfig {
	cfg := onboarding.PlayIntegrityConfig{
		PackageName:       l.PlayIntegrityPackageName,
		CertificateSHA256: l.PlayIntegrityCertSHA256,
		Timeout:           l.PlayIntegrityTimeout,
	}
	if l.PlayIntegrityCredentialsFile == "" {
		return cfg
	}

	raw, err := os.ReadFile(l.PlayIntegrityCredentialsFile)
	if err != nil {
		slog.Error("play integrity credentials unreadable; device verdicts will not be verified",
			"path", l.PlayIntegrityCredentialsFile, "error", err)
		return cfg
	}

	var account struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(raw, &account); err != nil {
		slog.Error("play integrity credentials are not valid JSON", "error", err)
		return cfg
	}

	block, _ := pem.Decode([]byte(account.PrivateKey))
	if block == nil {
		slog.Error("play integrity credentials carry no PEM private key")
		return cfg
	}
	key, err := parsePKCS1Or8(block.Bytes)
	if err != nil {
		slog.Error("play integrity private key unusable", "error", err)
		return cfg
	}

	cfg.ClientEmail = account.ClientEmail
	cfg.PrivateKey = key
	cfg.TokenURI = account.TokenURI
	return cfg
}

// parsePKCS1Or8 accepts both encodings Google has issued for service accounts.
func parsePKCS1Or8(der []byte) (*rsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("play integrity key is %T, want RSA", parsed)
	}
	return key, nil
}
