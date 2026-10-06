package router

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/config"
	"github.com/holis12821/bca-mobile-api/internal/domain/account"
	"github.com/holis12821/bca-mobile-api/internal/domain/auth"
	"github.com/holis12821/bca-mobile-api/internal/domain/card"
	"github.com/holis12821/bca-mobile-api/internal/domain/content"
	"github.com/holis12821/bca-mobile-api/internal/domain/cs"
	"github.com/holis12821/bca-mobile-api/internal/domain/ewallet"
	"github.com/holis12821/bca-mobile-api/internal/domain/onboarding"
	"github.com/holis12821/bca-mobile-api/internal/domain/qris"
	"github.com/holis12821/bca-mobile-api/internal/domain/registration"
	"github.com/holis12821/bca-mobile-api/internal/domain/ticket"
	"github.com/holis12821/bca-mobile-api/internal/domain/transaction"
	"github.com/holis12821/bca-mobile-api/internal/handler"
	"github.com/holis12821/bca-mobile-api/internal/middleware"
	"github.com/holis12821/bca-mobile-api/internal/pkg/apperr"
	"github.com/holis12821/bca-mobile-api/internal/pkg/clientip"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/pkg/idempotency"
	"github.com/holis12821/bca-mobile-api/internal/pkg/metrics"
	"github.com/holis12821/bca-mobile-api/internal/pkg/notify"
	"github.com/holis12821/bca-mobile-api/internal/pkg/push"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
	"github.com/holis12821/bca-mobile-api/internal/pkg/sms"
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

