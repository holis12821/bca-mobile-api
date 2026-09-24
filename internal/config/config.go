package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
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

	Client Client
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

	if !strings.HasPrefix(strings.ToLower(c.SignalingBaseURL), "wss://") {
		problems = append(problems, "SIGNALING_BASE_URL must use wss:// outside development")
	}

	if strings.EqualFold(c.DB.SSLMode, "disable") {
		problems = append(problems, "DB_SSLMODE must not be \"disable\" outside development")
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
