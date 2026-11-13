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

ARG GO_IMAGE=golang:1.26.10-bookworm
ARG NODE_IMAGE=node:26.13.0-bookworm-slim
ARG RUNTIME_IMAGE=debian:bookworm-20261104-slim

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

# ── the node binaries ─────────────────────────────────────────────────────
# fridolin for every platform a node runs on, with the core's version,
# which the core updates its nodes to (docs/NODES.md, "Updates"). Built on
# the build machine's own platform (cross-compiled, cgo off), so both
# images carry the same files; stripped and gzip'd, about 17 MB each.
FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS node-binaries
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    scripts/build-node-binaries.sh "${VERSION}" /out/node-binaries

# ── restic, rclone and litestream, from their releases ────────────────────
FROM ${RUNTIME_IMAGE} AS tools
ARG TARGETARCH
# renovate: datasource=github-releases depName=restic/restic
ARG RESTIC_VERSION=0.19.1
# renovate: datasource=github-releases depName=rclone/rclone
ARG RCLONE_VERSION=1.76.0
# renovate: datasource=github-releases depName=benbjohnson/litestream
ARG LITESTREAM_VERSION=0.5.18
# What brains' skills run besides: GitHub's CLI and Cloudflare's tunnel
# (quick tunnels to a dev server).
# renovate: datasource=github-releases depName=cli/cli
ARG GH_VERSION=2.103.3
# renovate: datasource=github-releases depName=cloudflare/cloudflared
ARG CLOUDFLARED_VERSION=2026.10.7
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl bzip2 unzip \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /dl
# fetch FILE URL retries what a release CDN fails now and then (a 504, a
# reset connection), so one such answer does not fail the whole build; the
# SHA-256 check after each download stays the judge of what arrived.
RUN set -eu; \
    fetch() { curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 --connect-timeout 30 -o "$1" "$2"; }; \
    case "${TARGETARCH}" in \
      amd64) restic_sha=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c; \
             rclone_sha=349ac8fba6ff65d6247043f1750cdcb518ec5d500ef91463a10d37c0ccdf3702; \
             ls_arch=x86_64; ls_sha=d3687b9fd0f2e9426bbd3da828429ff3f1dfc5c9b74d54fdf028e4ee510e7dea; \
             gh_sha=4c806978ffce1cec70853302a56d70ac6da4f19bae7adca4e1a8ab00633dd91d; \
             cf_sha=1f99fb518b83dec2ba8d870a7cc991d82a77e6196295a3d2754ba17ab9f91872 ;; \
      arm64) restic_sha=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465; \
             rclone_sha=7e1e8d69654941b7b7df84ee74c5f7fb09cce7d5496947fb7bf16b55ff427d10; \
             ls_arch=arm64; ls_sha=922a09e6a7ac67f2095571894dc6382e94e1dd4cdd9674883ddf1a8e74ab982d; \
             gh_sha=ee32df2323c8847f5fac6cb6b4b69ca75828e9329a8f22f677021f7b3306ce63; \
             cf_sha=3df43aedc9ff291d1735b5cb576cb97e0b157488796db2e32b28aecabb445775 ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    fetch restic.bz2 "https://github.com/restic/restic/releases/download/v${RESTIC_VERSION}/restic_${RESTIC_VERSION}_linux_${TARGETARCH}.bz2"; \
    echo "${restic_sha}  restic.bz2" | sha256sum -c -; \
    bunzip2 restic.bz2; install -m 0755 restic /usr/local/bin/restic; \
    fetch rclone.zip "https://github.com/rclone/rclone/releases/download/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-linux-${TARGETARCH}.zip"; \
    echo "${rclone_sha}  rclone.zip" | sha256sum -c -; \
    unzip -q rclone.zip; install -m 0755 "rclone-v${RCLONE_VERSION}-linux-${TARGETARCH}/rclone" /usr/local/bin/rclone; \
    fetch litestream.tar.gz "https://github.com/benbjohnson/litestream/releases/download/v${LITESTREAM_VERSION}/litestream-${LITESTREAM_VERSION}-linux-${ls_arch}.tar.gz"; \
    echo "${ls_sha}  litestream.tar.gz" | sha256sum -c -; \
    tar -xzf litestream.tar.gz litestream; install -m 0755 litestream /usr/local/bin/litestream; \
    fetch gh.tar.gz "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "${gh_sha}  gh.tar.gz" | sha256sum -c -; \
    tar -xzf gh.tar.gz "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh"; install -m 0755 "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh; \
    fetch cloudflared "https://github.com/cloudflare/cloudflared/releases/download/${CLOUDFLARED_VERSION}/cloudflared-linux-${TARGETARCH}"; \
    echo "${cf_sha}  cloudflared" | sha256sum -c -; \
    install -m 0755 cloudflared /usr/local/bin/cloudflared

