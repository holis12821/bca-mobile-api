# Project Setup Guide — Go Backend

> Step-by-step setup: dari nol sampai API pertama berjalan

---

## Prerequisites

```bash
# Go 1.23+ (gunakan versi terbaru)
go version   # go1.23.x

# PostgreSQL 16+
psql --version

# Redis 7+
redis-server --version

# Docker & Docker Compose (untuk local dev)
docker --version
docker compose version

# golang-migrate (database migrations)
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# golangci-lint (linter)
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# air (hot reload untuk development)
go install github.com/air-verse/air@latest
```

---

## 1. Initialize Project

```bash
# Buat directory
mkdir bca-mobile-api && cd bca-mobile-api

# Init Go module
go mod init github.com/yourorg/bca-mobile-api

# Buat struktur folder
mkdir -p cmd/server
mkdir -p internal/{config,domain/{auth,account,transaction,ewallet,notification},handler,middleware,repository/{postgres,redis},router,pkg/{crypto,validator,response,pagination,idempotency}}
mkdir -p migrations
mkdir -p scripts
mkdir -p deployments/k8s
```

---

## 2. Dependencies

```bash
# HTTP Router — chi (ringan, idiomatic, stdlib-compatible)
go get github.com/go-chi/chi/v5
go get github.com/go-chi/cors
go get github.com/go-chi/httprate

# Database — pgx (PostgreSQL driver terbaik untuk Go)
go get github.com/jackc/pgx/v5
go get github.com/jackc/pgx/v5/pgxpool

# Redis
go get github.com/redis/go-redis/v9

# JWT
go get github.com/golang-jwt/jwt/v5

# Validation
go get github.com/go-playground/validator/v10

# Configuration
go get github.com/caarlos0/env/v11

# Logging — structured logging (slog sudah built-in di Go 1.21+)
# Tidak perlu dependency tambahan, gunakan log/slog

# Hashing — Argon2 (sudah ada di golang.org/x/crypto)
go get golang.org/x/crypto

# UUID
go get github.com/google/uuid

# Migration tool
go get github.com/golang-migrate/migrate/v4

# Testing
go get github.com/stretchr/testify
go get github.com/DATA-DOG/go-sqlmock
```

### Kenapa dependency ini?

| Dependency | Alasan | Alternatif yang TIDAK dipilih |
|-----------|--------|-------------------------------|
| `chi` | Ringan, compatible net/http, middleware chain | `gin` (terlalu opinionated), `fiber` (non-stdlib) |
| `pgx` | Native PostgreSQL, connection pooling built-in, prepared statements | `database/sql` + `lib/pq` (kurang fitur) |
| `go-redis` | Official Redis client, cluster-ready | `redigo` (kurang maintained) |
| `golang-jwt` | Maintained, RS256 support | `dgrijalva/jwt-go` (archived) |
| `slog` | Built-in Go 1.21+, structured, zero dependency | `zap` (overkill untuk project ini) |
| `env` | Simple struct-based config | `viper` (terlalu besar) |

---

## 3. Core Files

### `cmd/server/main.go`

