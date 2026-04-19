FROM node:22-bookworm AS frontend-builder

WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend ./
RUN npm run build

FROM golang:1.22-bookworm AS builder

WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
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
    test -f ./cmd/notion2api/main.go \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -v -trimpath -ldflags="-s -w" -o /out/notion2api ./cmd/notion2api

FROM mcr.microsoft.com/playwright:v1.58.0-noble

ENV TZ=Asia/Shanghai
ENV PLAYWRIGHT_CHROME_BIN=/opt/playwright-helper/chrome
ENV CHROME_BIN=/opt/playwright-helper/chrome
ENV CHROMIUM_BIN=/opt/playwright-helper/chrome
ENV NODE_PATH=/opt/playwright-helper/node_modules
WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl tini \
    && rm -rf /var/lib/apt/lists/* \
    && browser_bin="" \
    && for candidate in /ms-playwright/chromium-*/chrome-linux64/chrome /ms-playwright/chromium-*/chrome-linux/chrome; do \
        if [ -x "$candidate" ]; then browser_bin="$candidate"; break; fi; \
    done \
    && test -n "$browser_bin" \
    && mkdir -p /opt/playwright-helper /app/config /app/data/notion_accounts /app/static \
    && ln -sf "$browser_bin" "$PLAYWRIGHT_CHROME_BIN" \
    && "$PLAYWRIGHT_CHROME_BIN" --version

RUN mkdir -p /opt/playwright-helper \
    && cd /opt/playwright-helper \
    && npm init -y >/dev/null 2>&1 \
    && npm install --omit=dev --no-package-lock playwright-core@1.58.0 \
    && test -d "$NODE_PATH/playwright-core" \
    && npm cache clean --force >/dev/null 2>&1

COPY --from=builder /out/notion2api /app/notion2api
COPY --from=builder /src/static /app/static
COPY config.docker.json /app/config/config.default.json
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

RUN sed -i 's/\r$//' /usr/local/bin/docker-entrypoint.sh \
    && chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD curl -fsS http://127.0.0.1:8787/healthz || exit 1

ENTRYPOINT ["tini", "--", "docker-entrypoint.sh"]
CMD ["./notion2api", "--config", "/app/config/config.json"]
