package config

import (
	"strings"
	"testing"
	"time"
)

// productionConfig is a config that passes validation; each test then breaks
// exactly one thing, which is how the failure messages stay readable.
func productionConfig() *Config {
	cfg := &Config{
		InternalAPIKey:   "a-real-secret-from-the-vault",
		SignalingBaseURL: "wss://api.bcamobile.id",
	}
	cfg.App.Env = "production"
	cfg.DB.SSLMode = "require"
	cfg.Crypto.AESKey = strings.Repeat("ab", 32)
	cfg.Crypto.LookupHMACKey = "lookup-secret"
	cfg.SMS.Provider = "twilio"
	cfg.Liveness.Provider = "internal"
	cfg.Liveness.PlayIntegrityCredentialsFile = "/etc/secrets/play-integrity.json"
	cfg.Liveness.PlayIntegrityCertSHA256 = "mRbXSyWcS0mGPaVyRo8t1Lh6ZVnkGPBNCVN0b3xCkCo"
	cfg.KTP.DukcapilMode = "real"
	cfg.KTP.StorageDriver = "local"
	return cfg
}

// "off" is the right default for a deployment with no registry access, and the
// wrong answer in production: there the identity has to be confirmed against
// Dukcapil, not merely self-consistent.
func TestValidate_ProductionRejectsDukcapilOff(t *testing.T) {
	for _, value := range []string{"off", "mock", "", " off "} {
		cfg := productionConfig()
		cfg.KTP.DukcapilMode = value

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("DUKCAPIL_MODE=%q must be refused in production", value)
		}
		if !strings.Contains(err.Error(), "DUKCAPIL_MODE") {
			t.Fatalf("error should name the variable, got: %v", err)
		}
	}
}

// The mock storage keeps nothing, which also leaves face match with no
// reference. Allowing it in production would reintroduce both bugs at once.
func TestValidate_ProductionRejectsMockKTPStorage(t *testing.T) {
	cfg := productionConfig()
	cfg.KTP.StorageDriver = "mock"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("KTP_STORAGE_DRIVER=mock must be refused in production")
	}
	if !strings.Contains(err.Error(), "KTP_STORAGE_DRIVER") {
		t.Fatalf("error should name the variable, got: %v", err)
	}
}

// The stub provider accepts any submission that passes the deterministic checks.
// It is refused in two independent places; this is the one that stops the boot,
// so a single mis-set variable cannot turn liveness into a formality.
func TestValidate_ProductionRejectsStubLivenessProvider(t *testing.T) {
	for _, value := range []string{"stub", "STUB", " stub "} {
		cfg := productionConfig()
		cfg.Liveness.Provider = value

		err := cfg.Validate()
		if err == nil {
			t.Fatalf("LIVENESS_PROVIDER=%q must be refused in production", value)
		}
		if !strings.Contains(err.Error(), "LIVENESS_PROVIDER") {
			t.Fatalf("error should name the variable, got: %v", err)
		}
	}
}

// A production server that only logs a failed device verdict is not enforcing one.
func TestValidate_ProductionRejectsIntegrityLogOnly(t *testing.T) {
	cfg := productionConfig()
	cfg.Liveness.IntegrityLogOnly = true

	err := cfg.Validate()
	if err == nil {
		t.Fatal("LIVENESS_INTEGRITY_LOG_ONLY=true must be refused in production")
	}
	if !strings.Contains(err.Error(), "LIVENESS_INTEGRITY_LOG_ONLY") {
		t.Fatalf("error should name the variable, got: %v", err)
	}
}

// Without credentials no device verdict is verified, and with the fail-closed
// policy that means every liveness attempt is refused. Better to say so at boot.
func TestValidate_ProductionRequiresPlayIntegrityCredentials(t *testing.T) {
	cfg := productionConfig()
	cfg.Liveness.PlayIntegrityCredentialsFile = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("PLAY_INTEGRITY_CREDENTIALS_FILE must be required in production")
	}
	if !strings.Contains(err.Error(), "PLAY_INTEGRITY_CREDENTIALS_FILE") {
		t.Fatalf("error should name the variable, got: %v", err)
	}
}

// Without the certificate digest, a repackaged APK claiming the right package
// name is accepted.
func TestValidate_ProductionRequiresPlayIntegrityCertDigest(t *testing.T) {
	cfg := productionConfig()
	cfg.Liveness.PlayIntegrityCertSHA256 = ""

	if err := cfg.Validate(); err == nil {
		t.Fatal("PLAY_INTEGRITY_CERT_SHA256 must be required in production")
	}
}