```go
package main

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "os/signal"
    "syscall"
    "time"

    "github.com/yourorg/bca-mobile-api/internal/config"
    "github.com/yourorg/bca-mobile-api/internal/handler"
    "github.com/yourorg/bca-mobile-api/internal/middleware"
    "github.com/yourorg/bca-mobile-api/internal/repository/postgres"
    "github.com/yourorg/bca-mobile-api/internal/repository/redis"
    "github.com/yourorg/bca-mobile-api/internal/router"

    "github.com/yourorg/bca-mobile-api/internal/domain/auth"
    "github.com/yourorg/bca-mobile-api/internal/domain/account"
    "github.com/yourorg/bca-mobile-api/internal/domain/transaction"
    "github.com/yourorg/bca-mobile-api/internal/domain/ewallet"
)

func main() {
    // 1. Load configuration
    cfg, err := config.Load()
    if err != nil {
        slog.Error("failed to load config", "error", err)
        os.Exit(1)
    }

    // 2. Setup structured logger
    logLevel := slog.LevelInfo
    if cfg.Env == "development" {
        logLevel = slog.LevelDebug
    }
    logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
        Level: logLevel,
    }))
    slog.SetDefault(logger)

    slog.Info("starting server",
        "env", cfg.Env,
        "port", cfg.Port,
    )

    // 3. Initialize database pool
    dbPool, err := postgres.NewPool(context.Background(), cfg.Database)
    if err != nil {
        slog.Error("failed to connect database", "error", err)
        os.Exit(1)
    }
    defer dbPool.Close()
    slog.Info("database connected")

    // 4. Initialize Redis
    redisClient, err := redis.NewClient(cfg.Redis)
    if err != nil {
        slog.Error("failed to connect redis", "error", err)
        os.Exit(1)
    }
    defer redisClient.Close()
    slog.Info("redis connected")

    // 5. Initialize repositories
    authRepo := postgres.NewAuthRepo(dbPool)
    accountRepo := postgres.NewAccountRepo(dbPool)
    txnRepo := postgres.NewTransactionRepo(dbPool)
    ewalletRepo := postgres.NewEWalletRepo(dbPool)
    sessionRepo := redis.NewSessionRepo(redisClient)
    cacheRepo := redis.NewCacheRepo(redisClient)
    rateLimitRepo := redis.NewRateLimitRepo(redisClient)

    // 6. Initialize domain services
    authService := auth.NewService(authRepo, sessionRepo, rateLimitRepo, cfg.JWT)
    accountService := account.NewService(accountRepo, cacheRepo)
    txnService := transaction.NewService(txnRepo, accountRepo, cacheRepo, rateLimitRepo)
    ewalletService := ewallet.NewService(ewalletRepo, txnRepo, accountRepo, cacheRepo)

    // 7. Initialize handlers
    healthHandler := handler.NewHealthHandler(dbPool, redisClient, cacheRepo)
    authHandler := handler.NewAuthHandler(authService)
    accountHandler := handler.NewAccountHandler(accountService)
    txnHandler := handler.NewTransactionHandler(txnService)
    ewalletHandler := handler.NewEWalletHandler(ewalletService)

    // 8. Setup middleware
    authMiddleware := middleware.NewAuthMiddleware(authService, sessionRepo)

    // 9. Build router
    r := router.New(
        healthHandler,
        authHandler,
        accountHandler,
        txnHandler,
        ewalletHandler,
        authMiddleware,
    )

    // 10. Create HTTP server
    srv := &http.Server{
        Addr:              fmt.Sprintf(":%d", cfg.Port),
        Handler:           r,
        ReadTimeout:       15 * time.Second,
        ReadHeaderTimeout: 5 * time.Second,
        WriteTimeout:      30 * time.Second,
        IdleTimeout:       60 * time.Second,
        MaxHeaderBytes:    1 << 20, // 1 MB
    }

    // 11. Graceful shutdown
    go func() {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            slog.Error("server failed", "error", err)
            os.Exit(1)
        }
    }()

    slog.Info("server started", "addr", srv.Addr)

    // Wait for interrupt
    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit

    slog.Info("shutting down server...")

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    if err := srv.Shutdown(ctx); err != nil {
        slog.Error("server forced shutdown", "error", err)
    }

    slog.Info("server stopped")
}
```

### `internal/config/config.go`

```go
package config

import (
    "fmt"
    "time"

    "github.com/caarlos0/env/v11"
)

type Config struct {
    Env  string `env:"APP_ENV" envDefault:"development"`
    Port int    `env:"APP_PORT" envDefault:"8080"`

    Database DatabaseConfig
    Redis    RedisConfig
    JWT      JWTConfig
    Security SecurityConfig
}

type DatabaseConfig struct {
    Host            string        `env:"DB_HOST" envDefault:"localhost"`
    Port            int           `env:"DB_PORT" envDefault:"5432"`
    User            string        `env:"DB_USER" envDefault:"bcamobile"`
    Password        string        `env:"DB_PASSWORD,required"`
    Name            string        `env:"DB_NAME" envDefault:"bcamobile"`
    SSLMode         string        `env:"DB_SSLMODE" envDefault:"disable"`
    MaxConns        int32         `env:"DB_MAX_CONNS" envDefault:"25"`
    MinConns        int32         `env:"DB_MIN_CONNS" envDefault:"5"`
    MaxConnLifetime time.Duration `env:"DB_MAX_CONN_LIFETIME" envDefault:"1h"`
    MaxConnIdleTime time.Duration `env:"DB_MAX_CONN_IDLE_TIME" envDefault:"30m"`
}

func (d DatabaseConfig) DSN() string {
    return fmt.Sprintf(
        "postgres://%s:%s@%s:%d/%s?sslmode=%s",
        d.User, d.Password, d.Host, d.Port, d.Name, d.SSLMode,
    )
}

type RedisConfig struct {
    Host     string `env:"REDIS_HOST" envDefault:"localhost"`
    Port     int    `env:"REDIS_PORT" envDefault:"6379"`
    Password string `env:"REDIS_PASSWORD" envDefault:""`
    DB       int    `env:"REDIS_DB" envDefault:"0"`
    PoolSize int    `env:"REDIS_POOL_SIZE" envDefault:"20"`
}

func (r RedisConfig) Addr() string {
    return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

type JWTConfig struct {
    PrivateKeyPath     string        `env:"JWT_PRIVATE_KEY_PATH" envDefault:"./keys/private.pem"`
    PublicKeyPath      string        `env:"JWT_PUBLIC_KEY_PATH" envDefault:"./keys/public.pem"`
    AccessTokenExpiry  time.Duration `env:"JWT_ACCESS_EXPIRY" envDefault:"15m"`
    RefreshTokenExpiry time.Duration `env:"JWT_REFRESH_EXPIRY" envDefault:"168h"` // 7 days
    Issuer             string        `env:"JWT_ISSUER" envDefault:"bca-mobile-api"`
}

type SecurityConfig struct {
    EncryptionKey       string `env:"ENCRYPTION_KEY,required"`       // AES-256 key (32 bytes hex)
    LookupHMACSecret    string `env:"LOOKUP_HMAC_SECRET,required"`   // HMAC key untuk hash lookup
    PINRSAPrivateKey    string `env:"PIN_RSA_PRIVATE_KEY_PATH" envDefault:"./keys/pin_private.pem"`
    MaxLoginAttempts    int    `env:"MAX_LOGIN_ATTEMPTS" envDefault:"5"`
    LockoutDuration     time.Duration `env:"LOCKOUT_DURATION" envDefault:"30m"`
}

func Load() (*Config, error) {
    cfg := &Config{}
    if err := env.Parse(cfg); err != nil {
        return nil, fmt.Errorf("failed to parse config: %w", err)
    }
    return cfg, nil
}
```

