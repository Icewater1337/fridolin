# syntax=docker/dockerfile:1
#
# The fridolin-core image: the bridge, its Python helpers and everything its
# brains and backups run, on Debian. One named volume (/data) holds all of
# the bridge's state; the image holds none. docs/CORE.md explains how it is
# started, moved and updated.
#
# Build from the repository's root:
#
#     docker build -f container/Dockerfile -t fridolin-core:dev .
#
# Base images are pinned to version tags at least two weeks old when they
# were chosen; the downloaded tools are pinned to a version and checked
# against the SHA-256 their release publishes.

ARG GO_IMAGE=golang:1.26.8-bookworm
ARG NODE_IMAGE=node:22.23.2-bookworm-slim
ARG RUNTIME_IMAGE=debian:bookworm-20261005-slim

# ── the bridge ────────────────────────────────────────────────────────────
FROM ${GO_IMAGE} AS build
# The version `fridolin version` reports and `fridolin core start` pins
# the image tag to.
ARG VERSION=dev
# 0 once SQLite is pure Go (modernc.org/sqlite, PLAN.md M0). A build of a
# tree that still links the cgo driver passes --build-arg CGO_ENABLED=1:
# this stage and the runtime share Debian bookworm's glibc.
ARG CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=${CGO_ENABLED} go build -trimpath -buildvcs=false \
      -ldflags "-X github.com/Icewater1337/fridolin/internal/version.version=${VERSION}" \
      -o /out/fridolin ./cmd/fridolin

# ── restic, rclone and litestream, from their releases ────────────────────
FROM ${RUNTIME_IMAGE} AS tools
ARG TARGETARCH
ARG RESTIC_VERSION=0.19.1
ARG RCLONE_VERSION=1.75.1
ARG LITESTREAM_VERSION=0.5.17
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl bzip2 unzip \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /dl
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) restic_sha=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c; \
             rclone_sha=982b5aa772841168f8e380f139e9e787b2a105403e32b94da8676a0e1c0a13ab; \
             ls_arch=x86_64; ls_sha=cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d ;; \
      arm64) restic_sha=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465; \
             rclone_sha=03f2504174034b6d004152ed7369251c9a9ec1f7e0836eda420f5c7a5ec0dff9; \
             ls_arch=arm64; ls_sha=f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5 ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo restic.bz2 "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${TARGETARCH}.bz2"; \
    echo "${restic_sha}  restic.bz2" | sha256sum -c -; \
    bunzip2 restic.bz2; install -m 0755 restic /usr/local/bin/restic; \
    curl -fsSLo rclone.zip "https://github.com/rclone/rclone/releases/download/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-linux-${TARGETARCH}.zip"; \
    echo "${rclone_sha}  rclone.zip" | sha256sum -c -; \
    unzip -q rclone.zip; install -m 0755 "rclone-v${RCLONE_VERSION}-linux-${TARGETARCH}/rclone" /usr/local/bin/rclone; \
    curl -fsSLo litestream.tar.gz "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-${LITESTREAM_VERSION}-linux-${ls_arch}.tar.gz"; \
    echo "${ls_sha}  litestream.tar.gz" | sha256sum -c -; \
    tar -xzf litestream.tar.gz litestream; install -m 0755 litestream /usr/local/bin/litestream

# ── Node and the Claude CLI ───────────────────────────────────────────────
# The CLI's package installs the native build for the image's platform
# (bin/claude.exe, whatever the OS); Node stays for npm-installed helpers
# such as the Plaud CLI and the site process.
FROM ${NODE_IMAGE} AS node
# At least the version internal/claudecli tests against (TestedClaudeVersion).
ARG CLAUDE_CODE_VERSION=2.1.303
RUN npm install -g --no-fund --no-audit "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force

# ── the runtime ───────────────────────────────────────────────────────────
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
LABEL org.opencontainers.image.title="fridolin-core" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates tzdata tini bubblewrap ffmpeg python3 git curl \
 && rm -rf /var/lib/apt/lists/*

COPY --from=tools /usr/local/bin/restic /usr/local/bin/rclone /usr/local/bin/litestream /usr/local/bin/
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && ln -s ../lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe /usr/local/bin/claude \
 && claude --version

# The binary with tools/ next to it, as `fridolin service install` lays
# out an install: brains get them as $FRIDOLIN_BIN and $FRIDOLIN_TOOLS_DIR.
COPY --from=build /out/fridolin /opt/fridolin/fridolin
COPY tools/*.py /opt/fridolin/tools/
COPY container/entrypoint.sh /opt/fridolin/entrypoint.sh
RUN ln -s /opt/fridolin/fridolin /usr/local/bin/fridolin

# One unprivileged user owns the volume. /data is made here, owned by it
# and 0700, so a new named volume starts out that way (Docker copies the
# folder's owner and mode into an empty volume on first use).
RUN groupadd --gid 10001 fridolin \
 && useradd --uid 10001 --gid 10001 --home-dir /var/lib/fridolin --create-home --shell /usr/sbin/nologin fridolin \
 && install -d -o fridolin -g fridolin -m 0700 /data

USER fridolin:fridolin
# FRIDOLIN_CONTAINER=1 tells the bridge it runs here: brains' runs are
# sandboxed with bubblewrap (sandbox.enabled is allowed and on by default),
# and doctor leaves out the host's checks.
ENV HOME=/var/lib/fridolin \
    FRIDOLIN_HOME=/data \
    FRIDOLIN_CONFIG=/data/config.json \
    TZ=UTC \
    FRIDOLIN_CONTAINER=1 \
    LANG=C.UTF-8
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8765

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD ["curl", "-fsS", "-o", "/dev/null", "http://127.0.0.1:8765/health"]

# tini reaps the brains' process groups' orphans and forwards signals, so
# `docker stop` reaches the bridge as SIGTERM and it shuts down in order.
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/fridolin/entrypoint.sh"]
CMD ["run", "--container"]
