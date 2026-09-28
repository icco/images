package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

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

type sourceObject struct {
	key    string
	bucket string
	object *storage.ObjectHandle
	attrs  *storage.ObjectAttrs
}

func (l GCSLoader) resolve(ctx context.Context, key string) (*sourceObject, error) {
	image := key
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
	return &sourceObject{key: image, bucket: bucket, object: object.Generation(attrs.Generation), attrs: attrs}, nil
}

func (s *sourceObject) version() string {
	return fmt.Sprintf("%s\n%s\n%d\n%d", s.key, s.bucket, s.attrs.Generation, s.attrs.Metageneration)
}

// Get implements imagor.Loader without arbitrary URL or bucket access.
func (l GCSLoader) Get(r *http.Request, key string) (*imagor.Blob, error) {
	ctx := r.Context()
	source, err := l.resolve(ctx, key)
	if err != nil {
		return nil, err
	}
	if ref, ok := ctx.Value(sourceContextKey{}).(*sourceRef); ok {
		ref.set(source)
	}
	attrs := source.attrs
	blob := imagor.NewBlob(func() (io.ReadCloser, int64, error) {
		// Read the exact generation used for the public result cache key.
		reader, err := source.object.NewReader(ctx)
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