### `internal/router/router.go`

```go
package router

import (
    "net/http"
    "time"

    "github.com/go-chi/chi/v5"
    chimiddleware "github.com/go-chi/chi/v5/middleware"
    "github.com/go-chi/cors"

    "github.com/yourorg/bca-mobile-api/internal/handler"
    "github.com/yourorg/bca-mobile-api/internal/middleware"
)

func New(
    health *handler.HealthHandler,
    auth *handler.AuthHandler,
    account *handler.AccountHandler,
    txn *handler.TransactionHandler,
    ewallet *handler.EWalletHandler,
    authMW *middleware.AuthMiddleware,
) http.Handler {
    r := chi.NewRouter()

    // --- Global Middleware ---
    r.Use(chimiddleware.RequestID)
    r.Use(middleware.RequestIDHeader)          // Propagate X-Request-ID
    r.Use(middleware.StructuredLogger)         // Structured access logging
    r.Use(chimiddleware.Recoverer)             // Panic recovery
    r.Use(middleware.SecurityHeaders)          // Security headers
    r.Use(chimiddleware.RealIP)                // Trust X-Forwarded-For
    r.Use(chimiddleware.Timeout(30 * time.Second))
    r.Use(cors.Handler(cors.Options{
        AllowedOrigins:   []string{},          // No browser access — mobile only
        AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
        AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Device-ID", "X-Request-ID", "X-Idempotency-Key"},
        ExposedHeaders:   []string{"X-Request-ID", "X-RateLimit-Limit", "X-RateLimit-Remaining"},
        MaxAge:           300,
    }))

    // --- API v1 ---
    r.Route("/v1", func(r chi.Router) {

        // Health — no auth
        r.Get("/health", health.Check)

        // --- Auth (public) ---
        r.Route("/auth", func(r chi.Router) {
            r.Post("/login/pin", auth.LoginPIN)
            r.Post("/login/biometric", auth.LoginBiometric)
            r.Get("/biometric/challenge", auth.BiometricChallenge)
            r.Post("/token/refresh", auth.RefreshToken)

            // Auth required
            r.Group(func(r chi.Router) {
                r.Use(authMW.Authenticate)
                r.Post("/logout", auth.Logout)
                r.Post("/pin/change", auth.ChangePIN)
                r.Post("/pin/verify", auth.VerifyPIN)
                r.Post("/biometric/register", auth.RegisterBiometric)
            })
        })

        // --- Protected Routes ---
        r.Group(func(r chi.Router) {
            r.Use(authMW.Authenticate)

            // Account
            r.Route("/account", func(r chi.Router) {
                r.Get("/profile", account.GetProfile)
                r.Put("/profile", account.UpdateProfile)
                r.Get("/balance", account.GetBalance)
                r.Get("/dashboard", account.GetDashboard)
                r.Put("/settings", account.UpdateSettings)
                r.Put("/transaction-limit", account.UpdateTransactionLimit)
            })

            // Transactions
            r.Route("/transactions", func(r chi.Router) {
                r.Get("/mutations", txn.GetMutations)
                r.Get("/history", txn.GetHistory)
                r.Get("/{transactionID}/receipt", txn.GetReceipt)
                r.Get("/{transactionID}/receipt/pdf", txn.GetReceiptPDF)
            })

            // Transfer
            r.Route("/transfer", func(r chi.Router) {
                r.Get("/recent", txn.GetRecentTransfers)
                r.Post("/inquiry", txn.InquiryTransfer)
                r.Post("/execute", txn.ExecuteTransfer)
            })

            // E-Wallet
            r.Route("/ewallet", func(r chi.Router) {
                r.Get("/providers", ewallet.GetProviders)
                r.Post("/inquiry", ewallet.Inquiry)
                r.Post("/topup", ewallet.TopUp)
            })

            // Notifications
            r.Route("/notifications", func(r chi.Router) {
                r.Get("/", txn.GetNotifications)
                r.Put("/{notificationID}/read", txn.MarkNotificationRead)
                r.Put("/read-all", txn.MarkAllNotificationsRead)
            })

            // QRIS
            r.Route("/qris", func(r chi.Router) {
                r.Post("/decode", txn.DecodeQRIS)
                r.Post("/pay", txn.PayQRIS)
            })
        })

        // --- Registration (public, rate-limited) ---
        r.Route("/registration", func(r chi.Router) {
            r.Post("/initiate", auth.InitiateRegistration)
            r.Post("/verify-otp", auth.VerifyRegistrationOTP)
            r.Post("/upload-document", auth.UploadDocument)
            r.Post("/complete", auth.CompleteRegistration)
        })
    })

    return r
}
```

