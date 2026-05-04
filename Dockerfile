FROM --platform=$BUILDPLATFORM node:22-bookworm AS frontend-builder

WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm,sharing=locked \
    npm ci
COPY frontend ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.25.0-bookworm AS builder
ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY static ./static
COPY --from=frontend-builder /frontend/out /src/static/admin

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -v -trimpath \
              -ldflags="-s -w" \
              -o /out/notion2api ./cmd/notion2api

FROM alpine:3.22

ARG TARGETARCH
ENV TZ=Asia/Shanghai
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata curl tini \
    && mkdir -p /app/config /app/data/notion_accounts /app/static

COPY --from=builder /out/notion2api /app/notion2api
COPY --from=builder /src/static /app/static
COPY config.docker.json /app/config/config.default.json
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN sed -i 's/\r$//' /usr/local/bin/docker-entrypoint.sh \
    && chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD curl -fsS http://127.0.0.1:8787/healthz || exit 1

ENTRYPOINT ["/sbin/tini", "--", "docker-entrypoint.sh"]
CMD ["./notion2api", "--config", "/app/config/config.json"]
