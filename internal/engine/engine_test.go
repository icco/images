package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cshum/imagor"
	"github.com/icco/images/internal/gateway"
	"go.uber.org/zap"
)

type memoryLoader struct {
	mu    sync.Mutex
	data  map[string][]byte
	calls int
}

func (l *memoryLoader) Get(_ *http.Request, key string) (*imagor.Blob, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	data, ok := l.data[key]
	if !ok {
		return nil, imagor.ErrNotFound
	}
	return imagor.NewBlobFromBytes(data), nil
}

type notifyingStorage struct {
	imagor.Storage
	saved chan struct{}
}

func (s notifyingStorage) Put(ctx context.Context, key string, blob *imagor.Blob) error {
	err := s.Storage.Put(ctx, key, blob)
	if err == nil {
		s.saved <- struct{}{}
	}
	return err
}

func TestNativeProcessingAndPrivateDeletion(t *testing.T) {
	var pngData, gifData bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := range 32 {
		for x := range 64 {
			im.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 8), A: 255})
		}
	}
	if err := png.Encode(&pngData, im); err != nil {
		t.Fatal(err)
	}
	frame := image.NewPaletted(im.Bounds(), color.Palette{color.Black, color.White})
	second := image.NewPaletted(im.Bounds(), frame.Palette)
	for i := range second.Pix {
		second.Pix[i] = 1
	}
	if err := gif.EncodeAll(&gifData, &gif.GIF{Image: []*image.Paletted{frame, second}, Delay: []int{10, 20}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="32"><rect width="64" height="32" fill="red"/></svg>`)
	l := &memoryLoader{data: map[string][]byte{"photos/a b+.png": pngData.Bytes(), "wallpapers/a.png": pngData.Bytes(), "photos/a.gif": gifData.Bytes(), "photos/a.svg": svg, "etu/notes/id/a.png": pngData.Bytes()}}
	cache := t.TempDir()
	app := New(l, cache, 2, zap.NewNop())
	saved := make(chan struct{}, 20)
	app.ResultStorages[0] = notifyingStorage{app.ResultStorages[0], saved}
	if err := app.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	h := gateway.Handler{Processor: app, MediaKey: "test-key"}
	get := func(path, accept string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(t.Context(), "GET", path, nil)
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tt := range []struct{ path, accept, contentType string }{
		{"/photos/a%20b+.png?w=32&fm=png", "", "image/png"},
		{"/wallpapers/a.png?w=32", "image/webp", "image/webp"},
		{"/wallpapers/a.png?w=32", "", "image/jpeg"},
		{"/photos/a.gif?w=32", "image/webp", "image/gif"},
		{"/photos/a.svg?w=32", "", "image/svg+xml"},
	} {
		w := get(tt.path, tt.accept)
		if w.Code != 200 || w.Header().Get("Content-Type") != tt.contentType {
			t.Fatalf("%s: %d %s %s", tt.path, w.Code, w.Header(), w.Body.String())
		}
		if tt.contentType == "image/png" {
			out, err := png.Decode(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			if out.Bounds().Dx() != 32 || out.Bounds().Dy() != 16 {
				t.Fatalf("wrong dimensions: %v", out.Bounds())
			}
		}
		if tt.contentType == "image/gif" {
			out, err := gif.DecodeAll(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Image) != 2 {
				t.Fatal("lost animation")
			}
		}
		if tt.contentType == "image/svg+xml" {
			if !bytes.Equal(w.Body.Bytes(), svg) {
				t.Fatal("SVG changed")
			}
			continue
		}
		select {
		case <-saved:
		case <-time.After(5 * time.Second):
			t.Fatal("result not cached")
		}
		l.mu.Lock()
		calls := l.calls
		l.mu.Unlock()
		if w := get(tt.path, tt.accept); w.Code != 200 {
			t.Fatalf("cache hit: %d", w.Code)
		}
		l.mu.Lock()
		after := l.calls
		l.mu.Unlock()
		if after != calls {
			t.Fatal("public cache missed")
		}
	}
	u := &url.URL{Path: "/etu/notes/id/a.png"}
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte("test-key"))
	mac.Write([]byte(u.EscapedPath() + "\n" + exp))
	u.RawQuery = url.Values{"exp": {exp}, "sig": {hex.EncodeToString(mac.Sum(nil))}, "w": {"32"}}.Encode()
	w := get(u.String(), "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("private response: %d %s", w.Code, w.Body.String())
	}
	l.mu.Lock()
	delete(l.data, "etu/notes/id/a.png")
	l.mu.Unlock()
	if w := get(u.String(), ""); w.Code != 404 {
		t.Fatalf("deleted private image still served: %d", w.Code)
	}
	select {
	case <-saved:
		t.Fatal("private result written to cache")
	default:
	}
}

type blockingLoader struct {
	release <-chan struct{}
	data    []byte
}

func (l blockingLoader) Get(r *http.Request, _ string) (*imagor.Blob, error) {
	select {
	case <-l.release:
		return imagor.NewBlobFromBytes(l.data), nil
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
}

func TestBurstIsQueuedNotRejected(t *testing.T) {
	const concurrency = 2
	release := make(chan struct{})
	app := New(blockingLoader{release: release, data: []byte("image")}, "", concurrency, zap.NewNop())
	// libvips cannot restart after another test's Shutdown, and the queue is independent of it.
	app.Processors = []imagor.Processor{copyProcessor{}}
	if err := app.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	h := gateway.Handler{Processor: app, MediaKey: "test-key"}
	const burst = 16 * concurrency
	codes := make(chan int, burst)
	for i := range burst {
		go func() {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/wallpapers/"+strconv.Itoa(i)+".png?w=32", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	select {
	case code := <-codes:
		t.Fatalf("request finished before any source loaded: %d", code)
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	for range burst {
		if code := <-codes; code != 200 {
			t.Fatalf("burst request: %d", code)
		}
	}
}

func TestSourceMapping(t *testing.T) {
	for _, tt := range []struct{ path, bucket, object string }{
		{"photos/2026/a b+猫.jpg", "photos", "photos/2026/a b+猫.jpg"},
		{"wallpapers/a.jpg", "wallpapers", "a.jpg"},
		{"etu/profiles/user/avatar", "etu", "profiles/user/avatar"},
	} {
		bucket, object, err := source(tt.path)
		if err != nil || bucket != tt.bucket || object != tt.object {
			t.Fatalf("%s: %s %s %v", tt.path, bucket, object, err)
		}
	}
	for _, path := range []string{"other-bucket/secret", "https://example.com/a", "photos/../secret", "etu/other/a", "etu/notes"} {
		if _, _, err := source(path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	result := filepath.Join(dir, "result")
	if err := os.MkdirAll(result, 0750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old", "new"} {
		if err := os.WriteFile(filepath.Join(result, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(result, "old"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Prune(context.Background(), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	var files []string
	err := filepath.WalkDir(result, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(files, ",") != "new" {
		t.Fatalf("remaining files: %v", files)
	}
}