### `internal/pkg/response/response.go`

```go
package response

import (
    "encoding/json"
    "net/http"
    "time"

    chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type Response struct {
    Status     string      `json:"status"`
    Data       interface{} `json:"data,omitempty"`
    Error      *ErrorBody  `json:"error,omitempty"`
    Pagination *Pagination `json:"pagination,omitempty"`
    Meta       Meta        `json:"meta"`
}

type ErrorBody struct {
    Code    string      `json:"code"`
    Message string      `json:"message"`
    Details interface{} `json:"details,omitempty"`
}

type Pagination struct {
    Cursor  string `json:"cursor"`
    HasMore bool   `json:"has_more"`
    Limit   int    `json:"limit"`
}

type Meta struct {
    RequestID string `json:"request_id"`
    Timestamp string `json:"timestamp"`
}

func Success(w http.ResponseWriter, r *http.Request, status int, data interface{}) {
    writeJSON(w, r, status, Response{
        Status: "success",
        Data:   data,
        Meta:   meta(r),
    })
}

func SuccessPaginated(w http.ResponseWriter, r *http.Request, data interface{}, pagination Pagination) {
    writeJSON(w, r, http.StatusOK, Response{
        Status:     "success",
        Data:       data,
        Pagination: &pagination,
        Meta:       meta(r),
    })
}

func Error(w http.ResponseWriter, r *http.Request, status int, code, message string, details interface{}) {
    writeJSON(w, r, status, Response{
        Status: "error",
        Error: &ErrorBody{
            Code:    code,
            Message: message,
            Details: details,
        },
        Meta: meta(r),
    })
}

func meta(r *http.Request) Meta {
    return Meta{
        RequestID: chimiddleware.GetReqID(r.Context()),
        Timestamp: time.Now().UTC().Format(time.RFC3339),
    }
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v interface{}) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(status)
    json.NewEncoder(w).Encode(v)
}
```

### `internal/domain/auth/entity.go`

```go
package auth

import (
    "time"

    "github.com/google/uuid"
)

type User struct {
    ID               uuid.UUID
    FullName         string
    DisplayName      string
    PhoneHash        string
    PINHash          string
    PINSalt          string
    Status           UserStatus
    LockedUntil      *time.Time
    FailedPINAttempts int
    MaxPINAttempts   int
    LastLoginAt      *time.Time
    CreatedAt        time.Time
    UpdatedAt        time.Time
}

type UserStatus string

const (
    UserActive    UserStatus = "ACTIVE"
    UserLocked    UserStatus = "LOCKED"
    UserSuspended UserStatus = "SUSPENDED"
    UserClosed    UserStatus = "CLOSED"
)

type Session struct {
    ID               uuid.UUID
    UserID           uuid.UUID
    DeviceID         uuid.UUID
    RefreshTokenHash string
    IPAddress        string
    UserAgent        string
    AuthMethod       AuthMethod
    ExpiresAt        time.Time
    CreatedAt        time.Time
}

type AuthMethod string

const (
    AuthPIN         AuthMethod = "PIN"
    AuthFingerprint AuthMethod = "FINGERPRINT"
    AuthFaceID      AuthMethod = "FACE_ID"
)

type BiometricKey struct {
    ID            uuid.UUID
    UserID        uuid.UUID
    DeviceID      uuid.UUID
    KeyID         string
    PublicKey     string
    BiometricType AuthMethod
    IsActive      bool
    CreatedAt     time.Time
}

type Device struct {
    ID           uuid.UUID
    UserID       uuid.UUID
    DeviceIDStr  string // Device fingerprint from client
    DeviceName   string
    DeviceModel  string
    OSVersion    string
    AppVersion   string
    IsTrusted    bool
    LastActiveAt *time.Time
    CreatedAt    time.Time
}

type TokenPair struct {
    AccessToken  string `json:"access_token"`
    RefreshToken string `json:"refresh_token"`
    TokenType    string `json:"token_type"`
    ExpiresIn    int    `json:"expires_in"`
}

type LoginResult struct {
    TokenPair
    User UserInfo `json:"user"`
}

type UserInfo struct {
    ID            string `json:"id"`
    DisplayName   string `json:"display_name"`
    MaskedAccount string `json:"masked_account"`
}
```

