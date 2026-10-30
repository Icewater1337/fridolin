# syntax=docker/dockerfile:1
#
# The template of the FrIDE instances' Docker Sandboxes (docs/NODES.md,
# "FrIDE instances"): the image of sbx's Claude kit, which has Go, Node,
# git, gh, socat, jq, ripgrep and lsof, with FrIDE at /opt/fride/bin/fride,
# its config at /opt/fride/companion.json, the Claude Code fridolin is
# tested with and the pinned ponytail plugin at /opt/fride/plugins/ponytail,
# as in the fridolin-fride image. CI publishes it for amd64 and arm64 as
# ghcr.io/<owner>/fridolin-fride-sandbox (.github/workflows/image.yml),
# which sbx pulls on a node that set up with `fridolin node fride host
# setup --no-build`; `fridolin node fride host setup` with Docker builds
# it here instead and loads it into sbx (`sbx template load`). FrIDE is
# built here, so that an instance needs neither FrIDE's source nor GitHub;
# a new FrIDE is a new template, which an instance gets when the node
# makes its sandbox anew.
#
# It holds no secret and no data. FrIDE's commit, Go, Claude Code and
# ponytail are the ones container/fride/Dockerfile names; a test holds
# them to the same values.
#
# Build from the repository's root:
#
#     docker build -f container/sandbox/fride.Dockerfile \
#       --build-arg FRIDE_REPO=https://github.com/OWNER/fride -t fridolin-fride-sbx:dev .

# sbx's Claude kit image, pinned by digest (Go 1.27, Node 22, git, gh, socat).
ARG BASE_IMAGE=docker/sandbox-templates:claude-code-docker@sha256:4a94d9144a70df744fd9247e0c365a14cfb97024afab8db7597c8ba3e0637642
ARG GO_IMAGE=golang:1.27.2-bookworm

# ── FrIDE ─────────────────────────────────────────────────────────────────
FROM ${GO_IMAGE} AS fride
ARG FRIDE_REPO
ARG FRIDE_COMMIT=aa7d94cd5e5da6ccb70930215ea71679e7e301fa
RUN test -n "${FRIDE_REPO}" || { echo "pass --build-arg FRIDE_REPO=<FrIDE's git URL>" >&2; exit 1; }; \
    git clone -q "${FRIDE_REPO}" /src/fride \
 && git -C /src/fride checkout -q --detach "${FRIDE_COMMIT}"
WORKDIR /src/fride
RUN CGO_ENABLED=0 go build -trimpath -o /out/fride .

# ── the template ──────────────────────────────────────────────────────────
FROM ${BASE_IMAGE}

# renovate: datasource=npm depName=@anthropic-ai/claude-code
ARG CLAUDE_CODE_VERSION=2.1.303
# ponytail v5.3.2 (docs/NODES.md, "Ponytail for every agent").
ARG PONYTAIL_REPO=https://github.com/DietrichGebert/ponytail
ARG PONYTAIL_COMMIT=16352ac229eb08a9598dcae48fe9f2b342567d86

USER root
# iproute2 finds the VM's address, which FrIDE's forwarder listens on.
# The kit's own Claude Code in ~/.local/bin gives way to the pinned one,
# which never updates itself (DISABLE_AUTOUPDATER; the node also denies
# downloads.claude.ai).
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends iproute2 \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force \
 && root="$(npm root -g)" \
 && ln -sf "$root/@anthropic-ai/claude-code/bin/claude.exe" /usr/local/bin/claude \
 && rm -f ~agent/.local/bin/claude \
 && ln -s /usr/local/bin/claude ~agent/.local/bin/claude \
 && git clone -q "${PONYTAIL_REPO}" /opt/fride/plugins/ponytail \
 && git -C /opt/fride/plugins/ponytail checkout -q --detach "${PONYTAIL_COMMIT}" \
 && rm -rf /opt/fride/plugins/ponytail/.git
COPY --from=fride /out/fride /opt/fride/bin/fride
# The box's FrIDE config, which a new instance's starts from when the node
# gives none (a template from the core: fride host setup --no-build).
COPY container/fride/companion.json /opt/fride/companion.json

ENV DISABLE_AUTOUPDATER=1
USER agent
RUN claude --version && go version && node --version && gh --version \
 && test -x /opt/fride/bin/fride && jq -e .companion /opt/fride/companion.json >/dev/null \
 && command -v socat jq lsof setsid ip \
 && test -f /opt/fride/plugins/ponytail/.claude-plugin/plugin.json
