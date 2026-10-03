// Command server is the variant service binary: one process serving the
// read path, and from later phases the tracking, admin and control-plane
// endpoints.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"variantsvc/internal/configcache"
	"variantsvc/internal/httpapi"
	"variantsvc/internal/store"
)

func main() {
	log := newLogger(env("LOG_LEVEL", "info"))

	port := env("PORT", "8080")
	refresh, err := time.ParseDuration(env("CONFIG_REFRESH_INTERVAL", "10s"))
	if err != nil || refresh <= 0 {
		log.Error("invalid CONFIG_REFRESH_INTERVAL", "value", os.Getenv("CONFIG_REFRESH_INTERVAL"))
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var source configcache.Source
	var db *store.Store
	switch {
	case os.Getenv("DATABASE_URL") != "":
		var err error
		if db, err = store.Open(ctx, os.Getenv("DATABASE_URL")); err != nil {
			// Startup is the one moment we insist on the database: without
			// a schema there is nothing to serve and nothing to fall back to.
			log.Error("database unavailable", "err", err)
			os.Exit(1)
		}
		defer db.Close()
		if err := db.Migrate(ctx); err != nil {
			log.Error("migrations failed", "err", err)
			os.Exit(1)
		}
		source = configcache.DBSource{Store: db}
		log.Info("config source", "type", "postgres")
	case os.Getenv("CONFIG_FILE") != "":
		source = configcache.FileSource{Path: os.Getenv("CONFIG_FILE")}
		log.Info("config source", "type", "file", "path", os.Getenv("CONFIG_FILE"))
	default:
		log.Error("no configuration source: set DATABASE_URL or CONFIG_FILE")
		os.Exit(2)
	}

	cache := &configcache.Cache{}
	// A failed first load is not fatal: the service starts with no snapshot,
	// /readyz reports it, and the refresher keeps trying.
	go cache.Run(ctx, source, refresh, log)

	snippetBase := env("CDN_BASE_URL", os.Getenv("PUBLIC_BASE_URL"))
	api := &httpapi.Server{
		Cache: cache, Log: log, SnippetBaseURL: snippetBase,
		Store: db, Source: source, PlatformKey: os.Getenv("PLATFORM_ADMIN_KEY"),
	}
	if db != nil && api.PlatformKey == "" {
		log.Warn("PLATFORM_ADMIN_KEY is not set: /v1/platform/* is disabled")
	}
	log.Info("snippet base url", "url", snippetBase)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
