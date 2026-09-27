package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cshum/imagor/imagorpath"
)

func TestRequestContract(t *testing.T) {
	for _, tt := range []struct{ name, path, accept, image, contains string }{
		{"encoded object", "/photos/2026/a%20b%2Bc.jpg?w=320", "", "photos/2026/a b+c.jpg", "fit-in/320x0"},
		{"crop", "/wallpapers/a.jpg?w=319&ar=2:1&fit=crop", "", "wallpapers/a.jpg", "320x160/filters"},
		{"webp", "/photos/a.jpg", "image/avif,image/webp,*/*", "photos/a.jpg", "format(webp)"},
		{"webp disabled", "/photos/a.jpg", "image/webp;q=0,image/jpeg", "photos/a.jpg", "format(jpeg)"},
		{"gif", "/photos/a.gif", "image/webp", "photos/a.gif", "format(gif)"},
		{"svg", "/photos/a.svg", "image/webp", "photos/a.svg", "raw()"},
		{"quality", "/photos/a.jpg?q=82", "", "photos/a.jpg", "quality(80)"},
		{"smart", "/photos/a.jpg?fit=crop&crop=faces", "", "photos/a.jpg", "smart/"},
		{"focal", "/photos/a.jpg?crop=focalpoint&fp-x=0.25", "", "photos/a.jpg", "focal(0.25,0.5)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Request(u, tt.accept, "test-key", time.Unix(1000, 0))
			if err != nil {
				t.Fatal(err)
			}
			if p.Image != tt.image || !strings.Contains(imagorpath.GeneratePath(p), tt.contains) {
				t.Fatalf("unexpected params: %+v", p)
			}
		})
	}
}

func TestRejectUntrustedRequests(t *testing.T) {
	for _, path := range []string{
		"/private/secret", "/unsafe/fit-in/32x32/photos/a.jpg", "/photos/a%2F..%2Fsecret", "/photos/.hidden",
		"/photos/https:%2F%2Fevil.test/a", "/photos/a%252F..", "/photos/a%00.jpg", "/photos/a%5Cb.jpg", "/photos//a",
		"/photos/a?url=http://evil.test", "/photos/a?w=1&w=2", "/photos/a?w=4097", "/photos/a?w=-1",
		"/photos/a?w=1.5", "/photos/a?h=NaN", "/photos/a?q=Inf", "/photos/a?q=", "/photos/a?ar=0:1",
		"/photos/a?ar=1:999999", "/photos/a?fm=svg", "/photos/a?fit=stretch", "/photos/a?fp-x=NaN&crop=faces",
		"/photos/a?crop=bad", "/photos/a?fm=bad", "/photos/a?bad=%zz", "/etu/notes/id/image",
	} {
		t.Run(path, func(t *testing.T) {
			u, err := url.Parse(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Request(u, "", "test-key", time.Unix(1000, 0)); err == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
}

func TestEtuCapabilityCompatibility(t *testing.T) {
	// Same construction as etu-backend/internal/service/media.go.
	u := &url.URL{Path: "/etu/notes/id/a b+c.jpg"}
	expiry := "2000"
	mac := hmac.New(sha256.New, []byte("test-key"))
	mac.Write([]byte(u.EscapedPath() + "\n" + expiry))
	u.RawQuery = url.Values{"exp": {expiry}, "sig": {hex.EncodeToString(mac.Sum(nil))}, "w": {"320"}}.Encode()
	if _, err := Request(u, "", "test-key", time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("w", "800")
	u.RawQuery = q.Encode()
	if _, err := Request(u, "", "test-key", time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	for _, now := range []int64{2000, -90000} {
		if _, err := Request(u, "", "test-key", time.Unix(now, 0)); err == nil {
			t.Fatal("accepted invalid expiry")
		}
	}
	u.Path += "-other"
	if _, err := Request(u, "", "test-key", time.Unix(1000, 0)); err == nil {
		t.Fatal("accepted altered object")
	}
}