### `internal/domain/auth/repository.go`

```go
package auth

import (
    "context"

    "github.com/google/uuid"
)

// Repository defines the interface for auth data persistence.
// Implemented by postgres package.
type Repository interface {
    // User
    GetUserByPhoneHash(ctx context.Context, phoneHash string) (*User, error)
    GetUserByID(ctx context.Context, id uuid.UUID) (*User, error)
    UpdatePINHash(ctx context.Context, userID uuid.UUID, hash, salt string) error
    IncrementFailedAttempts(ctx context.Context, userID uuid.UUID) error
    ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error
    LockUser(ctx context.Context, userID uuid.UUID, until time.Time) error
    UpdateLastLogin(ctx context.Context, userID uuid.UUID) error

    // Device
    GetOrCreateDevice(ctx context.Context, userID uuid.UUID, deviceID string, info DeviceInfo) (*Device, error)
    GetDevice(ctx context.Context, userID uuid.UUID, deviceID string) (*Device, error)

    // Biometric
    GetBiometricKey(ctx context.Context, keyID string) (*BiometricKey, error)
    RegisterBiometricKey(ctx context.Context, key *BiometricKey) error
    RevokeBiometricKey(ctx context.Context, keyID string) error

    // Session
    CreateSession(ctx context.Context, session *Session) error
    RevokeSession(ctx context.Context, sessionID uuid.UUID) error
    RevokeAllUserSessions(ctx context.Context, userID uuid.UUID) error

    // Audit
    LogAudit(ctx context.Context, entry AuditEntry) error
}

// SessionStore defines the interface for session caching in Redis.
type SessionStore interface {
    SetSession(ctx context.Context, userID, deviceID string, data SessionData, ttl time.Duration) error
    GetSession(ctx context.Context, userID, deviceID string) (*SessionData, error)
    DeleteSession(ctx context.Context, userID, deviceID string) error
    DeleteAllUserSessions(ctx context.Context, userID string) error

    SetRefreshToken(ctx context.Context, tokenHash, value string, ttl time.Duration) error
    GetRefreshToken(ctx context.Context, tokenHash string) (string, error)
    DeleteRefreshToken(ctx context.Context, tokenHash string) error

    SetBiometricChallenge(ctx context.Context, challengeID string, data ChallengeData, ttl time.Duration) error
    GetBiometricChallenge(ctx context.Context, challengeID string) (*ChallengeData, error)
    DeleteBiometricChallenge(ctx context.Context, challengeID string) error
}

// RateLimiter defines the interface for rate limiting in Redis.
type RateLimiter interface {
    CheckLoginRate(ctx context.Context, deviceID string) (allowed bool, remaining int, err error)
    CheckAccountLockout(ctx context.Context, userID string) (locked bool, err error)
    SetAccountLockout(ctx context.Context, userID string, duration time.Duration) error
}
```

### `internal/domain/auth/service.go`

