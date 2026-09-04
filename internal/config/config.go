package config

import (
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	App           App
	DB            DB
	RedisSession  Redis
	RedisCache    Redis
	JWT           JWT
	PIN           PIN
	Crypto        Crypto
	Argon2        Argon2
}

type App struct {
	Env              string        `env:"APP_ENV" envDefault:"development"`
	Host             string        `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	Port             int           `env:"SERVER_PORT" envDefault:"8080"`
	ReadTimeout      time.Duration `env:"SERVER_READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout     time.Duration `env:"SERVER_WRITE_TIMEOUT" envDefault:"15s"`
	IdleTimeout      time.Duration `env:"SERVER_IDLE_TIMEOUT" envDefault:"60s"`
	ShutdownTimeout  time.Duration `env:"SERVER_SHUTDOWN_TIMEOUT" envDefault:"10s"`
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
	AESKey         string `env:"AES_KEY"`
	LookupHMACKey  string `env:"LOOKUP_HMAC_SECRET"`
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

	if err := env.ParseWithOptions(&cfg.App, env.Options{}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.DB, env.Options{}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.RedisSession, env.Options{Prefix: "REDIS_SESSION_"}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.RedisCache, env.Options{Prefix: "REDIS_CACHE_"}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.JWT, env.Options{}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.PIN, env.Options{}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.Crypto, env.Options{}); err != nil {
		return nil, err
	}
	if err := env.ParseWithOptions(&cfg.Argon2, env.Options{}); err != nil {
		return nil, err
	}

	return &cfg, nil
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
