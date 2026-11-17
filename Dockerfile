# syntax=docker/dockerfile:1.7
#
# Tusk image: one distroless image with all four binaries.
#   /usr/local/bin/tusk        all roles (default ENTRYPOINT)
#   /usr/local/bin/redirector  hot path only
#   /usr/local/bin/api         JSON API + web UI
#   /usr/local/bin/worker      visit pipeline + expired-link sweeper
# Override with e.g. `--entrypoint /usr/local/bin/redirector` or compose `entrypoint:`.

ARG NODE_VERSION=26
ARG GO_VERSION=1.27.1

# ---------- 1. web: React + shadcn SPA ----------
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------- 2. go: static binaries ----------
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS go
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# The SPA is embedded via //go:embed all:dist in internal/webui.
COPY --from=web /src/web/dist/ ./internal/webui/dist/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; mkdir -p /out; \
    for c in tusk redirector api worker; do \
      GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/$c ./cmd/$c; \
    done; \
    mkdir -p /out/data /out/custom

# ---------- 3. runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="Tusk" \
      org.opencontainers.image.description="High-traffic URL shortener" \
      org.opencontainers.image.source="https://github.com/0mr4n/Tusk" \
      org.opencontainers.image.licenses="MIT"
WORKDIR /tusk
COPY --from=go /out/tusk /out/redirector /out/api /out/worker /usr/local/bin/
# Volumes: /var/lib/tusk (sqlite DB_FILENAME) and /tusk/custom (TUSK_CUSTOM_DIR).
COPY --from=go --chown=nonroot:nonroot /out/data /var/lib/tusk
COPY --from=go --chown=nonroot:nonroot /out/custom /tusk/custom
ENV PORT=3000 \
    TUSK_CUSTOM_DIR=/tusk/custom \
    DB_FILENAME=/var/lib/tusk/data.sqlite \
    NODE_ENV=production
EXPOSE 3000 3001 9090
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/tusk"]