// ToDomain must not silently zero a policy value: a zero cooldown would mean no
// cooldown at all.
func TestLivenessToDomain_FallsBackToDefaults(t *testing.T) {
	domain := Liveness{}.ToDomain()

	if domain.MaxFailures == 0 || domain.CooldownDuration == 0 ||
		domain.MaxCooldownRounds == 0 || domain.ChallengeTTL == 0 {
		t.Fatalf("an empty config must fall back to the documented defaults, got %+v", domain)
	}
}

func TestLivenessToDomain_CarriesOverrides(t *testing.T) {
	domain := Liveness{
		MaxFailures:      5,
		CooldownDuration: 90 * time.Second,
		IntegrityLogOnly: true,
	}.ToDomain()

	if domain.MaxFailures != 5 {
		t.Fatalf("want MaxFailures 5, got %d", domain.MaxFailures)
	}
	if domain.CooldownDuration != 90*time.Second {
		t.Fatalf("want 90s cooldown, got %s", domain.CooldownDuration)
	}
	if !domain.IntegrityLogOnly {
		t.Fatal("IntegrityLogOnly must carry over, including when true")
	}
}

func TestValidate_DevelopmentIsPermissive(t *testing.T) {
	cfg := &Config{}
	cfg.App.Env = "development"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("development should not require production settings: %v", err)
	}
}

func TestValidate_ProductionAcceptsCompleteConfig(t *testing.T) {
	if err := productionConfig().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Without a provider the service still generates, stores and audits every OTP,
// so every dashboard looks healthy while no nasabah can get past OTP_VERIFY.
// That has to stop the boot rather than wait for a support ticket.
func TestValidate_ProductionRejectsMissingSMSProvider(t *testing.T) {
	for name, provider := range map[string]string{"empty": "", "whitespace": "   "} {
		t.Run(name, func(t *testing.T) {
			cfg := productionConfig()
			cfg.SMS.Provider = provider

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), "SMS_PROVIDER") {
				t.Errorf("error should name the offending variable, got: %v", err)
			}
		})
	}
}

// The point of the whole check: a deployment that forgets INTERNAL_API_KEY used
// to fall back to "dev-internal-key", which is published in this repository,
// and the CS + audit endpoints were then open to anyone who had read it.
func TestValidate_ProductionRejectsMissingOrDefaultInternalKey(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"whitespace":      "   ",
		"shipped default": "dev-internal-key",
		"mixed case":      "Dev-Internal-Key",
	}

	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := productionConfig()
			cfg.InternalAPIKey = key

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), "INTERNAL_API_KEY") {
				t.Errorf("error should name the offending variable, got: %v", err)
			}
		})
	}
}

func TestValidate_ProductionRequiresCryptoKeys(t *testing.T) {
	cfg := productionConfig()
	cfg.Crypto.AESKey = ""
	cfg.Crypto.LookupHMACKey = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	for _, want := range []string{"AES_KEY", "LOOKUP_HMAC_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %s, got: %v", want, err)
		}
	}
}

func TestValidate_ProductionRequiresSecureTransport(t *testing.T) {
	cfg := productionConfig()
	cfg.SignalingBaseURL = "ws://api.bcamobile.id"
	cfg.DB.SSLMode = "disable"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	if !strings.Contains(err.Error(), "SIGNALING_BASE_URL") || !strings.Contains(err.Error(), "DB_SSLMODE") {
		t.Errorf("error should name both settings, got: %v", err)
	}
}

func TestIsProduction(t *testing.T) {
	cases := map[string]bool{
		"":            false,
		"development": false,
		"test":        false,
		"staging":     true,
		"production":  true,
		"PRODUCTION":  true,
	}
	for env, want := range cases {
		cfg := &Config{}
		cfg.App.Env = env
		if got := cfg.IsProduction(); got != want {
			t.Errorf("IsProduction(%q) = %v, want %v", env, got, want)
		}
	}
}

