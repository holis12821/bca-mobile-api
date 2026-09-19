package router

import (
	"encoding/hex"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/config"
	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/domain/ewallet"
	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
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
	ws "github.com/holis12821/bca-mobile-api/internal/websocket"
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
	r.Use(middleware.CORS(corsOrigins(deps.Config)))
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

	// Onboarding dependencies
	onboardingSessionRepo := postgres.NewOnboardingSessionRepo(deps.DB)
	onboardingAuditRepo := postgres.NewOnboardingAuditRepo(deps.DB)
	onboardingOCRRepo := postgres.NewOnboardingOCRResultRepo(deps.DB)
	onboardingCache := redisrepo.NewOnboardingSessionCache(deps.RedisCache)
	ocrRateLimiter := redisrepo.NewOCRRateLimiter(deps.RedisSession)

	onboardingSessionService := onboarding.NewSessionService(onboarding.SessionServiceConfig{
		Sessions: onboardingSessionRepo,
		Cache:    onboardingCache,
		Audit:    onboardingAuditRepo,
	})

	var piiAES *crypto.AES
	if len(deps.PIIKey) > 0 {
		var aesErr error
		piiAES, aesErr = crypto.NewAES(hex.EncodeToString(deps.PIIKey))
		if aesErr != nil {
			panic("invalid PII AES key: " + aesErr.Error())
		}
	}

	ocrService := onboarding.NewOCRService(onboarding.OCRServiceConfig{
		Sessions:    onboardingSessionRepo,
		Cache:       onboardingCache,
		OCRResults:  onboardingOCRRepo,
		OCREngine:   onboarding.NewMockOCREngine(),
		Dukcapil:    onboarding.NewMockDukcapilClient(),
		Storage:     onboarding.NewMockObjectStorage(),
		RateLimiter: ocrRateLimiter,
		AES:         piiAES,
		Audit:       onboardingAuditRepo,
	})

	onboardingPersonalDataRepo := postgres.NewOnboardingPersonalDataRepo(deps.DB)
	onboardingOTPCache := redisrepo.NewOnboardingOTPCache(deps.RedisSession)

	personalDataService := onboarding.NewPersonalDataService(onboarding.PersonalDataServiceConfig{
		Sessions:     onboardingSessionRepo,
		Cache:        onboardingCache,
		OCRResults:   onboardingOCRRepo,
		PersonalData: onboardingPersonalDataRepo,
		OTPCache:     onboardingOTPCache,
		SMS:          onboarding.NewMockSMSGateway(),
		AES:          piiAES,
		Audit:        onboardingAuditRepo,
	})

	onboardingBiometricRepo := postgres.NewOnboardingBiometricRepo(deps.DB)
	bioRateLimiter := redisrepo.NewBiometricRateLimiter(deps.RedisSession)

	biometricService := onboarding.NewBiometricService(onboarding.BiometricServiceConfig{
		Sessions:    onboardingSessionRepo,
		Cache:       onboardingCache,
		OCRResults:  onboardingOCRRepo,
		Biometrics:  onboardingBiometricRepo,
		Engine:      onboarding.NewMockBiometricEngine(),
		Storage:     onboarding.NewMockObjectStorage(),
		RateLimiter: bioRateLimiter,
		AES:         piiAES,
		Audit:       onboardingAuditRepo,
	})

	onboardingVideoCallRepo := postgres.NewOnboardingVideoCallRepo(deps.DB)
	videoCallQueueCache := redisrepo.NewVideoCallQueueCache(deps.RedisSession)

	sigBaseURL := "ws://localhost:8080"
	if deps.Config != nil && deps.Config.SignalingBaseURL != "" {
		sigBaseURL = deps.Config.SignalingBaseURL
	}

	videoCallService := onboarding.NewVideoCallService(onboarding.VideoCallServiceConfig{
		Sessions:        onboardingSessionRepo,
		Cache:           onboardingCache,
		VideoCalls:      onboardingVideoCallRepo,
		QueueCache:      videoCallQueueCache,
		JWTManager:      deps.JWTManager,
		Audit:           onboardingAuditRepo,
		SignalingBaseURL: sigBaseURL,
	})

	onboardingCredentialRepo := postgres.NewOnboardingCredentialRepo(deps.DB)

	credentialService := onboarding.NewCredentialService(onboarding.CredentialServiceConfig{
		Sessions:    onboardingSessionRepo,
		Cache:       onboardingCache,
		Credentials: onboardingCredentialRepo,
		PINKeys:     deps.PINKeys,
		Audit:       onboardingAuditRepo,
	})

	onboardingIdemCache := redisrepo.NewOnboardingIdempotencyCache(deps.RedisSession)

	submitService := onboarding.NewSubmitService(onboarding.SubmitServiceConfig{
		Sessions:     onboardingSessionRepo,
		Cache:        onboardingCache,
		PersonalData: onboardingPersonalDataRepo,
		Credentials:  onboardingCredentialRepo,
		CoreBanking:  onboarding.NewMockCoreBankingClient(),
		Idempotency:  onboardingIdemCache,
		AES:          piiAES,
		Audit:        onboardingAuditRepo,
	})

	monitoringService := onboarding.NewMonitoringService(onboarding.MonitoringServiceConfig{
		Sessions:   onboardingSessionRepo,
		Audit:      onboardingAuditRepo,
		QueueCache: videoCallQueueCache,
	})

	sigHub := ws.NewHub()
	signalingH := handler.NewSignalingHandler(sigHub, deps.JWTManager)

	onboardingH := handler.NewOnboardingHandler(onboardingSessionService, ocrService, personalDataService, biometricService, videoCallService, credentialService, submitService, monitoringService, deps.PINKeys)

	uploadDir := "uploads"
	if deps.Config != nil && deps.Config.UploadDir != "" {
		uploadDir = deps.Config.UploadDir
	}
	regH := handler.NewRegistrationHandler(regService, deps.JWTManager, uploadDir)

	r.Route("/v1", func(r chi.Router) {
		r.Get("/health", healthH.Health)
		r.Get("/health/live", healthH.Liveness)
		r.Get("/health/config", healthH.Config)

		// Development-only helpers (Postman / frontend integration).
		// Absent in any other APP_ENV — the route simply does not exist,
		// so a production deployment answers 404 through NotFound.
		if appEnv(deps.Config) == "development" {
			devH := handler.NewDevHandler(deps.PINKeys)
			r.Get("/dev/pin-public-key", devH.PINPublicKey)
			r.Post("/dev/encrypt-pin", devH.EncryptPIN)
		}

		// Public auth endpoints
		r.Post("/auth/login/pin", authH.LoginPIN)
		r.Post("/auth/login/biometric", authH.BiometricLogin)
		r.Post("/auth/biometric/challenge", authH.BiometricChallenge)
		r.Post("/auth/token/refresh", authH.RefreshToken)

		// Onboarding — buka rekening
		r.Route("/onboarding", func(r chi.Router) {
			r.Use(middleware.OnboardingAudit)

			// Public endpoints for nasabah
			r.Post("/sessions", onboardingH.CreateSession)
			r.Get("/sessions/{session_id}", onboardingH.GetSession)
			r.Delete("/sessions/{session_id}", onboardingH.CancelSession)
			r.Post("/ocr", onboardingH.ProcessOCR)
			r.Get("/ocr/{session_id}", onboardingH.GetOCRResult)
			r.Post("/personal-data", onboardingH.SavePersonalData)
			r.Post("/verify-otp", onboardingH.VerifyOTP)
			r.Post("/resend-otp", onboardingH.ResendOTP)
			r.Post("/biometric", onboardingH.ProcessBiometric)
			r.Post("/video-call/queue", onboardingH.JoinVideoCallQueue)
			r.Get("/video-call/signal", signalingH.HandleSignaling)
			r.Get("/credentials/public-key", onboardingH.GetEncryptionPublicKey)
			r.Post("/credentials", onboardingH.SetCredentials)
			r.Post("/submit", onboardingH.Submit)

			// Internal endpoints (CS backend, monitoring)
			internalAPIKey := "dev-internal-key"
			if deps.Config != nil && deps.Config.InternalAPIKey != "" {
				internalAPIKey = deps.Config.InternalAPIKey
			}
			r.Group(func(r chi.Router) {
				r.Use(middleware.InternalAPIKey(internalAPIKey))
				r.Post("/video-call/result", onboardingH.SubmitVideoCallResult)
				r.Get("/sessions/{session_id}/audit", onboardingH.GetAuditTrail)
				r.Get("/monitoring", onboardingH.GetMonitoringStatus)
			})
		})

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

// appEnv defaults to development so tests that pass a nil Config keep the
// permissive local behaviour. Every real deployment sets APP_ENV explicitly.
func appEnv(cfg *config.Config) string {
	if cfg == nil || cfg.App.Env == "" {
		return "development"
	}
	return cfg.App.Env
}

// corsOrigins is empty by default: this is a mobile-first API and no browser
// origin is trusted unless CORS_ALLOWED_ORIGINS names it. A web frontend
// (or a tunnelled dev build) has to be listed explicitly.
func corsOrigins(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.CORSAllowedOrigins
}