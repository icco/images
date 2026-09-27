// Package engine configures Imagor, native image processing, and source storage.
package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
	"github.com/cshum/imagor/processor/vipsprocessor"
	"github.com/cshum/imagor/storage/filestorage"
	"go.uber.org/zap"
)

// New owns one Imagor queue and one libvips processor for all sources.
// Only transformed public images are persisted. Etu has neither a source nor
// result cache, so a deleted private object cannot be read from stale storage.
func New(loader imagor.Loader, cacheDir string, logger *zap.Logger) *imagor.Imagor {
	return imagor.New(func(app *imagor.Imagor) {
		app.Loaders = []imagor.Loader{loader}
		app.Processors = []imagor.Processor{vipsprocessor.NewProcessor(
			vipsprocessor.WithMaxResolution(80000000),
			vipsprocessor.WithMaxAnimationFrames(200),
			vipsprocessor.WithMaxFilterOps(10),
		)}
		if cacheDir != "" {
			app.ResultStorages = []imagor.Storage{filestorage.New(filepath.Join(cacheDir, "result"), filestorage.WithExpiration(30*24*time.Hour))}
		}
		app.GetResultKey = resultKey
		app.ProcessConcurrency = 2
		app.ProcessQueueSize = 20
		app.RequestTimeout = 25 * time.Second
		app.DisableParamsEndpoint = true
		app.Logger = logger
	})
}

func resultKey(_ *http.Request, p imagorpath.Params) string {
	if strings.HasPrefix(p.Image, "etu/") {
		return ""
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(imagorpath.GeneratePath(p))))
	return sum[:2] + "/" + sum
}

// Prune removes expired public results, including entries never requested again.
// Called on startup and daily by the service, not by a host-specific cron job.
func Prune(ctx context.Context, cacheDir string, now time.Time) error {
	if cacheDir == "" {
		return nil
	}
	return pruneFiles(ctx, filepath.Join(cacheDir, "result"), now.Add(-30*24*time.Hour))
}