// The whole-struct parse walks RedisSession/RedisCache too, and their tags are
// bare names meant to be read under a prefix. If that pass runs after the
// prefixed ones it overwrites them with envDefaults, which silently pointed the
// cache client at the session instance — one Redis doing both jobs, and
// REDIS_CACHE_PORT ignored.
func TestLoad_KeepsSeparateRedisInstances(t *testing.T) {
	t.Setenv("REDIS_SESSION_HOST", "session-host")
	t.Setenv("REDIS_SESSION_PORT", "6379")
	t.Setenv("REDIS_SESSION_PASSWORD", "session-pass")
	t.Setenv("REDIS_SESSION_DB", "0")
	t.Setenv("REDIS_CACHE_HOST", "cache-host")
	t.Setenv("REDIS_CACHE_PORT", "6380")
	t.Setenv("REDIS_CACHE_PASSWORD", "cache-pass")
	t.Setenv("REDIS_CACHE_DB", "3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.RedisSession.Host != "session-host" || cfg.RedisSession.Port != 6379 {
		t.Errorf("session instance: got %s:%d", cfg.RedisSession.Host, cfg.RedisSession.Port)
	}
	if cfg.RedisCache.Host != "cache-host" || cfg.RedisCache.Port != 6380 {
		t.Errorf("cache instance: got %s:%d, want cache-host:6380", cfg.RedisCache.Host, cfg.RedisCache.Port)
	}
	if cfg.RedisCache.DB != 3 {
		t.Errorf("cache db: got %d, want 3", cfg.RedisCache.DB)
	}
	if cfg.RedisCache.Password != "cache-pass" || cfg.RedisSession.Password != "session-pass" {
		t.Errorf("passwords crossed over: session=%q cache=%q", cfg.RedisSession.Password, cfg.RedisCache.Password)
	}
}

// Top-level settings must survive the reordering that fixed the Redis parse.
func TestLoad_ReadsTopLevelSettings(t *testing.T) {
	t.Setenv("INTERNAL_API_KEY", "key-from-env")
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8,192.168.1.1")
	t.Setenv("SIGNALING_BASE_URL", "wss://example.test")
	t.Setenv("UPLOAD_DIR", "/tmp/uploads")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.InternalAPIKey != "key-from-env" {
		t.Errorf("internal api key: got %q", cfg.InternalAPIKey)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "10.0.0.0/8" {
		t.Errorf("trusted proxies: got %v", cfg.TrustedProxies)
	}
	if cfg.SignalingBaseURL != "wss://example.test" || cfg.UploadDir != "/tmp/uploads" {
		t.Errorf("top-level settings lost: %+v", cfg)
	}
}

// The Verify settings have to survive Load, including the comma-separated channel
// list — a []string behind an env tag is the kind of thing that silently arrives
// as one element containing a comma.
func TestLoad_ReadsVerifySettings(t *testing.T) {
	t.Setenv("SMS_PROVIDER", "twilio_verify")
	t.Setenv("SMS_VERIFY_SERVICE_SID", "VA00000000000000000000000000000000")
	t.Setenv("SMS_VERIFY_CHANNELS", "sms,call")
	t.Setenv("SMS_VERIFY_LOCALE", "id")
	t.Setenv("SMS_VERIFY_CODE_TTL", "5m")
	t.Setenv("SMS_VERIFY_BASE_URL", "http://verify.test")
	t.Setenv("SMS_BASE_URL", "http://messages.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got := cfg.SMS.VerifyChannels; len(got) != 2 || got[0] != "sms" || got[1] != "call" {
		t.Errorf("SMS_VERIFY_CHANNELS: got %v, want [sms call]", got)
	}
	if cfg.SMS.VerifyLocale != "id" {
		t.Errorf("SMS_VERIFY_LOCALE: got %q", cfg.SMS.VerifyLocale)
	}
	if cfg.SMS.VerifyCodeTTL != 5*time.Minute {
		t.Errorf("SMS_VERIFY_CODE_TTL: got %v, want 5m", cfg.SMS.VerifyCodeTTL)
	}
	// The two hosts must not cross over. Verify reading the Messages host is how
	// a correctly configured account answered OTP_EXPIRED to every nasabah.
	if cfg.SMS.VerifyBaseURL != "http://verify.test" || cfg.SMS.BaseURL != "http://messages.test" {
		t.Errorf("hosts crossed over: verify=%q messages=%q", cfg.SMS.VerifyBaseURL, cfg.SMS.BaseURL)
	}
}

// Unset means Twilio's own default, so otp_expires_at is never zero.
func TestLoad_VerifyCodeTTLDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SMS.VerifyCodeTTL != 10*time.Minute {
		t.Errorf("default SMS_VERIFY_CODE_TTL: got %v, want 10m", cfg.SMS.VerifyCodeTTL)
	}
}
