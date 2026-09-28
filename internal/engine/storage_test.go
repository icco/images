package engine

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGCSLoader(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                       string
		metadataStatus, readStatus int
		size                       int64
		encoding                   string
		wantError                  bool
	}{
		{"normal", 200, 200, int64(data.Len()), "", false},
		{"gzip metadata", 200, 200, 1, "gzip", false},
		{"missing", 404, 200, 1, "", true},
		{"denied", 403, 200, 1, "", true},
		{"too large", 200, 200, 101 << 20, "", true},
		{"read denied", 200, 403, int64(data.Len()), "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			client, err := storage.NewClient(ctx, option.WithoutAuthentication(), storage.WithJSONReads(),
				option.WithHTTPClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					status := tt.metadataStatus
					body := `{"error":{"code":403,"message":"test error"}}`
					if !strings.Contains(r.URL.Path, "/b/test-photos/o/photos/a b+猫.png") {
						t.Errorf("unexpected object path: %s", r.URL.Path)
					}
					if r.URL.Query().Get("alt") == "media" {
						status = tt.readStatus
						if r.URL.Query().Get("generation") != "7" {
							t.Error("read not pinned to generation")
						}
						if status == 200 {
							body = data.String()
						}
					} else if status == 200 {
						metadata, marshalErr := json.Marshal(map[string]string{"bucket": "test-photos", "name": "photos/a b+猫.png", "generation": "7", "size": strconv.FormatInt(tt.size, 10), "contentType": "image/png", "contentEncoding": tt.encoding})
						if marshalErr != nil {
							return nil, marshalErr
						}
						body = string(metadata)
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			loader := GCSLoader{Client: client, Buckets: map[string]string{"photos": "test-photos"}}
			r := httptest.NewRequestWithContext(ctx, "GET", "/", nil)
			blob, err := loader.Get(r, "photos/a b+猫.png")
			if err == nil {
				var reader io.ReadCloser
				reader, _, err = blob.NewReader()
				if err == nil {
					var got []byte
					got, err = io.ReadAll(reader)
					_ = reader.Close()
					if !tt.wantError && !bytes.Equal(got, data.Bytes()) {
						t.Error("source data changed")
					}
				}
			}
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, want error = %v", err, tt.wantError)
			}
			if _, err := loader.Get(r, "other/secret"); err == nil {
				t.Fatal("accepted unknown source")
			}
			if _, err := loader.Get(r, "wallpapers/a.png"); err == nil {
				t.Fatal("accepted unconfigured bucket")
			}
		})
	}
}
