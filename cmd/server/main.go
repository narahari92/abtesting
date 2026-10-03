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
)

func main() {
	log := newLogger(env("LOG_LEVEL", "info"))

	port := env("PORT", "8080")
	refresh, err := time.ParseDuration(env("CONFIG_REFRESH_INTERVAL", "10s"))
	if err != nil || refresh <= 0 {
		log.Error("invalid CONFIG_REFRESH_INTERVAL", "value", os.Getenv("CONFIG_REFRESH_INTERVAL"))
		os.Exit(2)
	}

	var source configcache.Source
	switch {
	case os.Getenv("CONFIG_FILE") != "":
		source = configcache.FileSource{Path: os.Getenv("CONFIG_FILE")}
		log.Info("config source", "type", "file", "path", os.Getenv("CONFIG_FILE"))
	default:
		log.Error("no configuration source: set CONFIG_FILE (DATABASE_URL support arrives in phase 2)")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cache := &configcache.Cache{}
	// A failed first load is not fatal: the service starts with no snapshot,
	// /readyz reports it, and the refresher keeps trying.
	go cache.Run(ctx, source, refresh, log)

	snippetBase := env("CDN_BASE_URL", os.Getenv("PUBLIC_BASE_URL"))
	api := &httpapi.Server{Cache: cache, Log: log, SnippetBaseURL: snippetBase}
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
