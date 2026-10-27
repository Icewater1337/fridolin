# syntax=docker/dockerfile:1
#
# The template of the sandboxed FrIDE's Docker Sandbox (docs/NODES.md,
# "FrIDE in a Docker Sandbox"): the image of sbx's Claude kit, which has
# Go, Node, git, gh, socat, jq, ripgrep and lsof, with the Claude Code
# fridolin is tested with and the pinned ponytail plugin at
# /opt/fride/plugins/ponytail, as in the fridolin-fride image.
# `fridolin node fride sandbox create` builds it with Docker and loads it
# into sbx (`sbx template load`); FrIDE itself is built inside the sandbox,
# at the commit container/fride/Dockerfile pins, so that an update needs
# no new template.
#
# It holds no secret and no data. The base image, Claude Code and ponytail
# are the ones container/sandbox/Dockerfile and container/fride/Dockerfile
# name; a test holds them to the same values.
#
# Build from the repository's root:
#
#     docker build -f container/sandbox/fride.Dockerfile -t fridolin-fride-sandbox:dev .

# sbx's Claude kit image, pinned by digest (Go 1.27, Node 22, git, gh, socat).
ARG BASE_IMAGE=docker/sandbox-templates:claude-code-docker@sha256:4a94d9144a70df744fd9247e0c365a14cfb97024afab8db7597c8ba3e0637642
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

ENV DISABLE_AUTOUPDATER=1
USER agent
RUN claude --version && go version && node --version && gh --version \
 && command -v socat jq lsof setsid ip \
 && test -f /opt/fride/plugins/ponytail/.claude-plugin/plugin.json
