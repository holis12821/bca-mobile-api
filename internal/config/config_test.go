package config

import (
	"strings"
	"testing"
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
	return cfg
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
