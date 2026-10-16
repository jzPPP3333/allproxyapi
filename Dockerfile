# Multi-stage build: the builder carries the Go toolchain, the runtime image
# ships only the static binary and its CA bundle.
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Dependencies first so a source-only change reuses the module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 keeps the binary fully static so it runs on a bare Alpine
# image; the ldflags drop the symbol table and DWARF data.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/allproxyapi ./cmd/proxy

# ---------------------------------------------------------------------------

FROM alpine:3.20

# The gateway talks HTTPS to every upstream, so the trust store matters. The
# timezone data keeps log timestamps and the check-in scheduler on local time.
RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 app

WORKDIR /app

COPY --from=builder /out/allproxyapi /app/allproxyapi

# Credentials live on a mounted volume, never in the image. The directory is
# owned by the unprivileged user so the gateway can persist refreshed tokens.
RUN mkdir -p /app/data/oauth && chown -R app:app /app

USER app

ENV TZ=Asia/Shanghai

# The listener defaults to loopback, which is unreachable from outside a
# container. Binding all interfaces is required here; publish it deliberately
# (see docker-compose.yml, which binds only to the host loopback).
EXPOSE 8320

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8320/health || exit 1

ENTRYPOINT ["/app/allproxyapi"]
CMD ["--config", "/app/data/config.yaml", "--host", "0.0.0.0"]