// New builds the route tree. It returns an error only for wiring that must stop
// the process — today that is exactly one thing: FCM credentials that are present
// but unusable.
func New(deps Deps) (http.Handler, error) {
	r := chi.NewRouter()

	// Global middleware. RealIP runs first so every downstream consumer — audit
	// logs, rate limit keys, handlers — sees the same, vetted client address.
	ipResolver, badProxies := clientip.NewResolver(trustedProxies(deps.Config))
	if len(badProxies) > 0 {
		slog.Error("ignoring unparseable TRUSTED_PROXIES entries", "entries", badProxies)
	}
	r.Use(middleware.RealIP(ipResolver))
	r.Use(middleware.RequestID)
	r.Use(middleware.Recovery)
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.CORS(corsOrigins(deps.Config)))
	r.Use(middleware.Logging)
	// A JSON body has no business being unbounded, and nothing capped it: a
	// single request could ask the process to allocate whatever the client
	// cared to send. Multipart uploads set their own, larger, limit.
	r.Use(middleware.BodyLimit(clientConfig(deps.Config).MaxRequestBodyBytes))
	// Timeout middleware existed in the tree and was never mounted, so a slow
	// handler was bounded only by the server's write timeout.
	r.Use(middleware.Timeout(clientConfig(deps.Config).RequestTimeout))

	// devMode gates every development-only affordance in one place: the mock
	// OCR/Dukcapil/biometric/core-banking providers, the SMS gateway that logs
	// instead of sending when no provider is configured, and the otp_debug
	// field. Deriving it once here is
	// what stops a production deployment from inheriting any of them.
	devMode := appEnv(deps.Config) == "development"

	// Handlers
	healthH := handler.NewHealthHandler(deps.DB, deps.RedisSession, deps.RedisCache, clientConfig(deps.Config))

	// Auth dependencies
	rateLimiter := redisrepo.NewRateLimiter(deps.RedisSession)
	authRateLimiter := redisrepo.NewAuthRateLimiter(rateLimiter)
	lockoutMgr := redisrepo.NewLockoutManager(deps.RedisSession)
	nonceStore := redisrepo.NewNonceStore(deps.RedisSession)
	sessionCache := redisrepo.NewSessionCache(deps.RedisSession)

	userRepo := postgres.NewUserRepo(deps.DB)
	deviceRepo := postgres.NewDeviceRepo(deps.DB)

	// Notifications. Nothing in the codebase used to insert a row, so
	// GET /notifications was permanently empty and the unread badge
	// permanently 0. Every flow that has something to tell the nasabah now
	// writes through this, and pushes it when a provider is configured.
	notificationRepo := postgres.NewNotificationRepo(deps.DB)

	// The one place a pusher is chosen. Order matters, and so does the noise:
	// a production process running without a provider has to say so, because the
	// notification rows keep being written and everything downstream looks
	// healthy while no handset ever rings.
	var pusher notify.Pusher
	switch {
	case pushConfig(deps.Config).CredentialsFile != "":
		fcm, err := push.NewFCMPusher(deviceRepo, deviceRepo, push.FCMConfig{
			CredentialsFile: pushConfig(deps.Config).CredentialsFile,
			Timeout:         pushConfig(deps.Config).Timeout,
		})
		if err != nil {
			// Not a fallback to NoopPusher. Credentials that were configured and
			// are broken is a deployment mistake, and hiding it behind a working
			// boot is how it reaches production unnoticed.
			return nil, fmt.Errorf("push provider: %w", err)
		}
		pusher = fcm
	case devMode:
		pusher = push.NewLoggingPusher(deviceRepo)
	default:
		pusher = push.NewNoopPusher()
		slog.Warn("push notifications are not configured: FCM_CREDENTIALS_FILE is empty, " +
			"so notifications are stored in-app only and no device will be notified")
	}

	notifier := notify.New(notificationRepo, pusher)

	// The one place an SMS gateway is chosen, and the same rule as the pusher
	// above: configured-and-broken fails the boot. A process that answers 503
	// OTP_DELIVERY_FAILED for every nasabah is not a healthy process, and the
	// previous arrangement had no way to say so until someone read the logs.
	smsCfg := sms.ProviderConfig{
		Provider:         smsConfig(deps.Config).Provider,
		AccountSID:       smsConfig(deps.Config).AccountSID,
		AuthToken:        smsConfig(deps.Config).AuthToken,
		Sender:           smsConfig(deps.Config).Sender,
		BaseURL:          smsConfig(deps.Config).BaseURL,
		Timeout:          smsConfig(deps.Config).Timeout,
		VerifyServiceSID: smsConfig(deps.Config).VerifyServiceSID,
		VerifyBaseURL:    smsConfig(deps.Config).VerifyBaseURL,
		VerifyChannels:   smsConfig(deps.Config).VerifyChannels,
		VerifyLocale:     smsConfig(deps.Config).VerifyLocale,
		VerifyCodeTTL:    smsConfig(deps.Config).VerifyCodeTTL,
	}

	// otpVerifier is set instead of smsGateway when the provider owns the code.
	//
	// Only the onboarding flow can use it. Registration and the profile-OTP both
	// hand a code they generated to a Gateway, so they keep whatever gateway the
	// switch below picks — which on a Twilio trial means the mock, because the
	// Messages API refuses custom bodies there (572006).
	var otpVerifier onboarding.OTPVerifier
	var smsGateway sms.Gateway
	switch {
	case sms.IsVerifierProvider(smsCfg.Provider):
		v, err := sms.NewVerifier(smsCfg)
		if err != nil {
			return nil, fmt.Errorf("sms verifier: %w", err)
		}
		otpVerifier = v
		// The two flows that still need a Gateway get the development mock or a
		// refusal — never a silent no-op.
		if devMode {
			smsGateway = sms.NewMockGateway()
		} else {
			smsGateway = sms.NewUnconfiguredGateway()
			slog.Warn("SMS_PROVIDER is a verifier, so registration and profile OTP have no gateway: " +
				"those two flows will answer OTP_DELIVERY_FAILED")
		}
	case smsCfg.Provider != "":
		gw, err := sms.NewProvider(smsCfg)
		if err != nil {
			return nil, fmt.Errorf("sms provider: %w", err)
		}
		smsGateway = gw
	case devMode:
		// Logs the code instead of sending it; otp_debug carries it too.
		smsGateway = sms.NewMockGateway()
	default:
		// Unreachable with a validated config (Validate requires SMS_PROVIDER
		// outside development), and kept anyway: router.New is also built by
		// tests that pass a bare Deps.
		smsGateway = sms.NewUnconfiguredGateway()
		slog.Warn("sms is not configured: SMS_PROVIDER is empty, " +
			"so every OTP will be generated and never delivered")
	}

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
		Notifier:           notifier,
		AccessTTL:          accessTTL,
		RefreshTTL:         refreshTTL,
	})

	authH := handler.NewAuthHandler(authService)

	// Account & dashboard dependencies
	accountRepo := postgres.NewAccountRepo(deps.DB)
	profileRepo := postgres.NewProfileRepo(deps.DB, deps.PIIKey)
	limitRepo := postgres.NewTransactionLimitRepo(deps.DB)
	promotionRepo := postgres.NewPromotionRepo(deps.DB)

	versionCounter := redisrepo.NewVersionCounter(deps.RedisCache)
	profileCache := redisrepo.NewProfileCache(deps.RedisCache)
	balanceCache := redisrepo.NewBalanceCache(deps.RedisCache)
	dashboardCache := redisrepo.NewDashboardCache(deps.RedisCache)
	notifCache := redisrepo.NewNotificationCache(deps.RedisCache)
	profileOTPCache := redisrepo.NewProfileOTPCache(deps.RedisSession)

	accountService := account.NewService(account.ServiceConfig{
		Accounts:      accountRepo,
		Profiles:      profileRepo,
		Limits:        limitRepo,
		Notifications: notificationRepo,
		Promotions:    promotionRepo,
		ProfileCache:  profileCache,
		BalanceCache:  balanceCache,
		DashCache:     dashboardCache,
		NotifCache:    notifCache,
		ProfileOTP:    profileOTPCache,
		SMS:           smsGateway,
		Notifier:      notifier,
		Versions:      versionCounter,
		DevMode:       devMode,
	})

	accountH := handler.NewAccountHandler(accountService)
	notifH := handler.NewNotificationHandler(accountService)
	deviceH := handler.NewDeviceHandler(deviceRepo)

	// Kartu MILIK nasabah (migrasi 000021). Paket domain terpisah dari account:
	// kartu punya siklus hidup sendiri (blokir, penggantian, kedaluwarsa).
	//
	// accountCardRepo bukan cardRepo di bawah — yang itu membaca KATALOG
	// (card_products) untuk flow buka rekening.
	accountCardRepo := postgres.NewAccountCardRepo(deps.DB)
	accountCardService := card.NewService(card.ServiceConfig{
		Cards:  accountCardRepo,
		Writer: accountCardRepo,
	})
	accountCardH := handler.NewAccountCardHandler(accountCardService)

	// Konten statis (migrasi 000023): Pusat Bantuan dan Kontak CS.
	contentService := content.NewService(content.ServiceConfig{
		Repo:  postgres.NewContentRepo(deps.DB),
		Cache: redisrepo.NewContentCache(deps.RedisCache),
	})
	contentH := handler.NewContentHandler(contentService)

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
		Notifier:         notifier,
		Versions:         versionCounter,
	})

	// Wire PIN verify → verification token issuance, and give the account
	// service the consumer it needs to demand a CHANGE_LIMIT token before it
	// will raise a daily ceiling.
	authH.SetTransactionService(txnService)
	accountService.SetVerificationTokenConsumer(txnService)
	accountCardService.SetVerificationTokenConsumer(txnService)

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
		Notifier:         notifier,
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
		Notifier:         notifier,
		Versions:         versionCounter,
	})

	qrisH := handler.NewQRISHandler(qrisService)

	// Registration dependencies.
	//
	// The executor needs the same PII passphrase and lookup HMAC the onboarding
	// provisioner uses — it writes the same users row. Without them it cannot
	// encrypt the phone or compute phone_hash, so registration is refused
	// rather than writing a half-populated row.
	regCache := redisrepo.NewRegistrationCache(deps.RedisSession)

	var lookupHasher *crypto.HMACHasher
	if deps.Config != nil && deps.Config.Crypto.LookupHMACKey != "" {
		lookupHasher = crypto.NewHMACHasher(deps.Config.Crypto.LookupHMACKey)
	}
	regExecutor := postgres.NewRegistrationExecutor(deps.DB, hex.EncodeToString(deps.PIIKey), lookupHasher)

	regService := registration.NewService(registration.ServiceConfig{
		Cache:      regCache,
		Executor:   regExecutor,
		JWTManager: deps.JWTManager,
		PINKeys:    deps.PINKeys,
		SMS:        smsGateway,
		DevMode:    devMode,
	})

	// Onboarding dependencies
	onboardingSessionRepo := postgres.NewOnboardingSessionRepo(deps.DB)
	onboardingAuditRepo := postgres.NewOnboardingAuditRepo(deps.DB)
	onboardingOCRRepo := postgres.NewOnboardingOCRResultRepo(deps.DB)
	onboardingCache := redisrepo.NewOnboardingSessionCache(deps.RedisCache)
	ocrRateLimiter := redisrepo.NewOCRRateLimiter(deps.RedisSession)

	// Satu registry metrik per proses. Dibagi ke CardService (yang menaikkan
	// counter) dan MonitoringService (yang membacanya untuk alert) — dua
	// registry berarti alert membaca angka yang tidak pernah dinaikkan.
	cardMetrics := metrics.NewRegistry()

	// Pilih Jenis Kartu Paspor (docs/08-PILIH-KARTU-API-SPEC.md).
	//
	// Dibangun SEBELUM SessionService, bukan sesudah: SessionService memakainya
	// untuk menentukan langkah awal sesi. Ketika urutannya terbalik, service
	// sesi lahir tanpa katalog, card_type yang dikirim nasabah diabaikan diam-
	// diam, dan setiap sesi baru berhenti di CARD_SELECTION tanpa jalan keluar.
	cardRepo := postgres.NewCardRepo(deps.DB)
	cardIssuanceRepo := postgres.NewCardIssuanceRepo(deps.DB)
	cardCache := redisrepo.NewCardCatalogCache(deps.RedisCache)

	clientCfg := clientConfig(deps.Config)

	// Sakelar fitur dibaca saat request, bukan sekali saat start: §13 menyebut
	// flag ini sebagai jalan keluar saat sisipan bermasalah di produksi, dan
	// jalan keluar yang menuntut deploy ulang bukan jalan keluar. Nilai env
	// menjadi default; Redis menimpanya tanpa restart.
	featureFlags := redisrepo.NewFeatureFlags(deps.RedisSession, map[string]bool{
		redisrepo.FlagCardSelection: clientCfg.CardSelectionEnabled,
	})

	cardService := onboarding.NewCardService(onboarding.CardServiceConfig{
		Cards:        cardRepo,
		Cache:        cardCache,
		Sessions:     onboardingSessionRepo,
		SessionCache: onboardingCache,
		Audit:        onboardingAuditRepo,
		Flag:         featureFlags,
		Metrics:      cardMetrics,
	})

	// S&K dibangun SEBELUM session service: CreateSession memvalidasi
	// `accepted_tnc_version` lewat service ini, dan tanpa itu kolom tnc_version
	// kembali menjadi string bebas yang tidak bisa dibuktikan isinya.
	tncService := onboarding.NewTNCService(onboarding.TNCServiceConfig{
		Repo:  postgres.NewOnboardingTNCRepo(deps.DB),
		Cache: redisrepo.NewOnboardingTNCCache(deps.RedisCache),
	})

	onboardingVideoCallRepo := postgres.NewOnboardingVideoCallRepo(deps.DB)

	// Registry petugas CS. Interface-nya, bukan tipe konkretnya: tanpa database —
	// hanya terjadi di test perakitan rute — nilainya tetap nil sejati, dan AgentAuth
	// menolak setiap permintaan. Membangunnya dengan pool nil akan lolos kompilasi lalu
	// panic pada request pertama yang benar-benar sampai ke lookup.
	var csAgentLookup middleware.AgentLookup
	if deps.DB != nil {
		csAgentLookup = postgres.NewCSAgentRepo(deps.DB)
	}
	videoCallQueueCache := redisrepo.NewVideoCallQueueCache(deps.RedisSession)

	sigBaseURL := "ws://localhost:8080"
	if deps.Config != nil && deps.Config.SignalingBaseURL != "" {
		sigBaseURL = deps.Config.SignalingBaseURL
	}

	// Hub dibuat di sini, bukan di bawah bersama handler signaling: VideoCallService
	// memerlukannya sebagai Notifier, dan tanpa itu `agent_assigned` tidak pernah terkirim —
	// yang berarti panggilan tidak pernah bisa dimulai.
	sigHub := ws.NewHub()

	videoCallService := onboarding.NewVideoCallService(onboarding.VideoCallServiceConfig{
		Sessions:         onboardingSessionRepo,
		Cache:            onboardingCache,
		VideoCalls:       onboardingVideoCallRepo,
		Schedules:        postgres.NewOnboardingVideoCallScheduleRepo(deps.DB),
		QueueCache:       videoCallQueueCache,
		JWTManager:       deps.JWTManager,
		Audit:            onboardingAuditRepo,
		SignalingBaseURL: sigBaseURL,
		ICEServers:       iceServers(deps.Config),
		Notifier:         sigHub,
	})

	// Video call dibangun SEBELUM session service, dan urutannya mengikat:
	// CancelSession harus membatalkan panggilan yang masih hidup, jadi ia menerima
	// VideoCallService sebagai VideoCallCanceller. Dibalik, field itu nil dan panggilan
	// nasabah yang membatalkan sesinya tetap mengantre — menggeser posisi orang lain dan
	// tampil di daftar petugas sebagai panggilan yang bisa diambil.
	onboardingSessionService := onboarding.NewSessionService(onboarding.SessionServiceConfig{
		Sessions:         onboardingSessionRepo,
		Cache:            onboardingCache,
		Audit:            onboardingAuditRepo,
		Cards:            cardService,
		TNC:              tncService,
		LegacyAppVersion: clientCfg.CardLegacyAppVersion,
		VideoCalls:       videoCallService,
	})

	// External integrations, chosen once by environment. In development these
	// are the mocks; anywhere else they refuse with PROVIDER_NOT_CONFIGURED
	// instead of fabricating a verified identity.
	providers := onboarding.ProvidersFor(devMode)

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
		OCREngine:   providers.OCR,
		Dukcapil:    providers.Dukcapil,
		Storage:     providers.Storage,
		RateLimiter: ocrRateLimiter,
		AES:         piiAES,
		Audit:       onboardingAuditRepo,
	})

	onboardingPersonalDataRepo := postgres.NewOnboardingPersonalDataRepo(deps.DB)
	// The hasher keys the per-number OTP send budget. Same secret as
	// users.phone_hash, so the key is unguessable without it; nil only in the
	// bare wiring tests use, where the counter falls back to an unkeyed digest.
	onboardingOTPCache := redisrepo.NewOnboardingOTPCache(deps.RedisSession, lookupHasher)

	personalDataService := onboarding.NewPersonalDataService(onboarding.PersonalDataServiceConfig{
		Sessions:     onboardingSessionRepo,
		Cache:        onboardingCache,
		OCRResults:   onboardingOCRRepo,
		PersonalData: onboardingPersonalDataRepo,
		OTPCache:     onboardingOTPCache,
		SMS:          smsGateway,
		Verifier:     otpVerifier,
		DevMode:      devMode,
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
		Engine:      providers.Biometric,
		Storage:     providers.Storage,
		RateLimiter: bioRateLimiter,
		AES:         piiAES,
		Audit:       onboardingAuditRepo,
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

	// Provisioning turns a finished session into a real m-BCA user. It needs
	// both the PII key and the lookup HMAC secret; without them the service
	// still runs (local dev) but the nasabah gets no login — which is logged
	// loudly rather than silently.
	var provisioner onboarding.AccountProvisioner
	if len(deps.PIIKey) > 0 && deps.Config != nil && deps.Config.Crypto.LookupHMACKey != "" {
		provisioner = postgres.NewOnboardingProvisioner(
			deps.DB,
			hex.EncodeToString(deps.PIIKey),
			crypto.NewHMACHasher(deps.Config.Crypto.LookupHMACKey),
		)
	} else {
		slog.Warn("onboarding account provisioning disabled: AES_KEY and LOOKUP_HMAC_SECRET are both required")
	}

	submitService := onboarding.NewSubmitService(onboarding.SubmitServiceConfig{
		Sessions:     onboardingSessionRepo,
		Cache:        onboardingCache,
		PersonalData: onboardingPersonalDataRepo,
		Credentials:  onboardingCredentialRepo,
		CoreBanking:  providers.CoreBanking,
		Provisioner:  provisioner,
		Idempotency:  onboardingIdemCache,
		AES:          piiAES,
		Audit:        onboardingAuditRepo,
		CardFlag:     featureFlags,
		Cards:        cardService,
		CardIssuance: cardIssuanceRepo,
		CardCodes:    clientCfg.CardCoreBankingCode,
		// Repo yang sama dengan pembaca GET /account/cards: kartu yang baru
		// terbit harus muncul di layar Profil Saya tanpa menunggu seeder.
		AccountCards: accountCardRepo,
	})

	monitoringService := onboarding.NewMonitoringService(onboarding.MonitoringServiceConfig{
		Sessions:   onboardingSessionRepo,
		Audit:      onboardingAuditRepo,
		QueueCache: videoCallQueueCache,
		Metrics:    cardMetrics,

		// Hanya dipakai detail sesi sisi CS. piiAES yang sama dengan PersonalDataService:
		// data yang dienkripsi satu kunci tidak bisa dibuka kunci lain, dan dua sumber
		// kunci untuk kolom yang sama adalah cara termudah menghasilkan 500 permanen.
		PersonalData: onboardingPersonalDataRepo,
		VideoCalls:   onboardingVideoCallRepo,
		AES:          piiAES,
	})

	// The signaling token is single-use; the store is what remembers a spent
	// jti. It lives on the session Redis, the same instance that holds the
	// challenges and nonces those tokens are peers of.
	signalingTokens := redisrepo.NewSignalingTokenStore(deps.RedisSession)
	// videoCallService memenuhi SignalingLifecycle: socket nasabah yang baru tersambung
	// langsung menerima posisi antreannya.
	signalingH := handler.NewSignalingHandler(
		sigHub, deps.JWTManager, signalingTokens, videoCallService, corsOrigins(deps.Config),
	)

	// Layanan nasabah untuk petugas CS (pencarian + profil).
	//
	// deps.PIIKey, bukan piiAES: kolom PII di tabel users ditulis pgp_sym_encrypt di
	// sisi Postgres, sementara piiAES adalah cipher Go yang dipakai tabel onboarding.
	// Repository-nya yang mengubah kunci itu ke bentuk hex — lihat NewCSCustomerRepo.
	//
	// lookupHasher yang SAMA dengan jalur pendaftaran: phone_hash yang dihitung dengan
	// kunci HMAC berbeda tidak akan pernah cocok dengan baris mana pun.
	csService := cs.NewService(cs.ServiceConfig{
		Customers: postgres.NewCSCustomerRepo(deps.DB, deps.PIIKey),
		Access:    postgres.NewCSAccessLogRepo(deps.DB),
		Hasher:    lookupHasher,
	})
	csH := handler.NewCSHandler(csService)

	// Terminal, sesi petugas, dan jejak audit CS. Satu paket domain `cs`, konteks sama:
	// semuanya tentang apa yang dilakukan PETUGAS, bukan tentang nasabah.
	csTerminalRepo := postgres.NewCSTerminalRepo(deps.DB)
	csSessionRepo := postgres.NewCSAgentSessionRepo(deps.DB)
	csAuditRepo := postgres.NewCSAuditEventRepo(deps.DB)

	csSessionService := cs.NewAgentSessionService(cs.AgentSessionServiceConfig{
		Creds:     postgres.NewCSAgentCredentialRepo(deps.DB),
		Sessions:  csSessionRepo,
		Terminals: csTerminalRepo,
		Audit:     csAuditRepo,
		Hasher:    crypto.NewPasswordHasher(),
	})

	csTerminalService := cs.NewTerminalService(cs.TerminalServiceConfig{
		Terminals:   csTerminalRepo,
		Supervisors: postgres.NewCSSupervisorRepo(deps.DB),
		Sessions:    csSessionRepo,
		Audit:       csAuditRepo,
	})

	csAuthH := handler.NewCSAuthHandler(csSessionService)
	csTerminalH := handler.NewCSTerminalHandler(csTerminalService)

	ticketH := handler.NewTicketHandler(ticket.NewService(ticket.ServiceConfig{
		Repo: postgres.NewTicketRepo(deps.DB),
	}))

	cardH := handler.NewCardHandler(cardService, clientCfg.ProductUnderMaintenance)
	cardAdminH := handler.NewCardAdminHandler(
		onboarding.NewCardAdminService(cardRepo, cardCache))

	onboardingH := handler.NewOnboardingHandler(onboardingSessionService, ocrService, personalDataService, biometricService, videoCallService, credentialService, submitService, monitoringService, tncService, deps.PINKeys)

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

		// Public auth endpoints.
		//
		// Unauthenticated and, for login, expensive (Argon2id) — so they carry
		// an IP ceiling of their own on top of the per-device and per-user
		// limits the auth service applies. /auth/token/refresh had none at all.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(rateLimiter, authRateKey, authRateLimit, authRateWindow, false))

			r.Post("/auth/login/pin", authH.LoginPIN)
			r.Post("/auth/login/biometric", authH.BiometricLogin)
			// The spec documents GET with ?device_id=; the app was built
			// against POST with a JSON body. Both are served so neither
			// client sees a 405.
			r.Get("/auth/biometric/challenge", authH.BiometricChallenge)
			r.Post("/auth/biometric/challenge", authH.BiometricChallenge)
			r.Post("/auth/token/refresh", authH.RefreshToken)

			// The PIN public key has to be reachable before login, since every
			// PIN endpoint needs it first. Public by nature — it is the public
			// half — and the counterpart to assets/pin_public.pem in the APK,
			// which cannot be rotated without a release.
			r.Get("/auth/pin/public-key", authH.PINPublicKey)
		})

		// Konten statis. Tanpa Authorization dengan sengaja: tidak ada data
		// nasabah di dalamnya, dan nasabah yang justru terkunci di luar
		// aplikasi adalah yang paling butuh nomor Halo BCA.
		//
		// Tetap kena rate limit publik — dua endpoint tanpa token adalah dua
		// endpoint yang bisa dipakai membanjiri database.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(rateLimiter, publicContentRateKey, publicContentRateLimit, publicContentRateWindow, false))

			r.Get("/content/help-center", contentH.HelpCenter)
			r.Get("/content/contact-cs", contentH.ContactCS)
		})

		// Onboarding — buka rekening
		r.Route("/onboarding", func(r chi.Router) {
			r.Use(middleware.OnboardingAudit)

			// Public endpoints for nasabah.
			//
			// They are unauthenticated by nature (there is no user yet) and
			// several either return PII or cost money downstream — OCR, SMS,
			// Dukcapil. A per-IP ceiling keeps a scripted client from walking
			// session ids or draining the OTP gateway. Redis down → allow, so
			// an infrastructure hiccup does not block onboarding. The internal
			// group below is deliberately outside this limit: the CS backend is
			// one address making many legitimate calls.
			// Katalog kartu dibaca SEBELUM sesi dibuat (layar S&K), jadi tanpa
			// session_id dan tanpa Authorization.
			//
			// Sengaja DI LUAR grup ber-limit-IP di bawah. Batas IP itu ada untuk
			// mencegah penelusuran session_id dan pemerasan OTP; katalog yang
			// publik dan cacheable tidak membawa risiko itu. Kalau ikut di
			// dalamnya, jatah 60/5menit per IP akan mengalahkan 60/jam per
			// device dan satu kantor di belakang satu NAT saling menghabiskan
			// jatah — persis yang §14 hindari dengan membatasi per device.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RateLimit(rateLimiter, cardCatalogRateKey,
					cardCatalogRateLimit, cardCatalogRateWindow, false))
				// Per-IP di atasnya: batas per-device sendirian bisa dilewati
				// dengan memutar X-Device-Id, dan bersama region_code itu
				// berarti entri cache Redis tanpa batas. Lihat
				// cardCatalogIPRateLimit.
				r.Use(middleware.RateLimit(rateLimiter, cardCatalogIPRateKey,
					cardCatalogIPRateLimit, cardCatalogIPRateWindow, false))

				r.Get("/products/{product_type}/cards", cardH.GetCatalog)
			})

			// S&K dibaca pada layar PERTAMA buka rekening — sebelum sesi ada,
			// jadi tanpa session_id dan tanpa Authorization. Sama seperti
			// katalog kartu, ia sengaja DI LUAR grup ber-limit-IP di bawah:
			// batas itu menjaga penelusuran session_id dan pemerasan OTP, yang
			// tidak berlaku untuk teks publik dan cacheable.
			//
			// Jatahnya sendiri, bukan menumpang bucket katalog kartu. Flow
			// Android memuat S&K lalu katalog pada dua layar berurutan, jadi
			// bucket bersama berarti dua endpoint saling menghabiskan jatah dan
			// nasabah menerima 429 di tengah pendaftaran.
			r.Group(func(r chi.Router) {
				r.Use(middleware.RateLimit(rateLimiter, tncRateKey,
					tncRateLimit, tncRateWindow, false))

				r.Get("/tnc", onboardingH.GetTNC)
			})

			r.Group(func(r chi.Router) {
				r.Use(middleware.RateLimit(rateLimiter, onboardingRateKey, onboardingRateLimit, onboardingRateWindow, false))

				r.Post("/sessions", onboardingH.CreateSession)
				r.Get("/sessions/{session_id}", onboardingH.GetSession)
				r.Delete("/sessions/{session_id}", onboardingH.CancelSession)

				// Ganti kartu punya batasnya sendiri, per sesi (§14): 10 kali
				// cukup untuk nasabah yang ragu, dan menahan client yang
				// mengirim ulang tanpa henti. Batas per-IP grup ini tetap
				// berlaku di atasnya.
				r.With(middleware.RateLimit(rateLimiter, setCardRateKey,
					setCardRateLimit, setCardRateWindow, false)).
					Put("/sessions/{session_id}/card", cardH.SetCard)
				r.Post("/ocr", onboardingH.ProcessOCR)
				r.Get("/ocr/{session_id}", onboardingH.GetOCRResult)
				r.Post("/personal-data", onboardingH.SavePersonalData)
				r.Post("/verify-otp", onboardingH.VerifyOTP)
				r.Post("/resend-otp", onboardingH.ResendOTP)
				r.Post("/biometric", onboardingH.ProcessBiometric)
				r.Post("/video-call/queue", onboardingH.JoinVideoCallQueue)
				r.Get("/video-call/signal", signalingH.HandleSignaling)

				// Penjadwalan ulang. Di grup nasabah ber-rate-limit yang sama dengan
				// /video-call/queue, dan penjaganya deviceOwnsSession — bukan jalur CS.
				r.Post("/video-call/schedule", onboardingH.ScheduleVideoCall)
				r.Get("/video-call/schedule", onboardingH.GetVideoCallSchedule)
				r.Delete("/video-call/schedule", onboardingH.CancelVideoCallSchedule)
				r.Get("/credentials/public-key", onboardingH.GetEncryptionPublicKey)
				r.Post("/credentials", onboardingH.SetCredentials)
				r.Post("/submit", onboardingH.Submit)
			})

			// Internal endpoints (CS backend, monitoring). There is no default
			// key: an unset INTERNAL_API_KEY makes the middleware deny every
			// request, and config.Validate refuses to start a production
			// process without one.
			internalAPIKey := ""
			if deps.Config != nil {
				internalAPIKey = deps.Config.InternalAPIKey
			}
			r.Group(func(r chi.Router) {
				r.Use(middleware.InternalAPIKey(internalAPIKey))

				// Melihat antrean, jejak audit, dan kesehatan sistem adalah tindakan
				// sistem/pengawas, bukan tindakan satu petugas: tidak ada yang
				// diatribusikan ke seseorang, dan daftar antrean sengaja tidak memuat PII.
				r.Get("/video-call/queued", onboardingH.ListQueuedVideoCalls)
				r.Get("/sessions/{session_id}/audit", onboardingH.GetAuditTrail)
				r.Get("/monitoring", onboardingH.GetMonitoringStatus)

				// Mengambil panggilan dan memutuskan hasil verifikasi JELAS tindakan
				// seseorang: namanya tampil di layar nasabah dan keputusannya yang
				// membuka pembukaan rekening. Dua penjaga, dua pertanyaan — sistem mana
				// yang memanggil (InternalAPIKey) dan petugas mana yang bertindak
				// (AgentAuth). Urutannya bukan selera: Argon2 di AgentAuth mahal, dan
				// tidak boleh bisa dipicu lalu lintas yang belum lolos penjaga pertama.
				r.Group(func(r chi.Router) {
					r.Use(middleware.AgentAuth(csAgentLookup, middleware.ScopeVideoCall))
					r.Post("/video-call/agent-token", onboardingH.IssueAgentSignalingToken)
					r.Post("/video-call/result", onboardingH.SubmitVideoCallResult)
				})
			})
		})

		// Registration (public endpoints).
		//
		// These had no rate limit whatsoever, which made a six-digit OTP a
		// 10^6 guess space with nothing in the way. The per-attempt ceiling in
		// the service handles a single registration; this handles an address
		// walking many of them.
		r.Group(func(r chi.Router) {
			r.Use(middleware.RateLimit(rateLimiter, registrationRateKey, registrationRateLimit, registrationRateWindow, false))
			// /v1/registration/* and /v1/onboarding/* describe the same feature
			// with two different contracts. /v1/onboarding/* is the one the
			// Android app implements and the one that is maintained; this family
			// is kept working for older builds and says so on every response,
			// so a client integrating today can see which one to pick without
			// reading both specs (butir 8).
			r.Use(deprecated("/v1/onboarding/*", "docs/01-API-SPECIFICATION.md#9-registrasi-buka-rekening"))

			r.Post("/registration/initiate", regH.Initiate)
			r.Post("/registration/verify-otp", regH.VerifyOTP)
			r.Post("/registration/upload-document", regH.UploadDocument)
			r.Post("/registration/complete", regH.Complete)
		})

		// Protected endpoints (require valid access token)
		r.Group(func(r chi.Router) {
			// authService is the SessionValidator: steps 12-13 of §2.1 in
			// docs/04-SECURITY.md. Verifying the JWT signature used to be the
			// whole check, so a logged-out access token kept working — for
			// transfers included — until it expired.
			r.Use(middleware.Auth(deps.JWTManager, authService))

			// Auth
			r.Post("/auth/logout", authH.Logout)
			r.Post("/auth/pin/change", authH.ChangePIN)
			r.Post("/auth/biometric/register", authH.RegisterBiometric)
			r.Post("/auth/pin/verify", authH.PINVerify)
			// The kode akses (login) and the PIN (transactions) are separate
			// secrets. /auth/pin/change moves the second; this moves the first.
			r.Post("/auth/access-code/change", authH.ChangeAccessCode)

			// Account & Profile
			r.Get("/account/profile", accountH.Profile)
			r.Get("/account/balance", accountH.Balance)
			r.Get("/account/dashboard", accountH.Dashboard)
			r.Get("/account/transaction-limit", accountH.TransactionLimits)
			r.Put("/account/transaction-limit", accountH.UpdateTransactionLimit)

			// Kartu milik nasabah. Tidak ada parameter apa pun: user id diambil
			// dari access token, jadi tidak ada jalan meminta kartu orang lain.
			r.Get("/account/cards", accountCardH.List)

			// Perubahan pada satu kartu. card_id datang dari URL dan SELALU
			// dipasangkan dengan user id dari token di lapisan repository —
			// tanpa itu, sebuah UUID tebakan bisa mengubah kartu orang lain.
			r.Put("/account/cards/{card_id}/settings", accountCardH.UpdateSettings)
			r.Post("/account/cards/{card_id}/block", accountCardH.Block)
			r.Post("/account/cards/{card_id}/replacement", accountCardH.RequestReplacement)

			// Account update endpoints
			r.Post("/account/profile/otp", accountH.RequestProfileOTP)
			r.Put("/account/profile", accountH.UpdateProfile)
			r.Put("/account/settings", accountH.UpdateSettings)

			// Push notification registration — devices.push_token has existed
			// since migration 000001 with no endpoint to populate it.
			r.Post("/account/device/push-token", deviceH.RegisterPushToken)

			// Notifications
			r.Get("/notifications", notifH.ListNotifications)
			r.Put("/notifications/{id}/read", notifH.MarkNotificationRead)
			r.Put("/notifications/read-all", notifH.MarkAllNotificationsRead)

			// Transactions
			r.Get("/transactions/mutations", txnH.ListMutations)
			r.Get("/transactions/history", txnH.ListHistory)
			r.Get("/transfer/recent", txnH.ListRecentTransfers)
			r.Get("/transactions/{transaction_id}/receipt", txnH.GetReceipt)
			r.Get("/transactions/{transaction_id}/receipt/pdf", txnH.GetReceiptPDF)
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

	// Admin katalog kartu (§3 docs/08-PILIH-KARTU-API-SPEC.md).
	//
	// Di bawah /internal/v1, bukan /v1: rate limit, body limit, dan CORS jalur
	// nasabah tidak berlaku untuk jalur operator, dan menempelkannya di /v1
	// akan membuat satu operator yang menulis katalog berbagi jatah laju dengan
	// nasabah. Penjaganya X-Internal-API-Key, middleware yang sama dengan
	// endpoint CS onboarding — dan tanpa INTERNAL_API_KEY, semuanya menolak.
	//
	// Menulis katalog juga menuntut petugas ber-scope CARD_ADMIN. Sebelumnya kunci
	// sistem saja sudah cukup, yang berarti setiap petugas video call — yang memegang
	// kunci itu untuk tugasnya sendiri — bisa mengubah biaya dan limit kartu untuk
	// SELURUH nasabah, dan menaikkan catalog_version yang memaksa setiap aplikasi
	// memuat ulang katalognya. Kewenangan itu tidak pernah diberikan kepada mereka.
	r.Route("/internal/v1", func(r chi.Router) {
		internalAPIKey := ""
		if deps.Config != nil {
			internalAPIKey = deps.Config.InternalAPIKey
		}
		r.Use(middleware.InternalAPIKey(internalAPIKey))

		// Katalog kartu: CARD_ADMIN.
		r.Group(func(r chi.Router) {
			r.Use(middleware.AgentAuth(csAgentLookup, middleware.ScopeCardAdmin))

			r.Get("/cards", cardAdminH.ListCards)
			r.Put("/cards/{card_type}", cardAdminH.UpdateCard)
			r.Put("/products/{product_type}/cards/{card_type}", cardAdminH.UpdateProductCard)
		})

		// Identitas dan sesi petugas.
		//
		// Tiga penjaga berbeda di satu grup, dan bedanya disengaja:
		//   login     → kunci sistem saja. Di sinilah petugas MEMBUKTIKAN dirinya;
		//               menuntut kredensial petugas akan membuatnya harus sudah masuk
		//               untuk bisa masuk.
		//   me, password → AgentIdentity (kunci API petugas). `me` justru yang MEMBERI
		//               TAHU cakupan, jadi menuntut cakupan di sana memutar balik; dan
		//               `password` adalah jalur penyetelan PERTAMA, jadi pemanggilnya
		//               belum bisa punya sesi.
		//   logout    → AgentSession. Yang ditutup adalah sesi yang tokennya dikirim,
		//               bukan sesi yang disebut di body.
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", csAuthH.Login)

			r.Group(func(r chi.Router) {
				r.Use(middleware.AgentIdentity(csAgentLookup))
				r.Get("/me", csAuthH.Me)
				r.Post("/password", csAuthH.SetPassword)
			})

			r.With(middleware.AgentSession(csSessionService)).
				Post("/logout", csAuthH.Logout)
		})

		// Supervisor: daftar untuk layar SCR-006, dan otorisasi dual-control.
		r.Route("/supervisors", func(r chi.Router) {
			// Daftar supervisor dibuka SEBELUM otorisasi, jadi ia hanya butuh sesi —
			// bukan gerbang yang justru akan dilaluinya.
			r.With(middleware.AgentSession(csSessionService)).
				Get("/", csTerminalH.ListSupervisors)
			r.With(middleware.AgentSession(csSessionService)).
				Post("/authorize", csTerminalH.AuthorizeSupervisor)
		})

		// Terminal dan tiga gerbang kesiapan (Rule 3).
		//
		// Endpoint gerbang TIDAK memakai {terminal_id} di path: terminalnya ditentukan
		// SESI. Petugas tidak bisa menyatakan kesiapan atas loket yang bukan tempat ia
		// masuk, dan tidak bisa menanyakan kesiapan loket orang lain.
		r.Route("/terminals", func(r chi.Router) {
			// Pendaftaran terminal: butuh pendaftar yang bisa disebut namanya di audit.
			r.With(middleware.AgentIdentity(csAgentLookup)).
				Post("/", csTerminalH.RegisterTerminal)

			r.Group(func(r chi.Router) {
				r.Use(middleware.AgentSession(csSessionService))

				r.Get("/readiness", csTerminalH.Readiness)
				r.Post("/healthcheck", csTerminalH.Healthcheck)
				r.Post("/pii-ack", csTerminalH.AcknowledgePII)
				r.Post("/activate", csTerminalH.Activate)
				r.Post("/deactivate", csTerminalH.Deactivate)
			})

			// Didaftarkan TERAKHIR: chi mencocokkan pola statis lebih dulu, tapi
			// menaruhnya di atas akan membuat /readiness terbaca sebagai terminal_id
			// oleh siapa pun yang membaca berkas ini.
			r.Get("/{terminal_id}", csTerminalH.GetTerminal)
		})

		// Nasabah yang sudah punya rekening: pencarian dan profil.
		//
		// CUSTOMER_PII untuk keduanya, termasuk pencariannya — hasil pencarian sudah
		// memuat nama dan nomor HP tersamar, jadi ia pembukaan data juga, bukan sekadar
		// pencarian. Setiap permintaan di sini menulis baris cs_access_logs.
		r.Route("/customers", func(r chi.Router) {
			r.Use(middleware.AgentAuth(csAgentLookup, middleware.ScopeCustomerPII))

			r.Get("/", csH.SearchCustomers)
			r.Get("/{user_id}", csH.GetCustomerProfile)
		})

		// Tiket layanan.
		//
		// TICKET, bukan CUSTOMER_PII: tiket memuat keluhan nasabah, bukan NIK dan
		// alamatnya, dan petugas yang menangani tiket tidak otomatis perlu membuka data
		// pribadi. Tiket yang merujuk nasabah hanya menyimpan user_id — yang mau tahu
		// siapa dia memanggil /customers/{id} dengan cakupan yang berbeda, dan panggilan
		// itu tercatat tersendiri.
		r.Route("/tickets", func(r chi.Router) {
			r.Use(middleware.AgentAuth(csAgentLookup, middleware.ScopeTicket))

			r.Post("/", ticketH.CreateTicket)
			r.Get("/", ticketH.ListTickets)
			r.Get("/{ticket_number}", ticketH.GetTicket)
			r.Patch("/{ticket_number}", ticketH.UpdateTicket)
			r.Post("/{ticket_number}/notes", ticketH.AddNote)
		})

		// Pemantauan sesi onboarding.
		//
		// Di /internal/v1, bukan menumpang /v1/onboarding seperti endpoint CS yang lebih
		// tua: GET /v1/onboarding/sessions/{session_id} sudah menjadi milik nasabah, dan
		// yang lebih penting, jalur operator tidak semestinya berbagi rate limit, body
		// limit, dan CORS dengan jalur nasabah. Endpoint CS berikutnya menyusul ke sini.
		r.Route("/onboarding", func(r chi.Router) {
			// Daftar sesi tidak memuat PII, jadi cukup kewenangan petugas panggilan.
			r.With(middleware.AgentAuth(csAgentLookup, middleware.ScopeVideoCall)).
				Get("/sessions", onboardingH.ListSessionsForCS)

			// Detailnya memuat data pribadi, dan itu kewenangan tersendiri.
			r.With(middleware.AgentAuth(csAgentLookup, middleware.ScopeCustomerPII)).
				Get("/sessions/{session_id}", onboardingH.GetSessionDetailForCS)
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

	return r, nil
}

// pushConfig returns the push block, tolerating a nil Config the way the other
// accessors in this file do — router_test builds Deps with almost nothing set.
func pushConfig(cfg *config.Config) config.Push {
	if cfg == nil {
		return config.Push{}
	}
	return cfg.Push
}

// smsConfig returns the SMS block, tolerating a nil Config the same way.
func smsConfig(cfg *config.Config) config.SMS {
	if cfg == nil {
		return config.SMS{}
	}
	return cfg.SMS
}

// appEnv defaults to development so tests that pass a nil Config keep the
// permissive local behaviour. Every real deployment sets APP_ENV explicitly.
// iceServers builds the WebRTC ICE list from the environment.
//
// Empty is a valid, and currently expected, answer: no TURN credentials have
// been issued yet (butir 7). Returning an empty list rather than a fabricated
// server means the app can tell "not configured" from "configured and broken" —
// and nothing here is a mock, so there is no environment gate to apply.
func iceServers(cfg *config.Config) []onboarding.ICEServer {
	if cfg == nil {
		return nil
	}

	var servers []onboarding.ICEServer
	if urls := nonEmpty(cfg.WebRTC.STUNURLs); len(urls) > 0 {
		servers = append(servers, onboarding.ICEServer{URLs: urls})
	}
	if urls := nonEmpty(cfg.WebRTC.TURNURLs); len(urls) > 0 {
		// A TURN server without credentials is a TURN server that refuses every
		// allocation. Saying so at startup beats debugging it from a nasabah's
		// failed video call.
		if cfg.WebRTC.TURNUsername == "" || cfg.WebRTC.TURNCredential == "" {
			slog.Error("TURN_URLS set without TURN_USERNAME/TURN_CREDENTIAL: ignoring TURN servers")
		} else {
			servers = append(servers, onboarding.ICEServer{
				URLs:       urls,
				Username:   cfg.WebRTC.TURNUsername,
				Credential: cfg.WebRTC.TURNCredential,
			})
		}
	}

	if len(servers) == 0 && appEnv(cfg) != "development" {
		slog.Warn("no ICE servers configured: video call will fail behind strict NAT")
	}

	return servers
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// deprecated marks a route family as superseded. RFC 8594 Sunset is deliberately
// NOT sent: no removal date has been agreed, and inventing one here would be a
// promise this repository cannot keep.
func deprecated(successor, reference string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Deprecation", "true")
			w.Header().Set("Link", `<`+successor+`>; rel="successor-version"`)
			w.Header().Set("X-API-Deprecation-Info", reference)
			next.ServeHTTP(w, r)
		})
	}
}

func appEnv(cfg *config.Config) string {
	if cfg == nil || cfg.App.Env == "" {
		return "development"
	}
	return cfg.App.Env
}

// Onboarding rate limit: generous enough for a real nasabah working through
// the flow (photo retries, OTP resends), tight enough that enumerating session
// ids or farming OTPs from one address is not practical.
const (
	onboardingRateLimit  = 60
	onboardingRateWindow = 5 * time.Minute
)

// Auth endpoints get a per-IP ceiling on top of the per-device and per-user
// limits the auth service applies. Fail-open on a Redis error: those inner
// limits are fail-closed already, so an infrastructure hiccup should not also
// take login offline.
const (
	authRateLimit  = 30
	authRateWindow = 15 * time.Minute
)

func authRateKey(r *http.Request) string {
	return "rate:auth:ip:" + middleware.ClientIP(r)
}

// Registration is unauthenticated, costs an SMS, and hands out a six-digit
// code. One address should not be able to farm those.
const (
	registrationRateLimit  = 20
	registrationRateWindow = 15 * time.Minute

	// Konten statis: longgar, karena layar Bantuan bisa dibuka berkali-kali
	// dan jawabannya sama untuk semua orang. Yang dibatasi di sini bukan
	// penyalahgunaan data, melainkan beban database dari alamat yang sama.
	publicContentRateLimit  = 60
	publicContentRateWindow = 1 * time.Minute
)

func publicContentRateKey(r *http.Request) string {
	return "rate:content:ip:" + middleware.ClientIP(r)
}

func registrationRateKey(r *http.Request) string {
	return "rate:registration:ip:" + middleware.ClientIP(r)
}

// clientConfig returns the app-facing switches with safe fallbacks, so a nil
// Config (tests) still yields usable body and timeout limits.
func clientConfig(cfg *config.Config) config.Client {
	if cfg != nil && cfg.Client.MaxRequestBodyBytes > 0 && cfg.Client.RequestTimeout > 0 {
		return cfg.Client
	}

	c := config.Client{}
	if cfg != nil {
		c = cfg.Client
	}
	if c.MinAppVersion == "" {
		c.MinAppVersion = "1.0.0"
	}
	if c.MaxRequestBodyBytes <= 0 {
		c.MaxRequestBodyBytes = 1 << 20
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 30 * time.Second
	}
	return c
}

// Katalog kartu: 60/jam per device (§14). Responsnya cacheable dan ber-ETag,
// jadi ini penjaga terhadap klien yang berulah, bukan pembatas pemakaian wajar.
const (
	cardCatalogRateLimit  = 60
	cardCatalogRateWindow = time.Hour
)

// Plafon per-IP di atas batas per-device untuk katalog kartu.
//
// cardCatalogRateKey membatasi per X-Device-Id, dan header itu dipilih client:
// memutarnya setiap permintaan melewati batas 60/jam sepenuhnya. Itu penting di
// endpoint ini karena setiap permintaan dengan region_code yang belum pernah
// dipakai menulis satu entri cache Redis berisi katalog utuh (TTL 15 menit) dan
// satu query database di belakangnya. Plafon ini yang membatasi jumlah entri
// yang bisa hidup bersamaan dari satu alamat.
//
// Angkanya longgar dengan sengaja: pemakai m-BCA berbagi alamat di belakang NAT
// operator, jadi batas per-IP yang ketat akan memutus nasabah yang wajar. 600/jam
// menahan satu penyalahguna tanpa menyentuh satu jaringan penuh nasabah.
const (
	cardCatalogIPRateLimit  = 600
	cardCatalogIPRateWindow = time.Hour
)

func cardCatalogIPRateKey(r *http.Request) string {
	return "rate:cards:ip:" + middleware.ClientIP(r)
}

// Ganti kartu: 10 per sesi (§14).
const (
	setCardRateLimit  = 10
	setCardRateWindow = time.Hour
)

// setCardRateKey membatasi per session_id di path. Sesi adalah satuan yang
// benar di sini: satu perangkat boleh punya beberapa draf, dan membatasi per
// device akan membuat draf kedua kehabisan jatah karena draf pertama.
func setCardRateKey(r *http.Request) string {
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		return "rate:cards:set:ip:" + middleware.ClientIP(r)
	}
	return "rate:cards:set:session:" + sessionID
}

