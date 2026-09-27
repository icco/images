package engine

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/cshum/imagor"
)

// GCSLoader reads exact object names from the gateway's three allowed sources.
type GCSLoader struct {
	Client  *storage.Client
	Buckets map[string]string
}

func source(key string) (string, string, error) {
	prefix, rest, ok := strings.Cut(key, "/")
	if !ok || rest == "" {
		return "", "", imagor.ErrInvalid
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") {
			return "", "", imagor.ErrInvalid
		}
	}
	switch prefix {
	case "photos":
		return prefix, key, nil
	case "wallpapers":
		return prefix, rest, nil
	case "etu":
		kind, _, ok := strings.Cut(rest, "/")
		if ok && (kind == "notes" || kind == "profiles" || kind == "audios") {
			return prefix, rest, nil
		}
	}
	return "", "", imagor.ErrInvalid
}

// Get implements imagor.Loader without arbitrary URL or bucket access.
func (l GCSLoader) Get(r *http.Request, key string) (*imagor.Blob, error) {
	ctx := r.Context()
	bucket, key, err := source(key)
	if err != nil {
		return nil, err
	}
	bucket = l.Buckets[bucket]
	if bucket == "" {
		return nil, imagor.ErrInvalid
	}
	object := l.Client.Bucket(bucket).Object(key)
	attrs, err := object.Attrs(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, imagor.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if attrs.Size > 100<<20 {
		return nil, imagor.ErrMaxSizeExceeded
	}
	// Read the exact object generation described by attrs.
	object = object.Generation(attrs.Generation)
	blob := imagor.NewBlob(func() (io.ReadCloser, int64, error) {
		reader, err := object.NewReader(ctx)
		if err != nil {
			return nil, 0, err
		}
		size := attrs.Size
		if attrs.ContentEncoding == "gzip" {
			size = 0
		}
		return reader, size, nil
	})
	blob.SetContentType(attrs.ContentType)
	return blob, blob.Err()
}

func pruneFiles(ctx context.Context, dir string, cutoff time.Time) error {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := root.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}
