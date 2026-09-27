# AGENTS.md

One Go binary embeds Imagor and libvips to serve GCS images.

- `cmd/images`: configuration, lifecycle, health check.
- `internal/gateway`: URL validation, Etu capabilities, HTTP responses.
- `internal/engine`: GCS routing, Imagor/libvips setup, public result cache.
- Run `docker build -t images .` for race tests, native integration tests, vet,
  and build. Run `go test -race ./internal/gateway` without native dependencies.
- Run `docker build --target lint .` for the icco/go-template lint rules.
- Preserve existing URL and Etu signing contracts. Never cache private Etu data.
- Keep documentation and defaults deployment-neutral; configure buckets at runtime.
- Use Conventional Commits with lowercase subjects. Never commit secrets.
