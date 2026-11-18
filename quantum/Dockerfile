# Context: repository root after scripts/prepare-quantum.py. Build only on request.
FROM golang:1.27.0-alpine AS backend
RUN apk add --no-cache gcc musl-dev
WORKDIR /src/quantum
COPY .build/quantum/source/backend/ ./
COPY companion/ /src/companion/
RUN go mod edit -replace=filebeans/companion=/src/companion \
    && go mod download \
    && go run ./scripts/setup-gofitz-cgo
ENV CGO_ENABLED=1
RUN go test -tags analytics,mupdf,musl ./internal/preview -run '^TestVaultNativeDocumentPreview$' -count=1
RUN go build -tags analytics,mupdf,musl \
    -ldflags="-w -s -X github.com/gtsteffaniak/filebrowser/backend/internal/version.Version=v2.0.2-filebeans-vaults -X github.com/gtsteffaniak/filebrowser/backend/internal/version.CommitSHA=2c071d1e6eb48d4b88da2657885d345663a0a88b" \
    -o /out/filebrowser .

# Preserve this release's frontend, FFmpeg, certificates and runtime dependencies.
FROM ghcr.io/gtsteffaniak/filebrowser:stable@sha256:de2b4c4d18485ea202ab2cdada5bd25d8f0b8ed071712c8216a67194c31285ae
COPY --from=backend --chown=1000:1000 /out/filebrowser /home/filebrowser/filebrowser
USER 1000:1000