```go
package auth

import (
    "context"
    "crypto"
    "crypto/rsa"
    "crypto/sha256"
    "encoding/base64"
    "errors"
    "fmt"
    "log/slog"
    "time"

    "github.com/google/uuid"

    appCrypto "github.com/yourorg/bca-mobile-api/internal/pkg/crypto"
)

var (
    ErrInvalidPIN          = errors.New("invalid pin")
    ErrAccountLocked       = errors.New("account locked")
    ErrBiometricNotFound   = errors.New("biometric key not found")
    ErrInvalidSignature    = errors.New("invalid biometric signature")
    ErrChallengeExpired    = errors.New("challenge expired or not found")
    ErrDeviceNotRecognized = errors.New("device not recognized")
    ErrRateLimited         = errors.New("rate limited")
)

type Service struct {
    repo        Repository
    sessions    SessionStore
    rateLimiter RateLimiter
    jwtManager  *appCrypto.JWTManager
}

func NewService(repo Repository, sessions SessionStore, rateLimiter RateLimiter, jwtCfg JWTConfig) *Service {
    jwtManager, err := appCrypto.NewJWTManager(jwtCfg.PrivateKeyPath, jwtCfg.PublicKeyPath, jwtCfg.Issuer, jwtCfg.AccessTokenExpiry, jwtCfg.RefreshTokenExpiry)
    if err != nil {
        slog.Error("failed to initialize JWT manager", "error", err)
        panic(err) // Fatal — cannot start without JWT keys
    }

    return &Service{
        repo:        repo,
        sessions:    sessions,
        rateLimiter: rateLimiter,
        jwtManager:  jwtManager,
    }
}

func (s *Service) LoginWithPIN(ctx context.Context, req LoginPINRequest) (*LoginResult, error) {
    // 1. Rate limit check
    allowed, remaining, err := s.rateLimiter.CheckLoginRate(ctx, req.DeviceID)
    if err != nil {
        return nil, fmt.Errorf("rate limit check: %w", err)
    }
    if !allowed {
        return nil, ErrRateLimited
    }

    // 2. Decrypt PIN
    pin, err := appCrypto.DecryptPIN(req.PINEncrypted)
    if err != nil {
        return nil, fmt.Errorf("decrypt pin: %w", err)
    }

    // 3. Find user (lookup by device → user mapping, or phone)
    device, err := s.repo.GetDevice(ctx, uuid.Nil, req.DeviceID)
    if err != nil {
        return nil, ErrDeviceNotRecognized
    }

    user, err := s.repo.GetUserByID(ctx, device.UserID)
    if err != nil {
        return nil, fmt.Errorf("get user: %w", err)
    }

    // 4. Check lockout
    if user.Status == UserLocked {
        if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
            return nil, ErrAccountLocked
        }
        // Lockout expired — unlock
        _ = s.repo.ResetFailedAttempts(ctx, user.ID)
    }

    // 5. Verify PIN
    if !appCrypto.VerifyPIN(pin, user.PINHash, user.PINSalt) {
        _ = s.repo.IncrementFailedAttempts(ctx, user.ID)
        if user.FailedPINAttempts+1 >= user.MaxPINAttempts {
            lockUntil := time.Now().Add(30 * time.Minute)
            _ = s.repo.LockUser(ctx, user.ID, lockUntil)
            _ = s.rateLimiter.SetAccountLockout(ctx, user.ID.String(), 30*time.Minute)
        }
        return nil, ErrInvalidPIN
    }

    // 6. Success — reset attempts, create session
    _ = s.repo.ResetFailedAttempts(ctx, user.ID)
    _ = s.repo.UpdateLastLogin(ctx, user.ID)

    return s.createSession(ctx, user, device, AuthPIN)
}

func (s *Service) createSession(ctx context.Context, user *User, device *Device, method AuthMethod) (*LoginResult, error) {
    sessionID := uuid.New()

    // Generate tokens
    accessToken, err := s.jwtManager.GenerateAccessToken(user.ID.String(), device.DeviceIDStr, sessionID.String())
    if err != nil {
        return nil, fmt.Errorf("generate access token: %w", err)
    }

    refreshToken, err := s.jwtManager.GenerateRefreshToken(user.ID.String(), device.DeviceIDStr, sessionID.String())
    if err != nil {
        return nil, fmt.Errorf("generate refresh token: %w", err)
    }

    // Hash refresh token for storage
    refreshHash := sha256Hash(refreshToken)

    // Save session to DB
    session := &Session{
        ID:               sessionID,
        UserID:           user.ID,
        DeviceID:         device.ID,
        RefreshTokenHash: refreshHash,
        AuthMethod:       method,
        ExpiresAt:        time.Now().Add(s.jwtManager.RefreshExpiry),
    }
    if err := s.repo.CreateSession(ctx, session); err != nil {
        return nil, fmt.Errorf("create session: %w", err)
    }

    // Cache session in Redis
    _ = s.sessions.SetSession(ctx, user.ID.String(), device.DeviceIDStr, SessionData{
        AccessTokenHash: sha256Hash(accessToken),
        UserID:          user.ID.String(),
        DeviceID:        device.DeviceIDStr,
        DisplayName:     user.DisplayName,
        AuthMethod:      string(method),
    }, s.jwtManager.AccessExpiry)

    // Store refresh token mapping
    _ = s.sessions.SetRefreshToken(ctx, refreshHash,
        fmt.Sprintf("%s:%s", user.ID.String(), device.DeviceIDStr),
        s.jwtManager.RefreshExpiry,
    )

    return &LoginResult{
        TokenPair: TokenPair{
            AccessToken:  accessToken,
            RefreshToken: refreshToken,
            TokenType:    "Bearer",
            ExpiresIn:    int(s.jwtManager.AccessExpiry.Seconds()),
        },
        User: UserInfo{
            ID:          user.ID.String(),
            DisplayName: user.DisplayName,
        },
    }, nil
}

func sha256Hash(s string) string {
    h := sha256.Sum256([]byte(s))
    return base64.RawStdEncoding.EncodeToString(h[:])
}
```

