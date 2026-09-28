# images

A Go image gateway for Google Cloud Storage, powered by Imagor and libvips.

## URLs

| URL | Bucket setting | Object key |
| --- | --- | --- |
| `/photos/2026/example.jpg` | `PHOTOS_BUCKET` | `photos/2026/example.jpg` |
| `/wallpapers/example.png` | `WALLPAPERS_BUCKET` | `example.png` |
| `/etu/notes/id/image?exp=…&sig=…` | `MEDIA_BUCKET` | `notes/id/image` |

Add parameters such as `?w=800&fit=crop&ar=2:1&fm=webp`:

- `w`, `h`, `q`: width defaults to 2560 and rounds up to a supported size (max 4096);
  quality defaults to 80 and rounds to multiples of five (minimum 5). No upscaling.
- `fit=max|clip|crop`, `ar`, `crop`, `fp-x`, `fp-y`: sizing and cropping.
  Smart crops use libvips attention, not face detection.
- `fm=jpeg|jpg|png|webp|avif|gif|svg`: output format. By default, `.gif` stays animated
  GIF and `.svg` passes through; other paths use WebP when accepted, otherwise JPEG.
  Legacy `auto` is ignored. See [parameter validation](internal/gateway/request.go).

Etu supports `notes/`, `profiles/`, and `audios/` paths but serves images only.
Sign URLs with `sig = hex(HMAC-SHA256(key, encoded pathname + "\n" + exp))`, where
`exp` is a Unix timestamp in seconds, in the future and at most 24 hours ahead.
The issuer shares the gateway's signing key; transformation parameters are unsigned.

## Caching

- **Public:** each request reaching the gateway checks GCS metadata. Disk cache keys
  include source versions and transformations; replacements invalidate results,
  and deletions or lookup failures return errors. Cache hits avoid downloads and
  processing. Entries expire after 30 days, with startup and daily cleanup.
- **HTTP:** public responses allow one day of caching plus seven days of
  stale-while-revalidate, so browsers may show older images until revalidation.
- **Exceptions:** Etu bypasses disk caching and returns `private, no-store`.
  SVG passthrough bypasses the disk result cache.

## Running

Use `ghcr.io/icco/images:main`. Set all three bucket variables above and provide
Google credentials with object-read access to those buckets.

| Variable | Purpose / default |
| --- | --- |
| `MEDIA_SIGNING_KEY` | Required unless a key file is supplied |
| `MEDIA_SIGNING_KEY_FILE` | Key file; overrides `MEDIA_SIGNING_KEY` |
| `GOOGLE_APPLICATION_CREDENTIALS` | Optional credentials file; otherwise uses Application Default Credentials |
| `PORT` | `8080` |
| `CACHE_DIR` | `/cache` |

Mount a cache volume writable by UID 1000 to persist results across restarts.
`GET /healthz` and `images healthcheck` check service readiness, not bucket access.

## Development

```sh
docker build -t images .             # Race tests, 80% coverage floor, vet, build
docker build --target lint .         # golangci-lint
go test -race ./internal/gateway     # No native dependencies required
```
