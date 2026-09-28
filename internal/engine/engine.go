// Package engine configures Imagor, native image processing, and source storage.
package engine

import (
	"path/filepath"
	"time"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/processor/vipsprocessor"
	"github.com/cshum/imagor/storage/filestorage"
	"go.uber.org/zap"
)

// New owns one Imagor queue and one libvips processor for all sources.
// Only transformed public images are persisted. Etu has neither a source nor
// result cache, so a deleted private object cannot be read from stale storage.
// Concurrency bounds simultaneous fetch-and-decode work; imagor rejects
// requests beyond the queue with 429, so it holds what drains in RequestTimeout.
func New(loader imagor.Loader, cacheDir string, concurrency int64, logger *zap.Logger) *Engine {
	app := imagor.New(func(app *imagor.Imagor) {
		app.Loaders = []imagor.Loader{loader}
		app.Processors = []imagor.Processor{vipsprocessor.NewProcessor(
			vipsprocessor.WithMaxResolution(80000000),
			vipsprocessor.WithMaxAnimationFrames(200),
			vipsprocessor.WithMaxFilterOps(10),
		)}
		if cacheDir != "" {
			app.ResultStorages = []imagor.Storage{filestorage.New(filepath.Join(cacheDir, "result"))}
		}
		app.GetResultKey = resultKey
		app.ProcessConcurrency = concurrency
		app.ProcessQueueSize = 16 * concurrency
		app.RequestTimeout = 25 * time.Second
		app.DisableParamsEndpoint = true
		app.Logger = logger
	})
	return &Engine{Imagor: app, loader: loader}
}