// S&K: 30 per 5 menit. Layarnya dibuka sekali per pendaftaran dan isinya
// di-cache 5 menit di client, jadi angka ini longgar untuk pemakaian wajar
// sekaligus menahan client yang mengulang tanpa henti.
const (
	tncRateLimit  = 30
	tncRateWindow = 5 * time.Minute
)

// tncRateKey membatasi per X-Device-Id, dengan jatuh ke IP bila headernya tidak
// ada. Berbeda dari katalog kartu, GetTNC TIDAK mewajibkan header itu: teks S&K
// sama untuk semua perangkat, dan menolak 400 di layar pertama buka rekening
// hanya menambah satu cara gagal tanpa menjaga apa pun. Cabang IP di bawah
// karena itu adalah jalur normal untuk client yang tidak mengirimnya, bukan
// kasus tepi.
func tncRateKey(r *http.Request) string {
	if deviceID := strings.TrimSpace(r.Header.Get("X-Device-Id")); deviceID != "" {
		return "rate:tnc:device:" + deviceID
	}
	return "rate:tnc:ip:" + middleware.ClientIP(r)
}

// cardCatalogRateKey membatasi per X-Device-Id. Belum ada sesi maupun token di
// titik ini, dan membatasi per IP akan membuat satu jaringan kantor berbagi
// satu jatah.
//
// Permintaan tanpa header itu ditolak handler dengan 400, tapi rate limiter
// berjalan lebih dulu. Tanpa cabang di bawah semuanya masuk ke satu bucket
// berakhiran kosong, dan begitu bucket itu habis, permintaan yang sekadar lupa
// memasang header menerima 429 alih-alih 400 yang menjelaskan masalahnya.
func cardCatalogRateKey(r *http.Request) string {
	if deviceID := strings.TrimSpace(r.Header.Get("X-Device-Id")); deviceID != "" {
		return "rate:cards:device:" + deviceID
	}
	return "rate:cards:noheader:ip:" + middleware.ClientIP(r)
}

// onboardingRateKey buckets by client IP — there is no user identity yet at
// this point in the flow. The "rate:" prefix follows the convention the auth
// limiter already uses (rate:login:ip:…), so every throttle bucket is one scan
// away in Redis.
func onboardingRateKey(r *http.Request) string {
	return "rate:onboarding:ip:" + middleware.ClientIP(r)
}

// trustedProxies lists the proxy CIDRs allowed to set forwarding headers.
func trustedProxies(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.TrustedProxies
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
