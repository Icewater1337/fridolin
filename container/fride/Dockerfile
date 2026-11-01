# syntax=docker/dockerfile:1
#
# The fridolin-fride image: FrIDE's companion with what its coding agents
# need (Claude Code, git, Go, Node, gh and common build tools), for the
# `fride` service of a node in a box (docs/NODES.md, "Node in a box").
# It holds no secret and no data: the Claude token, git identity, FrIDE's
# home and the repositories live in the volume at /var/lib/fride, the task
# workspaces in the one at /workspaces. It never gets the node's volume.
#
# Build from the repository's root:
#
#     docker build -f container/fride/Dockerfile \
#       --build-arg FRIDE_REPO=https://github.com/OWNER/fride \
#       --build-arg FRIDOLIN_REPO=https://github.com/OWNER/fridolin -t fridolin-fride:dev .
#
# FRIDE_REPO is where FrIDE is built from, and with FRIDOLIN_REPO what the
# home volume's starter repositories are cloned from; CI passes the
# repository owner's.
#
# FrIDE and ponytail are pinned to a commit, the downloads to a version
# and their SHA-256, the base images to version tags at least two weeks
# old when they were chosen.

ARG GO_IMAGE=golang:1.27.2-bookworm
ARG NODE_IMAGE=node:26.12.0-bookworm-slim

# ── FrIDE and ponytail ────────────────────────────────────────────────────
FROM ${GO_IMAGE} AS fride
ARG FRIDE_REPO
ARG FRIDE_COMMIT=aa7d94cd5e5da6ccb70930215ea71679e7e301fa
# ponytail v5.3.2 (docs/NODES.md, "Ponytail for every agent").
ARG PONYTAIL_REPO=https://github.com/DietrichGebert/ponytail
ARG PONYTAIL_COMMIT=16352ac229eb08a9598dcae48fe9f2b342567d86
RUN test -n "${FRIDE_REPO}" || { echo "pass --build-arg FRIDE_REPO=<FrIDE's git URL>" >&2; exit 1; }; \
    git clone -q "${FRIDE_REPO}" /src/fride \
 && git -C /src/fride checkout -q --detach "${FRIDE_COMMIT}" \
 && git clone -q "${PONYTAIL_REPO}" /out/ponytail \
 && git -C /out/ponytail checkout -q --detach "${PONYTAIL_COMMIT}" \
 && rm -rf /out/ponytail/.git
WORKDIR /src/fride
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/fride .

# ── GitHub's CLI, from its release ────────────────────────────────────────
FROM ${GO_IMAGE} AS tools
ARG TARGETARCH
# renovate: datasource=github-releases depName=cli/cli
ARG GH_VERSION=2.103.2
WORKDIR /dl
RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) gh_sha=62ccf747115ddc61237efffdde66a3c8f70ba249433dc64f70801089b59c59c8 ;; \
      arm64) gh_sha=4c3e99a50bd8b181a3fe9d69b25dd978bac768148d88f01507c6b4d1ef61a443 ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo gh.tar.gz "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz"; \
    echo "${gh_sha}  gh.tar.gz" | sha256sum -c -; \
    tar -xzf gh.tar.gz "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh"; \
    install -m 0755 "gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh

# ── Node and Claude Code ──────────────────────────────────────────────────
FROM ${NODE_IMAGE} AS node
# The version the core's image has (container/Dockerfile).
# renovate: datasource=npm depName=@anthropic-ai/claude-code
ARG CLAUDE_CODE_VERSION=2.1.310
RUN npm install -g --no-fund --no-audit "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force

# ── the runtime ───────────────────────────────────────────────────────────
# The Go image is the base: its Go builds what the agents work on, and it
# brings git, curl, gcc and make.
FROM ${GO_IMAGE}
ARG VERSION=dev
ARG FRIDE_REPO
ARG FRIDOLIN_REPO
LABEL org.opencontainers.image.title="fridolin-fride" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="MIT"

RUN apt-get update \
 && apt-get install -y --no-install-recommends tini socat lsof jq ripgrep python3 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=tools /usr/local/bin/gh /usr/local/bin/gh
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
 && ln -s ../lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe /usr/local/bin/claude \
 && claude --version && gh --version && node --version

COPY --from=fride /out/fride /opt/fride/bin/fride
COPY --from=fride /out/ponytail /opt/fride/plugins/ponytail
COPY container/fride/companion.json /opt/fride/companion.json
COPY container/fride/entrypoint.sh /opt/fride/entrypoint.sh
RUN ln -s /opt/fride/bin/fride /usr/local/bin/fride

# One unprivileged user, apart from the node's (10001): its home and the
# workspaces are the service's two volumes, made here so that new named
# volumes start out owned by it.
RUN groupadd --gid 10002 fride \
 && useradd --uid 10002 --gid 10002 --home-dir /var/lib/fride --create-home --shell /bin/bash fride \
 && install -d -o fride -g fride -m 0700 /workspaces

USER fride:fride
# GOPATH and the caches in the home volume; GOTOOLCHAIN=auto fetches the
# Go a repository's go.mod asks for when it is newer than the image's.
ENV HOME=/var/lib/fride \
    FRIDE_HOME=/var/lib/fride/fride \
    GOPATH=/var/lib/fride/go \
    GOTOOLCHAIN=auto \
    PATH=/var/lib/fride/go/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin \
    SHELL=/bin/bash \
    LANG=C.UTF-8 \
    FRIDE_STARTER_REPOS="${FRIDOLIN_REPO} ${FRIDE_REPO}"
WORKDIR /var/lib/fride
VOLUME ["/var/lib/fride", "/workspaces"]
EXPOSE 17840

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 \
  CMD ["curl", "-fsS", "-o", "/dev/null", "http://127.0.0.1:17840/"]

ENTRYPOINT ["/usr/bin/tini", "--", "/opt/fride/entrypoint.sh"]
CMD ["serve"]
