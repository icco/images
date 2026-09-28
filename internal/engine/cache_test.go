package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/storage"
	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
	"github.com/cshum/imagor/storage/filestorage"
	"github.com/icco/images/internal/gateway"
	"google.golang.org/api/option"
)

// Return a distinct blob so the test exercises result storage without libvips.
type copyProcessor struct{}

func (copyProcessor) Startup(context.Context) error  { return nil }
func (copyProcessor) Shutdown(context.Context) error { return nil }
func (copyProcessor) Process(_ context.Context, blob *imagor.Blob, _ imagorpath.Params, _ imagor.LoadFunc) (*imagor.Blob, error) {
	r, _, err := blob.NewReader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return imagor.NewBlobFromBytes(data), nil
}

type cacheSaveNotifier struct {
	imagor.Storage
	saved chan error
}

func (s cacheSaveNotifier) Put(ctx context.Context, key string, blob *imagor.Blob) error {
	err := s.Storage.Put(ctx, key, blob)
	s.saved <- err
	return err
}

func TestPublicSourceInvalidation(t *testing.T) {
	data := make(map[string][]byte)
	for i, c := range []color.RGBA{{R: 255, A: 255}, {B: 255, A: 255}} {
		im := image.NewRGBA(image.Rect(0, 0, 1, 1))
		im.Set(0, 0, c)
		var buf bytes.Buffer
		if err := png.Encode(&buf, im); err != nil {
			t.Fatal(err)
		}
		data[strconv.Itoa(i+1)] = buf.Bytes()
	}
	var mu sync.Mutex
	generation, metageneration := "1", "1"
	metadataStatus := http.StatusOK
	metadataCalls, downloads := 0, 0
	replaceDuringRead := false
	client, err := storage.NewClient(t.Context(), option.WithoutAuthentication(), storage.WithJSONReads(),
		option.WithHTTPClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			defer mu.Unlock()
			status := http.StatusOK
			var body []byte
			if r.URL.Query().Get("alt") == "media" {
				downloads++
				if replaceDuringRead {
					generation = "2"
					replaceDuringRead = false
				}
				body = data[r.URL.Query().Get("generation")]
				if body == nil {
					t.Error("source read was not pinned to a known generation")
					status = http.StatusNotFound
				}
			} else {
				metadataCalls++
				status = metadataStatus
				if status == http.StatusOK {
					var err error
					body, err = json.Marshal(map[string]string{
						"generation": generation, "metageneration": metageneration,
						"size": strconv.Itoa(len(data[generation])), "contentType": "image/png",
					})
					if err != nil {
						return nil, err
					}
				} else {
					body = []byte(`{"error":{"code":` + strconv.Itoa(status) + `,"message":"unavailable"}}`)
				}
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
		})}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cache := t.TempDir()
	saved := make(chan error, 10)
	bucket := "test-photos"
	// Each request gets a fresh engine to prove hits come from persistent storage,
	// rather than Imagor's in-flight request coalescing.
	get := func(path string) *httptest.ResponseRecorder {
		loader := GCSLoader{Client: client, Buckets: map[string]string{"photos": bucket, "etu": "test-media"}}
		app := imagor.New(func(app *imagor.Imagor) {
			app.Loaders = []imagor.Loader{loader}
			app.Processors = []imagor.Processor{copyProcessor{}}
			app.GetResultKey = resultKey
			app.ResultStorages = []imagor.Storage{cacheSaveNotifier{filestorage.New(cache), saved}}
		})
		h := gateway.Handler{Processor: &Engine{Imagor: app, loader: loader}, MediaKey: "test-key"}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		return w
	}
	check := func(path, wantGeneration string, wantDownload bool) {
		t.Helper()
		mu.Lock()
		beforeMetadata, beforeDownloads := metadataCalls, downloads
		mu.Unlock()
		w := get(path)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data[wantGeneration]) {
			t.Fatalf("response: %d %s", w.Code, w.Body.String())
		}
		if wantDownload {
			select {
			case err := <-saved:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("result not cached")
			}
		}
		mu.Lock()
		defer mu.Unlock()
		wantDownloads := 0
		if wantDownload {
			wantDownloads = 1
		}
		if metadataCalls-beforeMetadata != 1 || downloads-beforeDownloads != wantDownloads {
			t.Fatalf("metadata calls = %d, downloads = %d; want 1, %d", metadataCalls-beforeMetadata, downloads-beforeDownloads, wantDownloads)
		}
	}
	path := "/photos/a.png?w=32&fm=png"
	check(path, "1", true)
	check(path, "1", false)
	// A second transformation must have its own entry.
	other := strings.Replace(path, "32", "64", 1)
	check(other, "1", true)
	mu.Lock()
	generation = "2"
	mu.Unlock()
	check(path, "2", true)
	check(other, "2", true)
	check(path, "2", false)
	mu.Lock()
	metageneration = "2"
	mu.Unlock()
	check(path, "2", true)
	check(path, "2", false)
	bucket = "replacement-bucket"
	check(path, "2", true)
	// Private requests still fetch the source every time and never save results.
	privatePath := "/etu/notes/id/image"
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte("test-key"))
	mac.Write([]byte(privatePath + "\n" + exp))
	privatePath += "?exp=" + exp + "&sig=" + hex.EncodeToString(mac.Sum(nil))
	for range 2 {
		mu.Lock()
		beforeMetadata, beforeDownloads := metadataCalls, downloads
		mu.Unlock()
		w := get(privatePath)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("private response: %d %s", w.Code, w.Body.String())
		}
		mu.Lock()
		if metadataCalls-beforeMetadata != 1 || downloads-beforeDownloads != 1 {
			t.Error("private request did not load the source exactly once")
		}
		mu.Unlock()
		select {
		case <-saved:
			t.Fatal("private result cached")
		default:
		}
	}
	// Cached results must not hide deletion or denied metadata access.
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		mu.Lock()
		metadataStatus = status
		mu.Unlock()
		w := get(path)
		if w.Code == http.StatusOK || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("served stale result on metadata status %d: %d", status, w.Code)
		}
		if status == http.StatusNotFound && w.Code != http.StatusNotFound {
			t.Fatalf("deleted source: %d", w.Code)
		}
	}
	// A replacement between metadata lookup and download must not store the new
	// bytes under the old version's key. No timestamps are involved.
	mu.Lock()
	metadataStatus = http.StatusOK
	generation, metageneration = "1", "3"
	replaceDuringRead = true
	mu.Unlock()
	check(path, "1", true)
	check(path, "2", true)
	check(path, "2", false)
}
