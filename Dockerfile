FROM --platform=$BUILDPLATFORM node:22-bookworm AS frontend-builder

WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend ./
RUN npm run build

FROM --platform=$BUILDPLATFORM rust:1.86-bookworm AS rust-builder
ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG TARGETARCH
ARG TARGETOS=linux
WORKDIR /src

RUN apt-get update -o Acquire::Retries=5 \
    && apt-get install -y -o Acquire::Retries=5 --no-install-recommends \
        cmake perl build-essential libclang-dev clang lld file \
        gcc-x86-64-linux-gnu g++-x86-64-linux-gnu \
        gcc-aarch64-linux-gnu g++-aarch64-linux-gnu \
    && rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) RUST_TARGET=x86_64-unknown-linux-gnu ;; \
      arm64) RUST_TARGET=aarch64-unknown-linux-gnu ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    rustup target add "${RUST_TARGET}"; \
    echo "${RUST_TARGET}" > /tmp/rust_target

ENV CARGO_TARGET_X86_64_UNKNOWN_LINUX_GNU_LINKER=x86_64-linux-gnu-gcc \
    CC_x86_64_unknown_linux_gnu=x86_64-linux-gnu-gcc \
    CXX_x86_64_unknown_linux_gnu=x86_64-linux-gnu-g++ \
    AR_x86_64_unknown_linux_gnu=x86_64-linux-gnu-ar \
    CARGO_TARGET_AARCH64_UNKNOWN_LINUX_GNU_LINKER=aarch64-linux-gnu-gcc \
    CC_aarch64_unknown_linux_gnu=aarch64-linux-gnu-gcc \
    CXX_aarch64_unknown_linux_gnu=aarch64-linux-gnu-g++ \
    AR_aarch64_unknown_linux_gnu=aarch64-linux-gnu-ar

ENV CARGO_TARGET_DIR=/cargo-target

COPY wreq-ffi ./wreq-ffi

RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/cargo-target,id=cargo-target-${TARGETARCH},sharing=locked \
    set -eux; \
    RUST_TARGET=$(cat /tmp/rust_target); \
    case "${TARGETARCH}" in \
      amd64) CC=x86_64-linux-gnu-gcc; CXX=x86_64-linux-gnu-g++; AR=x86_64-linux-gnu-ar ;; \
      arm64) CC=aarch64-linux-gnu-gcc; CXX=aarch64-linux-gnu-g++; AR=aarch64-linux-gnu-ar ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    export CC CXX AR; \
    echo "rust-builder toolchain: TARGETARCH=${TARGETARCH} RUST_TARGET=${RUST_TARGET} CC=${CC} CXX=${CXX} AR=${AR}"; \
    echo "rust-builder diag: BUILDPLATFORM=${BUILDPLATFORM} TARGETPLATFORM=${TARGETPLATFORM} TARGETARCH=${TARGETARCH} RUST_TARGET=${RUST_TARGET} host=$(uname -m)"; \
    cd wreq-ffi; \
    mkdir -p include; \
    touch src/lib.rs; \
    cargo build --release --target "${RUST_TARGET}"; \
    test -f include/wreq_ffi.h; \
    mkdir -p /out; \
    cp "${CARGO_TARGET_DIR}/${RUST_TARGET}/release/libwreq_ffi.a" /out/; \
    cp include/wreq_ffi.h /out/; \
    FIRST_MEMBER=$(ar t /out/libwreq_ffi.a | head -1); \
    AFILE=$(ar p /out/libwreq_ffi.a "$FIRST_MEMBER" | file -); \
    echo "rust-builder: first member ($FIRST_MEMBER) of /out/libwreq_ffi.a => ${AFILE}"; \
    case "${TARGETARCH}" in \
      amd64) echo "${AFILE}" | grep -q 'x86-64'  || { echo "FATAL: /out/libwreq_ffi.a is not x86-64 (TARGETARCH=amd64). This usually means a cache mount got mixed up; try: docker buildx prune -af" >&2; exit 1; } ;; \
      arm64) echo "${AFILE}" | grep -q 'aarch64' || { echo "FATAL: /out/libwreq_ffi.a is not aarch64 (TARGETARCH=arm64). This usually means a cache mount got mixed up; try: docker buildx prune -af" >&2; exit 1; } ;; \
    esac; \
    echo "rust-builder: arch verified for TARGETARCH=${TARGETARCH}"

FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS builder
ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH

RUN apt-get update -o Acquire::Retries=5 \
    && apt-get install -y -o Acquire::Retries=5 --no-install-recommends \
        file \
        gcc-x86-64-linux-gnu g++-x86-64-linux-gnu \
        gcc-aarch64-linux-gnu g++-aarch64-linux-gnu \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY static ./static
COPY --from=frontend-builder /frontend/out /src/static/admin
COPY --from=rust-builder /out/libwreq_ffi.a /src/wreq-ffi/target/release/libwreq_ffi.a
COPY --from=rust-builder /out/wreq_ffi.h    /src/wreq-ffi/include/wreq_ffi.h

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    case "${TARGETARCH}" in \
      amd64) CC=x86_64-linux-gnu-gcc; CXX=x86_64-linux-gnu-g++ ;; \
      arm64) CC=aarch64-linux-gnu-gcc; CXX=aarch64-linux-gnu-g++ ;; \
      *) echo "unsupported TARGETARCH=${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    echo "go-builder diag: BUILDPLATFORM=${BUILDPLATFORM} TARGETPLATFORM=${TARGETPLATFORM} TARGETARCH=${TARGETARCH} CC=${CC} host=$(uname -m)"; \
    FIRST_MEMBER=$(ar t /src/wreq-ffi/target/release/libwreq_ffi.a | head -1); \
    AFILE=$(ar p /src/wreq-ffi/target/release/libwreq_ffi.a "$FIRST_MEMBER" | file -); \
    echo "go-builder: first member ($FIRST_MEMBER) of libwreq_ffi.a => ${AFILE}"; \
    case "${TARGETARCH}" in \
      amd64) echo "${AFILE}" | grep -q 'x86-64'  || { echo "FATAL: libwreq_ffi.a in builder stage is not x86-64; rust-builder produced wrong arch or COPY layer is stale. Run: docker buildx prune -af" >&2; exit 1; } ;; \
      arm64) echo "${AFILE}" | grep -q 'aarch64' || { echo "FATAL: libwreq_ffi.a in builder stage is not aarch64; rust-builder produced wrong arch or COPY layer is stale. Run: docker buildx prune -af" >&2; exit 1; } ;; \
    esac; \
    test -f ./cmd/notion2api/main.go; \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} CC=${CC} CXX=${CXX} \
      go build -v -trimpath -tags wreq_ffi \
              -ldflags="-s -w" \
              -o /out/notion2api ./cmd/notion2api

FROM node:22-bookworm-slim

ENV TZ=Asia/Shanghai
ENV NODE_PATH=/opt/notion2api-helper/node_modules
ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl tini \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb \
    && mkdir -p /opt/notion2api-helper /app/config /app/data/notion_accounts /app/static

RUN cd /opt/notion2api-helper \
    && npm init -y >/dev/null 2>&1 \
    && npm install --omit=dev --no-package-lock node-wreq@2.2.1 \
    && test -d "$NODE_PATH/node-wreq" \
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
