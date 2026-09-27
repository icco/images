# images

A Go image gateway that embeds Imagor 1.9.6 and libvips, reads Google Cloud
Storage objects, and caches public transformations.

## URLs

| URL | Bucket setting | Object key |
| --- | --- | --- |
| `/photos/2026/example.jpg` | `PHOTOS_BUCKET` | `photos/2026/example.jpg` |
| `/wallpapers/example.png` | `WALLPAPERS_BUCKET` | `example.png` |
| `/etu/notes/id/image?exp=…&sig=…` | `MEDIA_BUCKET` | `notes/id/image` |

Parameters: `w`, `h`, `q`, `fit=max|clip|crop`, `ar=2:1`,
`crop=entropy|faces|focalpoint|faces,focalpoint`, `fp-x`, `fp-y`, and
`fm=jpeg|jpg|png|webp|avif|gif|svg`. Legacy `auto` is accepted.
Width defaults to 2560 and rounds up to a responsive size (maximum 4096);
quality rounds to five. Output defaults to WebP when accepted, JPEG otherwise.
GIF animation and SVG originals are preserved. Smart crops use libvips attention,
not face detection. AVIF requires `fm=avif`.

Etu requires HMAC-SHA256 hex over **encoded pathname + newline + Unix expiry**,
using `MEDIA_SIGNING_KEY`, shared with the application issuing URLs. Expiry must be within the next 24 hours;
transformation parameters may change without resigning. Etu responses are
`private, no-store` and bypass disk caches. The gateway processes images only.

Public results expire after 30 days; the service cleans `/cache/result` daily.
Processing allows two concurrent transforms, 20 queued requests, and 25 seconds
per request. GCS objects over 100 MiB are rejected by stored size; libvips limits
decoding to 80 million pixels across frames and processing to 200 animation frames.

## Configuration

| Variable | Purpose / default |
| --- | --- |
| `PHOTOS_BUCKET` | Required photos bucket |
| `WALLPAPERS_BUCKET` | Required wallpapers bucket |
| `MEDIA_BUCKET` | Required private-media bucket |
| `GOOGLE_APPLICATION_CREDENTIALS` | Optional ADC file; otherwise uses default Google credentials |
| `MEDIA_SIGNING_KEY` | Required shared Etu key |
| `MEDIA_SIGNING_KEY_FILE` | Optional key file; overrides the environment value |
| `PORT` | `8080` |
| `CACHE_DIR` | `/cache` |

`GET /healthz` checks readiness after initialization, not GCS permissions.
`images healthcheck` runs the container probe. SIGTERM drains HTTP requests.

## Development

```sh
docker build -t images .             # Race tests, 80% coverage floor, vet, build
docker build --target lint .         # golangci-lint
go test -race ./internal/gateway     # No native dependencies required
```

Tooling follows [icco/go-template](https://github.com/icco/go-template). Docker
provides matching libvips build/runtime dependencies. Coverage measures
`internal/...`; command startup wiring is built and vetted. CI publishes
`ghcr.io/icco/images:main` and a commit-SHA tag using `GITHUB_TOKEN`.
Pushing a `v*` tag creates a source release; start with `v1.0.0`.

## Running

Supply the required environment variables and Google credentials with object-read
access to the configured buckets. Mount a writable volume at `CACHE_DIR` to retain
public results across restarts. The container runs as UID 1000 and listens on port
8080 by default. Keep the signing key stable; rotating it invalidates existing URLs.
