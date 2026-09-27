package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
)

type fakeProcessor struct {
	calls int
	err   error
}

func (p *fakeProcessor) Do(r *http.Request, _ imagorpath.Params) (*imagor.Blob, error) {
	p.calls++
	if len(r.Header) != 0 {
		panic("client headers reached Imagor")
	}
	return imagor.NewBlobFromBytes([]byte("<svg/>")), p.err
}

func TestHTTPBoundary(t *testing.T) {
	p := &fakeProcessor{}
	h := Handler{Processor: p, MediaKey: "test-key"}
	for _, tt := range []struct {
		method, path  string
		status, calls int
	}{
		{"GET", "/healthz", 200, 0},
		{"POST", "/photos/a.jpg", 405, 0},
		{"GET", "/unsafe/a.jpg", 404, 0},
		{"GET", "/etu/notes/id/a.jpg", 403, 0},
		{"GET", "/photos/a.svg", 200, 1},
		{"HEAD", "/photos/a.svg", 200, 2},
	} {
		r := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
		r.Header.Set("Imagor-Raw", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tt.status || p.calls != tt.calls {
			t.Fatalf("%s %s: status %d, calls %d", tt.method, tt.path, w.Code, p.calls)
		}
		if tt.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
		if w.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
			t.Fatal("missing SVG policy")
		}
	}
	p.err = imagor.ErrNotFound
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", "/photos/missing.jpg", nil))
	if w.Code != 404 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("cached error")
	}
}
