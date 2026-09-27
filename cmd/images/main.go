// Command images serves the image gateway with embedded Imagor and libvips.
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

	"cloud.google.com/go/storage"
	"github.com/icco/images/internal/engine"
	"github.com/icco/images/internal/gateway"
	"go.uber.org/zap"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:"+env("PORT", "8080")+"/healthz", nil)
		if err != nil {
			os.Exit(1)
		}
		res, err := client.Do(req)
		if err != nil {
			os.Exit(1)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("images stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	key := os.Getenv("MEDIA_SIGNING_KEY")
	if file := os.Getenv("MEDIA_SIGNING_KEY_FILE"); file != "" {
		data, err := os.ReadFile(file) // #nosec G304 G703 -- trusted operator configuration, never a request path.
		if err != nil {
			return err
		}
		key = strings.TrimSpace(string(data))
	}
	if key == "" {
		return errors.New("MEDIA_SIGNING_KEY or MEDIA_SIGNING_KEY_FILE is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client, err := storage.NewClient(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	logger, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }() // stderr may not support fsync.
	cache := env("CACHE_DIR", "/cache")
	app := engine.New(engine.GCSLoader{Client: client}, cache, logger)
	if err := app.Startup(ctx); err != nil {
		return err
	}
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			slog.Error("processor shutdown failed", "error", err)
		}
	}()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			if err := engine.Prune(ctx, cache, time.Now()); err != nil && ctx.Err() == nil {
				slog.Warn("cache cleanup failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	server := &http.Server{
		Addr: ":" + env("PORT", "8080"), Handler: gateway.Handler{Processor: app, MediaKey: key},
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	slog.Info("images listening", "address", server.Addr)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
