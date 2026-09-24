package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/config"
	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type HealthHandler struct {
	db           *pgxpool.Pool
	redisSession *redis.Client
	redisCache   *redis.Client

	// client holds the app-facing switches. They used to be string literals
	// inside this handler, so maintenance_mode and min_app_version were
	// permanently false and "1.0.0" — the force-update gate could not be
	// operated at all.
	client config.Client

	configMu     sync.RWMutex
	configCache  map[string]any
	configExpiry time.Time
}

func NewHealthHandler(db *pgxpool.Pool, redisSession, redisCache *redis.Client, client config.Client) *HealthHandler {
	if client.MinAppVersion == "" {
		client.MinAppVersion = "1.0.0"
	}
	return &HealthHandler{
		db:           db,
		redisSession: redisSession,
		redisCache:   redisCache,
		client:       client,
	}
}

// clientConfig builds the app-facing config payload, cached for 5 minutes.
func (h *HealthHandler) clientConfig() map[string]any {
	h.configMu.RLock()
	if h.configCache != nil && time.Now().Before(h.configExpiry) {
		cached := h.configCache
		h.configMu.RUnlock()
		return cached
	}
	h.configMu.RUnlock()

	cfg := map[string]any{
		"maintenance_mode": h.client.MaintenanceMode,
		"min_app_version":  h.client.MinAppVersion,
		"feature_flags": map[string]bool{
			"biometric_login": h.client.FeatureBiometricLogin,
			"qris_payment":    h.client.FeatureQRISPayment,
			"ewallet_topup":   h.client.FeatureEWalletTopUp,
			"onboarding":      h.client.FeatureOnboarding,
		},
	}

	h.configMu.Lock()
	h.configCache = cfg
	h.configExpiry = time.Now().Add(5 * time.Minute)
	h.configMu.Unlock()

	return cfg
}

// Liveness — NOT cached. Pings DB + both Redis instances, timeout 1s.
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
	defer cancel()

	dbStatus := "ok"
	if err := h.db.Ping(ctx); err != nil {
		dbStatus = "error"
	}

	redisSessionStatus := "ok"
	if err := h.redisSession.Ping(ctx).Err(); err != nil {
		redisSessionStatus = "error"
	}

	redisCacheStatus := "ok"
	if err := h.redisCache.Ping(ctx).Err(); err != nil {
		redisCacheStatus = "error"
	}

	status := http.StatusOK
	if dbStatus != "ok" || redisSessionStatus != "ok" {
		status = http.StatusServiceUnavailable
	}

	response.Success(w, r, status, map[string]any{
		"database":      dbStatus,
		"redis_session": redisSessionStatus,
		"redis_cache":   redisCacheStatus,
	})
}

// Config — cached 5 minutes.
func (h *HealthHandler) Config(w http.ResponseWriter, r *http.Request) {
	response.Success(w, r, http.StatusOK, h.clientConfig())
}

// Health — combined endpoint: liveness data + config data.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
	defer cancel()

	dbStatus := "ok"
	if err := h.db.Ping(ctx); err != nil {
		dbStatus = "error"
	}

	redisSessionStatus := "ok"
	if err := h.redisSession.Ping(ctx).Err(); err != nil {
		redisSessionStatus = "error"
	}

	redisCacheStatus := "ok"
	if err := h.redisCache.Ping(ctx).Err(); err != nil {
		redisCacheStatus = "error"
	}

	cfg := h.clientConfig()

	status := http.StatusOK
	if dbStatus != "ok" || redisSessionStatus != "ok" {
		status = http.StatusServiceUnavailable
	}

	response.Success(w, r, status, map[string]any{
		"database":      dbStatus,
		"redis_session": redisSessionStatus,
		"redis_cache":   redisCacheStatus,
		"config":        cfg,
	})
}
