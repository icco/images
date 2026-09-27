ARG BASE_IMAGE=ghcr.io/cshum/imagor-base:vips8.18.5-r14
FROM golang:1.27.1-bookworm AS go
FROM ${BASE_IMAGE}-dev AS development
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:/go/bin:$PATH GOPATH=/go CGO_ENABLED=1
ENV PKG_CONFIG_PATH=/opt/imagor/lib/pkgconfig CGO_CFLAGS=-I/opt/imagor/include
ENV CGO_LDFLAGS="-L/opt/imagor/lib -Wl,-rpath,/opt/imagor/lib"
ENV LD_LIBRARY_PATH=/opt/imagor/lib
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

FROM development AS lint
COPY --from=golangci/golangci-lint:v2.14.0 /usr/bin/golangci-lint /usr/local/bin/golangci-lint
RUN --mount=type=cache,target=/root/.cache/go-build golangci-lint run --timeout=5m

FROM development AS build
RUN --mount=type=cache,target=/root/.cache/go-build sh scripts/test.sh
RUN --mount=type=cache,target=/root/.cache/go-build go build -trimpath -ldflags="-s -w" -o /images ./cmd/images

FROM ${BASE_IMAGE}
LABEL org.opencontainers.image.source=https://github.com/icco/images
COPY --from=build /images /usr/local/bin/images
ENV LD_LIBRARY_PATH=/opt/imagor/lib VIPS_WARNING=0 MALLOC_ARENA_MAX=2
ENV FONTCONFIG_PATH=/etc/fonts XDG_CACHE_HOME=/tmp PORT=8080 CACHE_DIR=/cache
USER 1000:1000
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/usr/local/bin/images", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/images"]
