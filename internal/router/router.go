package router

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/config"
	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
	"github.com/holis12821/bca-mobile-api/internal/repository/postgres"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
)

type Deps struct {
	DB           *pgxpool.Pool
	RedisSession *goredis.Client
	RedisCache   *goredis.Client
	PINKeys      *crypto.RSAKeyPair
	JWTManager   *crypto.JWTManager
	Config       *config.Config
	PIIKey       []byte // AES-256-GCM key for PII encryption/decryption
}

func New(deps Deps) http.Handler {
	r := chi.NewRouter()

	// Global middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.Recovery)
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.CORS())
	r.Use(middleware.Logging)

	// Handlers
	healthH := handler.NewHealthHandler(deps.DB, deps.RedisSession, deps.RedisCache)

	// Auth dependencies
	rateLimiter := redisrepo.NewRateLimiter(deps.RedisSession)
	authRateLimiter := redisrepo.NewAuthRateLimiter(rateLimiter)
	lockoutMgr := redisrepo.NewLockoutManager(deps.RedisSession)
	nonceStore := redisrepo.NewNonceStore(deps.RedisSession)
	sessionCache := redisrepo.NewSessionCache(deps.RedisSession)

	userRepo := postgres.NewUserRepo(deps.DB)
	deviceRepo := postgres.NewDeviceRepo(deps.DB)
	sessionRepo := postgres.NewSessionRepo(deps.DB)
	biometricRepo := postgres.NewBiometricRepo(deps.DB)

	biometricChallengeCache := redisrepo.NewBiometricChallengeCache(deps.RedisSession)
	tokenRevocation := redisrepo.NewTokenRevocationCache(deps.RedisSession)
	auditRepo := postgres.NewAuditRepo(deps.DB)
	auditService := auth.NewAuditService(auditRepo)

	accessTTL := 15 * time.Minute
	refreshTTL := 168 * time.Hour
	if deps.Config != nil {
		accessTTL = deps.Config.JWT.AccessTokenTTL
		refreshTTL = deps.Config.JWT.RefreshTokenTTL
	}

	authService := auth.NewService(auth.ServiceConfig{
		Users:              userRepo,
		Devices:            deviceRepo,
		Sessions:           sessionRepo,
		SessionCache:       sessionCache,
		TokenRevocation:    tokenRevocation,
		BiometricKeys:      biometricRepo,
		BiometricChallenge: biometricChallengeCache,
		RateLimiter:        authRateLimiter,
		Lockout:            lockoutMgr,
		PINKeys:            deps.PINKeys,
		JWTManager:         deps.JWTManager,
		NonceChecker:       nonceStore,
		Audit:              auditService,
		AccessTTL:          accessTTL,
		RefreshTTL:         refreshTTL,
	})

	authH := handler.NewAuthHandler(authService)

	// Account & dashboard dependencies
	accountRepo := postgres.NewAccountRepo(deps.DB)
	profileRepo := postgres.NewProfileRepo(deps.DB, deps.PIIKey)
	limitRepo := postgres.NewTransactionLimitRepo(deps.DB)
	notifRepo := postgres.NewNotificationRepo(deps.DB)

	versionCounter := redisrepo.NewVersionCounter(deps.RedisCache)
	profileCache := redisrepo.NewProfileCache(deps.RedisCache)
	balanceCache := redisrepo.NewBalanceCache(deps.RedisCache)
	dashboardCache := redisrepo.NewDashboardCache(deps.RedisCache)
	notifCache := redisrepo.NewNotificationCache(deps.RedisCache)

	accountService := account.NewService(account.ServiceConfig{
		Accounts:      accountRepo,
		Profiles:      profileRepo,
		Limits:        limitRepo,
		Notifications: notifRepo,
		ProfileCache:  profileCache,
		BalanceCache:  balanceCache,
		DashCache:     dashboardCache,
		NotifCache:    notifCache,
		Versions:      versionCounter,
	})

	accountH := handler.NewAccountHandler(accountService)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/health", healthH.Health)
		r.Get("/health/live", healthH.Liveness)
		r.Get("/health/config", healthH.Config)

		// Public auth endpoints
		r.Post("/auth/login/pin", authH.LoginPIN)
		r.Post("/auth/login/biometric", authH.BiometricLogin)
		r.Post("/auth/biometric/challenge", authH.BiometricChallenge)
		r.Post("/auth/token/refresh", authH.RefreshToken)

		// Protected endpoints (require valid access token)
		r.Group(func(r chi.Router) {
			r.Use(middleware.Auth(deps.JWTManager))

			// Auth
			r.Post("/auth/logout", authH.Logout)
			r.Post("/auth/biometric/register", authH.RegisterBiometric)

			// Account & Profile
			r.Get("/account/profile", accountH.Profile)
			r.Get("/account/balance", accountH.Balance)
			r.Get("/account/dashboard", accountH.Dashboard)
			r.Put("/account/transaction-limit", accountH.UpdateTransactionLimit)

			// Notifications
			r.Get("/notifications", accountH.ListNotifications)
			r.Put("/notifications/{id}/read", accountH.MarkNotificationRead)
			r.Put("/notifications/read-all", accountH.MarkAllNotificationsRead)
		})
	})

	// Not found — consistent envelope
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		response.Err(w, r, apperr.NotFound)
	})

	// Method not allowed — consistent envelope
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		response.Err(w, r, apperr.Error{
			Status:  http.StatusMethodNotAllowed,
			Code:    "METHOD_NOT_ALLOWED",
			Message: "Metode HTTP tidak diizinkan.",
		})
	})

	return r
}