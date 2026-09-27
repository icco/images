// Package gateway validates public image requests and private media capabilities.
package gateway

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
)

// Processor is Imagor's typed request API; no internal HTTP listener is exposed.
type Processor interface {
	Do(*http.Request, imagorpath.Params) (*imagor.Blob, error)
}

// Handler serves the image URL contract and health endpoint.
type Handler struct {
	Processor Processor
	MediaKey  string
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain")
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, "images\n")
		}
		return
	}
	p, err := Request(r.URL, r.Header.Get("Accept"), h.MediaKey, time.Now())
	if err != nil {
		var e *requestError
		if errors.As(err, &e) {
			http.Error(w, e.message, e.status)
		} else {
			http.Error(w, "Invalid request", http.StatusBadRequest)
		}
		return
	}
	// Only the gateway calls Imagor.Do. Empty Params.Path uses Imagor's typed
	// API, so no internal URL signer or publicly reachable unsafe route is needed.
	inner := r.Clone(r.Context())
	inner.Header = make(http.Header)
	blob, err := h.Processor.Do(inner, p)
	if err != nil {
		status := imagor.WrapError(err).Code
		if status < 400 || status > 599 {
			status = 502
		}
		if status >= 500 {
			slog.Error("image processing failed", "source", strings.SplitN(p.Image, "/", 2)[0], "error", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	if blob == nil {
		http.Error(w, "Image not found", http.StatusNotFound)
		return
	}
	reader, size, err := blob.NewReader()
	if err != nil {
		http.Error(w, "Image unavailable", http.StatusBadGateway)
		return
	}
	defer reader.Close()
	w.Header().Set("Content-Type", blob.ContentType())
	w.Header().Set("Vary", "Accept")
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if !strings.HasPrefix(p.Image, "etu/") {
		w.Header().Set("Cache-Control", "public, max-age=86400, stale-while-revalidate=604800")
	}
	if r.Method != http.MethodHead {
		if _, err := io.Copy(w, reader); err != nil {
			slog.Warn("image response interrupted", "error", err)
		}
	}
}
