package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
	"github.com/cshum/imagor/storage/filestorage"
	"go.uber.org/zap"
)

// Engine carries the source version each render used through to result storage.
type Engine struct {
	*imagor.Imagor
}

type sourceContextKey struct{}

type sourceResolver interface {
	resolve(context.Context, string) (*sourceObject, error)
}

// sourceRef is filled by the loader on a cache miss and read when the result is saved.
type sourceRef struct {
	mu     sync.Mutex
	source *sourceObject
}

func (r *sourceRef) set(source *sourceObject) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.source = source
}

func (r *sourceRef) get() *sourceObject {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source
}

// Do serves cached results without contacting GCS; resultStorage revalidates them afterwards.
func (e *Engine) Do(r *http.Request, p imagorpath.Params) (*imagor.Blob, error) {
	return e.Imagor.Do(r.WithContext(context.WithValue(r.Context(), sourceContextKey{}, &sourceRef{})), p)
}

func resultKey(_ *http.Request, p imagorpath.Params) string {
	if strings.HasPrefix(p.Image, "etu/") {
		return ""
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(imagorpath.GeneratePath(p))))
	return sum[:2] + "/" + sum
}

const versionSuffix = ".source"

// resultStorage keeps the source version beside each public result. After a hit,
// it checks GCS in the background and deletes the result if the source changed.
type resultStorage struct {
	*filestorage.FileStorage
	resolve  func(context.Context, string) (*sourceObject, error)
	logger   *zap.Logger
	checking sync.Map
	slots    chan struct{}
	wg       sync.WaitGroup
}

func newResultStorage(dir string, resolve func(context.Context, string) (*sourceObject, error), logger *zap.Logger) *resultStorage {
	// Bounded so a burst of hits cannot open a GCS connection per request.
	return &resultStorage{FileStorage: filestorage.New(dir), resolve: resolve, logger: logger, slots: make(chan struct{}, 8)}
}

func (s *resultStorage) Put(ctx context.Context, key string, blob *imagor.Blob) error {
	ref, _ := ctx.Value(sourceContextKey{}).(*sourceRef)
	source := ref.get()
	if source == nil {
		return errors.New("result has no source version")
	}
	// Written first, so a result on disk always has the version it was rendered from.
	if err := s.FileStorage.Put(ctx, key+versionSuffix, imagor.NewBlobFromBytes([]byte(source.version()))); err != nil {
		return err
	}
	return s.FileStorage.Put(ctx, key, blob)
}

func (s *resultStorage) Get(r *http.Request, key string) (*imagor.Blob, error) {
	blob, err := s.FileStorage.Get(r, key)
	if err == nil {
		s.revalidate(key)
	}
	return blob, err
}

func (s *resultStorage) revalidate(key string) {
	if _, busy := s.checking.LoadOrStore(key, struct{}{}); busy {
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		// A later hit checks it.
		s.checking.Delete(key)
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.checking.Delete(key)
		defer func() { <-s.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.check(ctx, key); err != nil {
			s.logger.Warn("cache revalidation failed", zap.String("key", key), zap.Error(err))
		}
	}()
}

func (s *resultStorage) check(ctx context.Context, key string) error {
	var recorded string
	blob, err := s.FileStorage.Get(nil, key+versionSuffix)
	if err == nil {
		var data []byte
		if data, err = blob.ReadAll(); err != nil {
			return err
		}
		recorded = string(data)
	} else if !errors.Is(err, imagor.ErrNotFound) {
		return err
	}
	current := ""
	if image, _, _ := strings.Cut(recorded, "\n"); image != "" {
		source, err := s.resolve(ctx, image)
		switch {
		case errors.Is(err, imagor.ErrNotFound):
		case err != nil:
			// Keep serving the result while GCS is unreachable.
			return err
		default:
			current = source.version()
		}
	}
	if current != "" && current == recorded {
		return nil
	}
	for _, name := range []string{key, key + versionSuffix} {
		if err := s.FileStorage.Delete(ctx, name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