---

## 4. Docker Compose (Local Development)

### `deployments/docker-compose.yml`

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: bca-postgres
    environment:
      POSTGRES_DB: bcamobile
      POSTGRES_USER: bcamobile
      POSTGRES_PASSWORD: localdev_password_123
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U bcamobile"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    container_name: bca-redis
    command: redis-server --requirepass localdev_redis_123
    ports:
      - "6379:6379"
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "-a", "localdev_redis_123", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  postgres_data:
  redis_data:
```

---

## 5. Environment Variables

### `.env.example`

```bash
# Application
APP_ENV=development
APP_PORT=8080

# Database (PostgreSQL)
DB_HOST=localhost
DB_PORT=5432
DB_USER=bcamobile
DB_PASSWORD=localdev_password_123
DB_NAME=bcamobile
DB_SSLMODE=disable
DB_MAX_CONNS=25
DB_MIN_CONNS=5

# Redis
REDIS_HOST=localhost
REDIS_PORT=6379
REDIS_PASSWORD=localdev_redis_123
REDIS_DB=0
REDIS_POOL_SIZE=20

# JWT
JWT_PRIVATE_KEY_PATH=./keys/private.pem
JWT_PUBLIC_KEY_PATH=./keys/public.pem
JWT_ACCESS_EXPIRY=15m
JWT_REFRESH_EXPIRY=168h
JWT_ISSUER=bca-mobile-api

# Security
ENCRYPTION_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
LOOKUP_HMAC_SECRET=your_hmac_secret_here_change_in_production
PIN_RSA_PRIVATE_KEY_PATH=./keys/pin_private.pem
MAX_LOGIN_ATTEMPTS=5
LOCKOUT_DURATION=30m
```

---

## 6. Makefile

```makefile
.PHONY: help setup dev build test lint migrate seed clean keys

# Default
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# === Setup ===

setup: ## First-time project setup
	@echo "=== Setting up BCA Mobile API ==="
	cp -n .env.example .env || true
	$(MAKE) keys
	$(MAKE) infra-up
	sleep 3
	$(MAKE) migrate-up
	$(MAKE) seed
	@echo "=== Setup complete! Run 'make dev' to start ==="

keys: ## Generate RSA key pair for JWT and PIN encryption
	@mkdir -p keys
	@echo "Generating JWT RSA keys..."
	openssl genrsa -out keys/private.pem 2048
	openssl rsa -in keys/private.pem -pubout -out keys/public.pem
	@echo "Generating PIN RSA keys..."
	openssl genrsa -out keys/pin_private.pem 2048
	openssl rsa -in keys/pin_private.pem -pubout -out keys/pin_public.pem
	@echo "Keys generated in ./keys/"

# === Development ===

dev: ## Start dev server with hot reload
	air

run: ## Run server without hot reload
	go run cmd/server/main.go

build: ## Build production binary
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/server cmd/server/main.go

# === Infrastructure ===

infra-up: ## Start PostgreSQL & Redis via Docker
	docker compose -f deployments/docker-compose.yml up -d

infra-down: ## Stop PostgreSQL & Redis
	docker compose -f deployments/docker-compose.yml down

infra-reset: ## Reset all data (destructive!)
	docker compose -f deployments/docker-compose.yml down -v
	$(MAKE) infra-up
	sleep 3
	$(MAKE) migrate-up
	$(MAKE) seed

# === Database ===

migrate-create: ## Create new migration (usage: make migrate-create NAME=create_users)
	migrate create -ext sql -dir migrations -seq $(NAME)

migrate-up: ## Run all pending migrations
	migrate -path migrations -database "postgres://bcamobile:localdev_password_123@localhost:5432/bcamobile?sslmode=disable" up

migrate-down: ## Rollback last migration
	migrate -path migrations -database "postgres://bcamobile:localdev_password_123@localhost:5432/bcamobile?sslmode=disable" down 1

migrate-status: ## Show migration status
	migrate -path migrations -database "postgres://bcamobile:localdev_password_123@localhost:5432/bcamobile?sslmode=disable" version

seed: ## Seed database with test data
	go run scripts/seed/main.go

# === Quality ===

test: ## Run all tests
	go test -race -cover ./...

test-verbose: ## Run tests with verbose output
	go test -race -cover -v ./...

test-coverage: ## Generate coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint: ## Run linter
	golangci-lint run ./...