# ── the Python brains' tools run ──────────────────────────────────────────
# A venv at /opt/fridolin/venv, first on PATH, so `python3 script.py` and
# `#!/usr/bin/env python3` get it: local transcription (torch, CPU only,
# and transformers, as tools/transcribe.py uses them), Google's API
# client for calendar scripts, and yt-dlp. Every package is pinned with
# its hashes in container/python/requirements.lock (make
# container-python-lock). The runtime stage has the same Debian, so the
# venv's python3 is the runtime's.
FROM ${RUNTIME_IMAGE} AS python
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates python3 python3-venv \
 && rm -rf /var/lib/apt/lists/*
COPY container/python/requirements.lock /tmp/requirements.lock
RUN python3 -m venv /opt/fridolin/venv \
 && /opt/fridolin/venv/bin/pip install --no-cache-dir --disable-pip-version-check \
      --require-hashes --no-deps --only-binary=:all: \
      --index-url https://pypi.org/simple --extra-index-url https://download.pytorch.org/whl/cpu \
      -r /tmp/requirements.lock \
 && find /opt/fridolin/venv -name __pycache__ -prune -exec rm -rf {} + \
 && /opt/fridolin/venv/bin/python -c "import torch, transformers, googleapiclient, google_auth_oauthlib, groq, yt_dlp"

# ── Node and the Claude CLI ───────────────────────────────────────────────
# The CLI's package installs the native build for the image's platform
# (bin/claude.exe, whatever the OS); Node stays for npm-installed helpers
# such as the Plaud CLI and the site process.
FROM ${NODE_IMAGE} AS node
# Claude Code (at least internal/claudecli's TestedClaudeVersion), Codex
# (agent_provider "codex"; each brain signs in with brains/<id>/codex/
# auth.json in the data dir, so a sign-in moves with an export) and the
# Plaud CLI (plaud.enabled, as docs/PLAUD.md installs it). Their versions,
# and every package beneath them with its hash, are pinned in
# container/npm/package-lock.json, which Renovate updates: a build installs
# the same tree every time, not whatever was published last. Install
# scripts stay off, but for Claude Code's own, which puts the native build
# for the image's platform in place. container/npm/.npmrc nests each
# tool's dependencies in its own folder, as npm install -g does, and the
# tools are copied into the global folder from there.
COPY container/npm/package.json container/npm/package-lock.json container/npm/.npmrc /tmp/npm/
RUN cd /tmp/npm \
 && npm ci --ignore-scripts --no-fund --no-audit \
 && npm rebuild @anthropic-ai/claude-code \
 && cp -a node_modules/@anthropic-ai node_modules/@openai node_modules/@plaud-ai /usr/local/lib/node_modules/ \
 && cd / && rm -rf /tmp/npm \
 && npm cache clean --force

# ── the runtime ───────────────────────────────────────────────────────────
FROM ${RUNTIME_IMAGE}
ARG VERSION=dev
# Where box.sh and box.ps1 of this version are published, and the
# registry of its images: what the core's !join reply puts in the line
# that installs a node in a box. CI passes the repository owner's; a
# local build without them offers native nodes only. The tag of the FrIDE
# instances' template there (fridolin-fride-sandbox), which a node that
# built none makes its instances from; without it the core names the
# template of its version.
ARG FRIDOLIN_BOX_SOURCE=
ARG FRIDOLIN_BOX_REGISTRY=
ARG FRIDOLIN_FRIDE_SANDBOX_TAG=
LABEL org.opencontainers.image.title="fridolin-core" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"

# libatomic1: Node 26, copied in from the node image, links against it.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates tzdata tini bubblewrap ffmpeg python3 git openssh-client curl jq sqlite3 age \
      libatomic1 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=tools /usr/local/bin/restic /usr/local/bin/rclone /usr/local/bin/litestream \
     /usr/local/bin/gh /usr/local/bin/cloudflared /usr/local/bin/
COPY --from=python /opt/fridolin/venv /opt/fridolin/venv
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && ln -s ../lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe /usr/local/bin/claude \
 && ln -s ../lib/node_modules/@openai/codex/bin/codex.js /usr/local/bin/codex \
 && ln -s ../lib/node_modules/@plaud-ai/cli/dist/index.js /usr/local/bin/plaud \
 && claude --version && codex --version && plaud --help >/dev/null \
 && gh --version && cloudflared --version && /opt/fridolin/venv/bin/yt-dlp --version

# The binary with tools/ next to it, as `fridolin service install` lays
# out an install: brains get them as $FRIDOLIN_BIN and $FRIDOLIN_TOOLS_DIR.
COPY --from=build /out/fridolin /opt/fridolin/fridolin
# Beside the binary, where the bridge looks for them (FRIDOLIN_NODE_BINARIES
# names another folder).
COPY --from=node-binaries /out/node-binaries /opt/fridolin/node-binaries
COPY tools/*.py /opt/fridolin/tools/
# The Plaud Sync phone app's source: the image has no JDK or Android SDK,
# so the dashboard sends it to a node that builds Android apps
# (android-build, docs/PLAUD.md).
COPY plaud-android /opt/fridolin/plaud-android
COPY container/entrypoint.sh /opt/fridolin/entrypoint.sh
RUN ln -s /opt/fridolin/fridolin /usr/local/bin/fridolin

# One unprivileged user owns the volume. /data is made here, owned by it
# and 0700, so a new named volume starts out that way (Docker copies the
# folder's owner and mode into an empty volume on first use); /node and
# /backups are a box's node volumes (container/box.yaml).
# The home folder is the volume fridolin-home (compose.yaml), where the
# credentials folder a core move brings lives (sandbox.credentials_dir).
RUN groupadd --gid 10001 fridolin \
 && useradd --uid 10001 --gid 10001 --home-dir /var/lib/fridolin --create-home --shell /usr/sbin/nologin fridolin \
 && install -d -o fridolin -g fridolin -m 0700 /data /node /backups

USER fridolin:fridolin
# FRIDOLIN_CONTAINER=1 tells the bridge it runs here: brains' runs are
# sandboxed with bubblewrap (sandbox.enabled is allowed and on by default),
# and doctor leaves out the host's checks.
# PATH puts the image's Python first (see the python stage).
ENV HOME=/var/lib/fridolin \
    PATH=/opt/fridolin/venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    FRIDOLIN_HOME=/data \
    FRIDOLIN_CONFIG=/data/config.json \
    TZ=UTC \
    FRIDOLIN_CONTAINER=1 \
    FRIDOLIN_BOX_SOURCE=${FRIDOLIN_BOX_SOURCE} \
    FRIDOLIN_BOX_REGISTRY=${FRIDOLIN_BOX_REGISTRY} \
    FRIDOLIN_FRIDE_SANDBOX_TAG=${FRIDOLIN_FRIDE_SANDBOX_TAG} \
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
