# images

Go image gateway with embedded Imagor for images.natwelch.com.

The image service at **images.natwelch.com**. One Go binary embeds Imagor 1.9.6
and libvips, handles the public URL contract, and reads from three GCS sources.
There are no separate gateway/Imagor processes or upstream HTTP image loaders.

## URLs

| URL | Source object |
| --- | --- |
| `/photos/2026/example.jpg` | `gs://icco-cloud/photos/2026/example.jpg` |
| `/wallpapers/example.png` | `gs://iccowalls/example.png` |
| `/etu/notes/id/image?exp=…&sig=…` | `gs://etu-images/notes/id/image` |

Parameters: `w`, `h`, `q`, `fit=max|clip|crop`, `ar=2:1`,
`crop=entropy|faces|focalpoint|faces,focalpoint`, `fp-x`, `fp-y`,
`fm=jpeg|jpg|png|webp|avif|gif|svg`. Legacy `auto` is accepted.
Widths round up to a responsive size set (maximum 4096); quality rounds to five.
Default width is 2560. Output defaults to WebP when accepted, JPEG otherwise.
GIF animation and SVG originals are preserved. `faces` and `entropy` use libvips
attention-based smart cropping. AVIF is opt-in.

Etu capabilities use HMAC-SHA256 hex over **encoded URL pathname + newline + Unix
expiry in seconds**, using the same `MEDIA_SIGNING_KEY` as etu-backend. Expiries
must be in the future and no more than 24 hours away. Changing transformations
does not invalidate a capability. Etu responses are `private, no-store` and
never use source, result, or libvips preview caches. Audio is delivered by the
backend's GCS signed URLs.

Public transformed results persist in `/cache/result`, with a 30-day expiry and
daily cleanup inside the service. Originals are read directly from GCS. Cache
keys include the source and explicit output format. All sources share a bounded
queue: two concurrent transforms and 20 waiting requests. Requests time out
after 25 seconds; input limits are 100 MiB, 80 megapixels and 200 animation frames.

## Configuration

| Variable | Default / purpose |
| --- | --- |
| `GOOGLE_APPLICATION_CREDENTIALS` | GCP ADC credential file; deployed as `/creds.json` |
| `MEDIA_SIGNING_KEY` | Required shared Etu capability key |
| `MEDIA_SIGNING_KEY_FILE` | Optional key file, takes precedence over the environment value |
| `PORT` | `8080` |
| `CACHE_DIR` | `/cache` |

`GET /healthz` reports readiness after GCS client and libvips initialization.
It does not make billable GCS probes; verify actual objects to check IAM.
`images healthcheck` is the container health check. SIGTERM drains HTTP requests.

## Build and test

```sh
# Includes race tests, real libvips integration tests, and go vet.
docker build -t images .

# URL/capability contract tests do not need native libraries.
go test -race ./internal/gateway
```

Native builds require the libvips dependencies provided by the pinned Imagor base
image in `Dockerfile`. The runtime contains only this service, not an Imagor CLI.
Repository tooling comes from [icco/go-template](https://github.com/icco/go-template):
golangci-lint rules, CodeQL, coverage checks, PR-title checks, YAML formatting,
Dependabot, MIT licensing, and GoReleaser source releases. Test and lint jobs use
the native build image; run `docker build --target lint .` for the same lint checks.
The template's 80% coverage floor covers `internal/...` (gateway and engine);
command startup/shutdown wiring is built and vetted but excluded from the floor.
Releases run when a `v*` tag is pushed (starting with `v1.0.0`).
GitHub Actions tests and publishes `ghcr.io/icco/images:main` and a commit-SHA tag
on every push to main. No repository deployment secrets are needed: publishing
uses GitHub's per-workflow token.

## Deployment

Host configuration and bucket-scoped IAM live in `icco/icco.me`. The `images`
Compose service mounts `/home/nat/creds/images.json` and
`/docker/appdata/images:/cache`, and Caddy routes `images.natwelch.com` to port 8080.
The `mist-images` account has object-viewer access only on the three source
buckets. Its key is `mist-images-sa-key` in Secret Manager; mist's bootstrap
account retrieves it during `mist/update.sh`.

Run `ssh newyork /home/nat/Projects/icco.me/mist/update.sh` to deploy after the
image is published and infrastructure apply succeeds. `newyork` is the remote
SSH alias for mist. The shared media key is persisted in the host's mode-600
`mist/.env` and is used by both images and etu-backend. Never commit credentials.
Back up that file before rotating the key; rotation expires existing Etu URLs.

Check `/healthz`, a photo, wallpaper, and authorized Etu URL after deployment.
For rollback, pin the Compose image to a previously published commit-SHA tag.