vet: ## Run go vet
	go vet ./...

check: lint vet test ## Run all checks (lint + vet + test)

# === Cleanup ===

clean: ## Remove build artifacts
	rm -rf bin/ coverage.out coverage.html
```

---

## 7. Hot Reload Config

### `.air.toml`

```toml
root = "."
tmp_dir = "tmp"

[build]
  cmd = "go build -o ./tmp/main ./cmd/server/main.go"
  bin = "./tmp/main"
  delay = 1000 # ms
  include_ext = ["go", "tpl", "tmpl", "html"]
  exclude_dir = ["tmp", "vendor", "node_modules", "deployments", "migrations", "keys", "docs"]
  exclude_regex = ["_test\\.go"]

[log]
  time = false

[color]
  main = "magenta"
  watcher = "cyan"
  build = "yellow"
  runner = "green"
```

---

## 8. Linter Config

### `.golangci.yml`

```yaml
linters:
  enable:
    - errcheck
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - unused
    - gosec           # Security linter
    - bodyclose       # HTTP body close
    - contextcheck    # Context propagation
    - nilerr
    - sqlclosecheck   # SQL rows close
    - rowserrcheck    # SQL rows err check

linters-settings:
  gosec:
    severity: medium
    excludes:
      - G104  # Unhandled errors (terlalu noisy untuk audit log)

issues:
  max-issues-per-linter: 50
  max-same-issues: 3

run:
  timeout: 5m
```

---

## 9. Quick Start

```bash
# 1. Clone & masuk ke project
cd bca-mobile-api

# 2. Setup (satu kali)
make setup

# Output:
#   - .env dibuat dari template
#   - RSA keys di-generate
#   - Docker containers started (Postgres + Redis)
#   - Migrations dijalankan
#   - Test data di-seed

# 3. Jalankan development server
make dev

# Server berjalan di http://localhost:8080

# 4. Test health endpoint
curl http://localhost:8080/v1/health | jq

# 5. Jalankan semua quality checks
make check
```

---

## 10. Fase Pengembangan (Roadmap)

### Phase 1: Foundation (Minggu 1-2)
```
[x] Project structure
[x] Config management
[x] Database connection pool
[x] Redis connection
[x] Migration framework
[x] Health endpoint
[x] Standard response format
[x] Middleware stack (logging, recovery, security headers, request ID)
[x] Error handling pattern
```

### Phase 2: Authentication (Minggu 3-4)
```
[ ] PIN login flow
[ ] JWT generation & validation
[ ] Session management (Redis + PostgreSQL)
[ ] Rate limiting middleware
[ ] Biometric challenge-response
[ ] Biometric key registration
[ ] Token refresh with rotation
[ ] Logout (single + all devices)
[ ] Account lockout
[ ] Audit logging
```

### Phase 3: Account & Dashboard (Minggu 5-6)
```
[ ] Profile CRUD
[ ] Balance retrieval
[ ] Dashboard aggregation endpoint
[ ] Settings management
[ ] Transaction limits
[ ] Notification system
[ ] PII encryption/decryption
[ ] Cache layer (Redis)
```

### Phase 4: Transactions (Minggu 7-9)
```
[ ] Mutation listing with pagination
[ ] Transaction history
[ ] Transfer inquiry
[ ] Transfer execution (with idempotency)
[ ] Double-entry bookkeeping
[ ] Daily limit enforcement
[ ] Receipt generation
[ ] Receipt PDF export
```

### Phase 5: E-Wallet & QRIS (Minggu 10-11)
```
[ ] E-Wallet provider listing
[ ] E-Wallet inquiry
[ ] E-Wallet top-up execution
[ ] QRIS decode
[ ] QRIS payment
```

### Phase 6: Registration (Minggu 12)
```
[ ] Registration initiation
[ ] OTP flow
[ ] Document upload
[ ] KYC stub
```

### Phase 7: Hardening (Minggu 13-14)
```
[ ] Integration tests
[ ] Load testing
[ ] Security audit
[ ] Performance tuning
[ ] Monitoring & alerting setup
[ ] Documentation final
```

---

## Catatan Penting

1. **Jangan deploy tanpa security review** — Kode ini adalah panduan arsitektur. Production deployment memerlukan: penetration testing, security audit, compliance review (PCI-DSS, OJK).

2. **HSM untuk production** — Private keys (JWT, PIN decryption) harus disimpan di Hardware Security Module, bukan file system.

3. **Core Banking Integration** — Dalam realita, balance dan mutations datang dari core banking system (biasanya mainframe). API ini berfungsi sebagai **middleware layer** antara mobile app dan core banking.

4. **High Availability** — Production memerlukan: multi-instance, load balancer, PostgreSQL replication, Redis Sentinel/Cluster.