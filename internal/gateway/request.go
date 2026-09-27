package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cshum/imagor/imagorpath"
)

var widths = []int{16, 32, 48, 64, 96, 128, 200, 256, 300, 320, 384, 480, 640, 750, 768, 800, 828, 960, 1080, 1200, 1280, 1600, 1920, 2048, 2560, 3840, 4096}
var aspectRatio = regexp.MustCompile(`^\d+(\.\d+)?:\d+(\.\d+)?$`)
var expiryPattern = regexp.MustCompile(`^\d+$`)

type requestError struct {
	status  int
	message string
}

func (e *requestError) Error() string { return e.message }

func invalid(message string) error { return &requestError{http.StatusBadRequest, message} }

// Request validates the public URL before constructing any Imagor parameters.
// Image contains a decoded logical source path, never a user-selected URL/bucket.
func Request(u *url.URL, accept, mediaKey string, now time.Time) (imagorpath.Params, error) {
	var out imagorpath.Params
	path := u.Path
	if !utf8.ValidString(path) || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, `\%?#:`) || strings.ContainsFunc(path, unicode.IsControl) {
		return out, invalid("Invalid path")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") {
			return out, invalid("Invalid path")
		}
	}
	if len(parts) < 2 || !slices.Contains([]string{"photos", "wallpapers", "etu"}, parts[0]) {
		return out, &requestError{http.StatusNotFound, "Unknown source"}
	}
	p, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return out, invalid("Invalid query")
	}
	for key, values := range p {
		if !slices.Contains([]string{"w", "h", "q", "fit", "crop", "ar", "fp-x", "fp-y", "auto", "fm", "exp", "sig"}, key) {
			return out, invalid("Unsupported parameter: " + key)
		}
		if key != "auto" && len(values) != 1 {
			return out, invalid("Duplicate parameter: " + key)
		}
	}
	if parts[0] == "etu" {
		if len(parts) < 3 || !slices.Contains([]string{"notes", "profiles", "audios"}, parts[1]) {
			return out, &requestError{http.StatusNotFound, "Unknown media path"}
		}
		expiry := p.Get("exp")
		exp, err := strconv.ParseInt(expiry, 10, 64)
		if mediaKey == "" || err != nil || !expiryPattern.MatchString(expiry) || exp <= now.Unix() || exp > now.Unix()+86400 {
			return out, &requestError{http.StatusForbidden, "Expired or missing media capability"}
		}
		mac := hmac.New(sha256.New, []byte(mediaKey))
		mac.Write([]byte(u.EscapedPath() + "\n" + expiry))
		sig, err := hex.DecodeString(p.Get("sig"))
		if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
			return out, &requestError{http.StatusForbidden, "Invalid media capability"}
		}
	}
	// Validate even ignored numeric options so malformed requests cannot hide
	// behind smart-crop or aspect-ratio overrides.
	values := map[string]float64{}
	for _, spec := range []struct {
		key                string
		fallback, min, max float64
	}{
		{"w", 2560, 1, 4096}, {"h", 0, 0, 4096}, {"q", 80, 1, 100},
		{"fp-x", .5, 0, 1}, {"fp-y", .5, 0, 1},
	} {
		n := spec.fallback
		if p.Has(spec.key) {
			n, err = strconv.ParseFloat(p.Get(spec.key), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < spec.min || n > spec.max {
				return out, invalid("Invalid " + spec.key)
			}
		}
		values[spec.key] = n
	}
	if math.Trunc(values["w"]) != values["w"] || math.Trunc(values["h"]) != values["h"] {
		return out, invalid("Dimensions must be integers")
	}
	for _, w := range widths {
		if w >= int(values["w"]) {
			out.Width = w
			break
		}
	}
	out.Height = int(values["h"])
	if ar := p.Get("ar"); ar != "" {
		if !aspectRatio.MatchString(ar) {
			return out, invalid("Invalid aspect ratio")
		}
		pair := strings.Split(ar, ":")
		a, _ := strconv.ParseFloat(pair[0], 64)
		b, _ := strconv.ParseFloat(pair[1], 64)
		h := math.Round(float64(out.Width) * b / a)
		if a <= 0 || b <= 0 || math.IsNaN(h) || math.IsInf(h, 0) || h < 1 || h > 4096 {
			return out, invalid("Invalid aspect ratio")
		}
		out.Height = int(h)
	}
	fit := p.Get("fit")
	if !slices.Contains([]string{"", "max", "clip", "crop"}, fit) {
		return out, invalid("Unsupported fit")
	}
	out.FitIn = fit != "crop"
	crop := p.Get("crop")
	if !slices.Contains([]string{"", "entropy", "faces", "focalpoint", "faces,focalpoint"}, crop) {
		return out, invalid("Unsupported crop")
	}
	smart := strings.Contains(crop, "faces") || crop == "entropy"
	out.Smart = smart && fit == "crop"
	quality := max(5, int(math.Round(values["q"]/5))*5)
	out.Filters = imagorpath.Filters{{Name: "quality", Args: strconv.Itoa(quality)}, {Name: "no_upscale"}, {Name: "strip_exif"}, {Name: "strip_icc"}}
	if !smart && (p.Has("fp-x") || p.Has("fp-y")) {
		out.Filters = append(out.Filters, imagorpath.Filter{Name: "focal", Args: fmt.Sprintf("%g,%g", values["fp-x"], values["fp-y"])})
	}
	format := p.Get("fm")
	if !slices.Contains([]string{"", "jpg", "jpeg", "png", "webp", "avif", "gif", "svg"}, format) {
		return out, invalid("Unsupported format")
	}
	svg := strings.HasSuffix(strings.ToLower(path), ".svg")
	if format == "" {
		switch {
		case svg:
			format = "svg"
		case strings.HasSuffix(strings.ToLower(path), ".gif"):
			format = "gif"
		case acceptsWebP(accept):
			format = "webp"
		default:
			format = "jpeg"
		}
	}
	if format == "jpg" {
		format = "jpeg"
	}
	if format == "svg" {
		if !svg {
			return out, invalid("SVG output requires an SVG source")
		}
		out.Filters = append(out.Filters, imagorpath.Filter{Name: "raw"})
	} else {
		out.Filters = append(out.Filters, imagorpath.Filter{Name: "format", Args: format})
	}
	out.Image = strings.Join(parts, "/")
	return out, nil
}

func acceptsWebP(accept string) bool {
	for _, item := range strings.Split(strings.ToLower(accept), ",") {
		parts := strings.Split(item, ";")
		if strings.TrimSpace(parts[0]) != "image/webp" {
			continue
		}
		q := 1.0
		for _, part := range parts[1:] {
			if value, ok := strings.CutPrefix(strings.TrimSpace(part), "q="); ok {
				var err error
				q, err = strconv.ParseFloat(value, 64)
				if err != nil {
					q = 0
				}
			}
		}
		if q > 0 && q <= 1 {
			return true
		}
	}
	return false
}
