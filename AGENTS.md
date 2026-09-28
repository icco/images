# AGENTS.md

One Go binary embeds Imagor and libvips to serve GCS images.

- `cmd/images`: configuration, lifecycle, health check.
- `internal/gateway`: URL validation, Etu capabilities, HTTP responses.
- `internal/engine`: GCS routing, Imagor/libvips setup, public result cache.
- Run `docker build -t images .` for race tests, native integration tests, vet,
  and build. Run `go test -race ./internal/gateway` without native dependencies.
- CI runs golangci-lint inside the imagor-base dev image. Locally, run `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run` after `brew install vips`.
- Preserve existing URL and Etu signing contracts. Never cache private Etu data.
- Keep documentation and defaults deployment-neutral; configure buckets at runtime.
- Use Conventional Commits with lowercase subjects. Never commit secrets.
