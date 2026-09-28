package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
)

// Engine resolves public source versions before consulting Imagor's result cache.
type Engine struct {
	*imagor.Imagor
	loader imagor.Loader
}

type sourceContextKey struct{}

type sourceResolver interface {
	resolve(context.Context, string) (*sourceObject, error)
}

// Do checks current GCS metadata even on cache hits. The same snapshot pins both
// the result key and source read, including replacements during processing.
func (e *Engine) Do(r *http.Request, p imagorpath.Params) (*imagor.Blob, error) {
	if resolver, ok := e.loader.(sourceResolver); ok && !strings.HasPrefix(p.Image, "etu/") {
		ctx := r.Context()
		cancel := func() {}
		if e.LoadTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, e.LoadTimeout)
		}
		source, err := resolver.resolve(ctx, p.Image)
		cancel()
		if err != nil {
			return nil, err
		}
		r = r.WithContext(context.WithValue(r.Context(), sourceContextKey{}, source))
	}
	return e.Imagor.Do(r, p)
}

func resultKey(r *http.Request, p imagorpath.Params) string {
	if strings.HasPrefix(p.Image, "etu/") {
		return ""
	}
	key := imagorpath.GeneratePath(p)
	if source, ok := r.Context().Value(sourceContextKey{}).(*sourceObject); ok {
		key += fmt.Sprintf("\n%s\n%d\n%d", source.bucket, source.attrs.Generation, source.attrs.Metageneration)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
	return sum[:2] + "/" + sum
}
