package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/holis12821/bca-mobile-api/internal/config"
	"github.com/holis12821/bca-mobile-api/internal/pkg/crypto"
	"github.com/holis12821/bca-mobile-api/internal/repository/postgres"
	redisrepo "github.com/holis12821/bca-mobile-api/internal/repository/redis"
	"github.com/holis12821/bca-mobile-api/internal/router"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	loadEnvFile(".env")

	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Postgres
	pool, err := postgres.NewPool(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	// Redis (two instances)
	rdb, err := redisrepo.NewClients(ctx, cfg.RedisSession, cfg.RedisCache)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	defer rdb.Close()

	// PIN RSA keys
	pinKeys, err := crypto.LoadRSAKeyPair(cfg.PIN.PrivateKeyPath, cfg.PIN.PublicKeyPath)
	if err != nil {
		return fmt.Errorf("load pin keys: %w", err)
	}

	// JWT RSA keys
	jwtKeys, err := crypto.LoadRSAKeyPair(cfg.JWT.PrivateKeyPath, cfg.JWT.PublicKeyPath)
	if err != nil {
		return fmt.Errorf("load jwt keys: %w", err)
	}
	jwtMgr := crypto.NewJWTManager(jwtKeys, cfg.JWT.AccessTokenTTL, cfg.JWT.RefreshTokenTTL)

	// Router
	handler := router.New(router.Deps{
		DB:           pool,
		RedisSession: rdb.Session,
		RedisCache:   rdb.Cache,
		PINKeys:      pinKeys,
		JWTManager:   jwtMgr,
		Config:       cfg,
	})

	// HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.App.Host, cfg.App.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  cfg.App.ReadTimeout,
		WriteTimeout: cfg.App.WriteTimeout,
		IdleTimeout:  cfg.App.IdleTimeout,
	}

	// Graceful shutdown
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server starting", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		slog.Info("shutting down", "signal", sig.String())
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(ctx, cfg.App.ShutdownTimeout)
	defer cancel()

	return srv.Shutdown(shutdownCtx)
}