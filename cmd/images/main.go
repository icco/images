// Command images serves the image gateway with embedded Imagor and libvips.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/storage"
	"github.com/icco/images/internal/engine"
	"github.com/icco/images/internal/gateway"
	"go.uber.org/zap"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx)
	stop()
	if err != nil {
		slog.Error("images stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		return healthcheck(ctx)
	}
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
	buckets := make(map[string]string, 3)
	for source, variable := range map[string]string{
		"photos": "PHOTOS_BUCKET", "wallpapers": "WALLPAPERS_BUCKET", "etu": "MEDIA_BUCKET",
	} {
		bucket := os.Getenv(variable)
		if bucket == "" {
			return fmt.Errorf("%s is required", variable)
		}
		buckets[source] = bucket
	}
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
	// Slots also wait on GCS downloads, so allow more than one per CPU.
	concurrency := int64(4 * runtime.GOMAXPROCS(0))
	if value := os.Getenv("PROCESS_CONCURRENCY"); value != "" {
		concurrency, err = strconv.ParseInt(value, 10, 64)
		if err != nil || concurrency < 1 {
			return fmt.Errorf("PROCESS_CONCURRENCY must be a positive integer: %q", value)
		}
	}
	cache := env("CACHE_DIR", "/cache")
	app := engine.New(engine.GCSLoader{Client: client, Buckets: buckets}, cache, concurrency, logger)
	if err := app.Startup(ctx); err != nil {
		return err
	}
	defer func() {
		if err := app.Shutdown(context.WithoutCancel(ctx)); err != nil {
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
		Addr:              ":" + env("PORT", "8080"),
		Handler:           gateway.Handler{Processor: app, MediaKey: key},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	slog.Info("images listening", "address", server.Addr)
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		return nil
	}
}

func healthcheck(ctx context.Context) error {
	client := http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+env("PORT", "8080")+"/healthz", nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s", res.Status)
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
