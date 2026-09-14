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
	"github.com/holis12821/bca-mobile-api/internal/domain/ewallet"
	"github.com/holis12821/bca-mobile-api/internal/domain/qris"
	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
	"github.com/holis12821/bca-mobile-api/internal/pkg/idempotency"
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
	notifH := handler.NewNotificationHandler(accountService)

	// Transaction dependencies
	mutationRepo := postgres.NewMutationRepo(deps.DB)
	transactionRepo := postgres.NewTransactionRepo(deps.DB)
	inquiryRepo := postgres.NewInquiryRepo(deps.DB)
	vtokenRepo := postgres.NewVerificationTokenRepo(deps.DB)
	favoriteRepo := postgres.NewFavoriteTransferRepo(deps.DB)

	inquiryCache := redisrepo.NewInquiryCache(deps.RedisSession)
	vtokenCache := redisrepo.NewVerificationTokenCache(deps.RedisSession)
	recentCache := redisrepo.NewRecentTransferCache(deps.RedisCache)
	txnCacheInvalidator := redisrepo.NewTransactionCacheInvalidator(deps.RedisCache)
	receiptCache := redisrepo.NewReceiptCache(deps.RedisCache)
	transferExecutor := postgres.NewTransferExecutor(deps.DB)
	idemStore := idempotency.NewStore(deps.RedisSession)

	txnService := transaction.NewService(transaction.ServiceConfig{
		Mutations:        mutationRepo,
		Transactions:     transactionRepo,
		Inquiries:        inquiryRepo,
		VTokens:          vtokenRepo,
		Favorites:        favoriteRepo,
		Accounts:         accountRepo,
		Executor:         transferExecutor,
		InquiryCache:     inquiryCache,
		VTokenCache:      vtokenCache,
		RecentCache:      recentCache,
		ReceiptCache:     receiptCache,
		CacheInvalidator: txnCacheInvalidator,
		IdemStore:        idemStore,
		Versions:         versionCounter,
	})

	// Wire PIN verify → verification token issuance
	authH.SetTransactionService(txnService)

	txnH := handler.NewTransactionHandler(txnService)
	transferH := handler.NewTransferHandler(txnService)

	// E-Wallet dependencies
	ewalletProviderRepo := postgres.NewEWalletProviderRepo(deps.DB)
	ewalletExecutor := postgres.NewEWalletExecutor(deps.DB)
	ewalletProviderCache := redisrepo.NewEWalletProviderCache(deps.RedisCache)

	ewalletService := ewallet.NewService(ewallet.ServiceConfig{
		Providers:        ewalletProviderRepo,
		ProviderStub:     ewallet.NewFakeProvider(),
		Executor:         ewalletExecutor,
		Accounts:         accountRepo,
		InquiryCache:     inquiryCache,
		VTokenCache:      vtokenCache,
		ProviderCache:    ewalletProviderCache,
		CacheInvalidator: txnCacheInvalidator,
		IdemStore:        idemStore,
		Versions:         versionCounter,
	})

	ewalletH := handler.NewEWalletHandler(ewalletService)

	// QRIS dependencies
	qrisExecutor := postgres.NewQRISExecutor(deps.DB)
	qrisDecodeCache := redisrepo.NewQRISDecodeCache(deps.RedisSession)

	qrisService := qris.NewService(qris.ServiceConfig{
		Executor:         qrisExecutor,
		Accounts:         accountRepo,
		DecodeCache:      qrisDecodeCache,
		VTokenCache:      vtokenCache,
		CacheInvalidator: txnCacheInvalidator,
		IdemStore:        idemStore,
		Versions:         versionCounter,
	})

	qrisH := handler.NewQRISHandler(qrisService)

	// Registration dependencies
	regCache := redisrepo.NewRegistrationCache(deps.RedisSession)
	regExecutor := postgres.NewRegistrationExecutor(deps.DB)

	regService := registration.NewService(registration.ServiceConfig{
		Cache:      regCache,
		Executor:   regExecutor,
		JWTManager: deps.JWTManager,
		PINKeys:    deps.PINKeys,
	})

	uploadDir := "uploads"
	if deps.Config != nil && deps.Config.UploadDir != "" {
		uploadDir = deps.Config.UploadDir
	}
	regH := handler.NewRegistrationHandler(regService, deps.JWTManager, uploadDir)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/health", healthH.Health)
		r.Get("/health/live", healthH.Liveness)
		r.Get("/health/config", healthH.Config)

		// Public auth endpoints
		r.Post("/auth/login/pin", authH.LoginPIN)
		r.Post("/auth/login/biometric", authH.BiometricLogin)
		r.Post("/auth/biometric/challenge", authH.BiometricChallenge)
		r.Post("/auth/token/refresh", authH.RefreshToken)

		// Registration (public endpoints)
		r.Post("/registration/initiate", regH.Initiate)
		r.Post("/registration/verify-otp", regH.VerifyOTP)
		r.Post("/registration/upload-document", regH.UploadDocument)
		r.Post("/registration/complete", regH.Complete)

		// Protected endpoints (require valid access token)
		r.Group(func(r chi.Router) {
			r.Use(middleware.Auth(deps.JWTManager))

			// Auth
			r.Post("/auth/logout", authH.Logout)
			r.Post("/auth/pin/change", authH.ChangePIN)
			r.Post("/auth/biometric/register", authH.RegisterBiometric)
			r.Post("/auth/pin/verify", authH.PINVerify)

			// Account & Profile
			r.Get("/account/profile", accountH.Profile)
			r.Get("/account/balance", accountH.Balance)
			r.Get("/account/dashboard", accountH.Dashboard)
			r.Put("/account/transaction-limit", accountH.UpdateTransactionLimit)

			// Account update endpoints
			r.Put("/account/profile", accountH.UpdateProfile)
			r.Put("/account/settings", accountH.UpdateSettings)

			// Notifications
			r.Get("/notifications", notifH.ListNotifications)
			r.Put("/notifications/{id}/read", notifH.MarkNotificationRead)
			r.Put("/notifications/read-all", notifH.MarkAllNotificationsRead)

			// Transactions
			r.Get("/transactions/mutations", txnH.ListMutations)
			r.Get("/transactions/history", txnH.ListHistory)
			r.Get("/transfer/recent", txnH.ListRecentTransfers)
			r.Get("/transactions/{transaction_id}/receipt", txnH.GetReceipt)
			r.Post("/transfer/inquiry", transferH.Inquiry)
			r.Post("/transfer/execute", transferH.Execute)

			// E-Wallet
			r.Get("/ewallet/providers", ewalletH.ListProviders)
			r.Post("/ewallet/inquiry", ewalletH.Inquiry)
			r.Post("/ewallet/topup", ewalletH.TopUp)

			// QRIS
			r.Post("/qris/decode", qrisH.Decode)
			r.Post("/qris/pay", qrisH.Pay)
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