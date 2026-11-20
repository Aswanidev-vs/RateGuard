# syntax=docker/dockerfile:1

# RateGuard image: one final image with both binaries.
#   /app/rateguard -> cmd/server (API + dashboard)
#   /app/demo      -> examples/quote (quote upstream for local demos)

ARG VERSION=dev
ARG COMMIT=none

# ---------- build ----------
FROM golang:1.27-bookworm AS builder

ARG VERSION
ARG COMMIT

WORKDIR /src

# Dependency layer: only invalidated when the module files change.
COPY go.mod go.sum ./
RUN go mod download

# Source layer (web/ is embedded by the app and must be present).
COPY . .

RUN mkdir -p /out \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/rateguard ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/demo ./examples/quote

# ---------- runtime ----------
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION
ARG COMMIT

LABEL org.opencontainers.image.title="RateGuard" \
      org.opencontainers.image.description="Multi-tenant API rate limiting proxy, usage dashboard, and AWS cost anomaly view" \
      org.opencontainers.image.source="https://github.com/Aswanidev-vs/RateGuard" \
      org.opencontainers.image.url="https://github.com/Aswanidev-vs/RateGuard" \
      org.opencontainers.image.documentation="https://github.com/Aswanidev-vs/RateGuard/blob/main/docs/docker/DOCKER-GUIDE.md" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12:nonroot" \
      org.opencontainers.image.vendor="RateGuard"

WORKDIR /app

COPY --from=builder --chown=65532:65532 /out/rateguard /app/rateguard
COPY --from=builder --chown=65532:65532 /out/demo /app/demo

# distroless nonroot is UID/GID 65532; no shell or package manager in the image.
USER 65532:65532

EXPOSE 8080

# The server binary knows how to probe itself; distroless has no curl/wget.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD ["/app/rateguard", "-healthcheck"]

ENTRYPOINT ["/app/rateguard"]
