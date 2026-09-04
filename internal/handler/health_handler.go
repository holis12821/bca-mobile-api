package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/holis12821/bca-mobile-api/internal/pkg/response"
)

type HealthHandler struct {
	db           *pgxpool.Pool
	redisSession *redis.Client
	redisCache   *redis.Client

	configMu     sync.RWMutex
	configCache  map[string]any
	configExpiry time.Time
}

func NewHealthHandler(db *pgxpool.Pool, redisSession, redisCache *redis.Client) *HealthHandler {
	return &HealthHandler{
		db:           db,
		redisSession: redisSession,
		redisCache:   redisCache,
	}
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
	h.configMu.RLock()
	if h.configCache != nil && time.Now().Before(h.configExpiry) {
		cached := h.configCache
		h.configMu.RUnlock()
		response.Success(w, r, http.StatusOK, cached)
		return
	}
	h.configMu.RUnlock()

	cfg := map[string]any{
		"maintenance_mode": false,
		"min_app_version":  "1.0.0",
		"feature_flags": map[string]bool{
			"biometric_login": true,
			"qris_payment":    true,
			"ewallet_topup":   true,
		},
	}

	h.configMu.Lock()
	h.configCache = cfg
	h.configExpiry = time.Now().Add(5 * time.Minute)
	h.configMu.Unlock()

	response.Success(w, r, http.StatusOK, cfg)
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

	h.configMu.RLock()
	var cfg map[string]any
	if h.configCache != nil && time.Now().Before(h.configExpiry) {
		cfg = h.configCache
	}
	h.configMu.RUnlock()

	if cfg == nil {
		cfg = map[string]any{
			"maintenance_mode": false,
			"min_app_version":  "1.0.0",
			"feature_flags": map[string]bool{
				"biometric_login": true,
				"qris_payment":    true,
				"ewallet_topup":   true,
			},
		}
		h.configMu.Lock()
		h.configCache = cfg
		h.configExpiry = time.Now().Add(5 * time.Minute)
		h.configMu.Unlock()
	}

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
