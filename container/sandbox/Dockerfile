# syntax=docker/dockerfile:1
#
# The template of a brain's Docker Sandbox (docs/NODES.md, "Docker
# Sandboxes"): the image of sbx's Claude kit, with the Claude Code fridolin
# is tested with, Codex, ffmpeg, and fridolin's tools/ at
# /opt/fridolin/tools. `fridolin node sandbox setup` builds it with Docker
# and loads it into sbx (`sbx template load`); every brain's sandbox is
# created from it (`sbx create -t … --pull never`).
#
# It holds no secret and no data. The linux fridolin of the core's version
# is not in it: the node keeps that one in its state dir and links it to
# /opt/fridolin/bin/fridolin in each sandbox, from a read-only mount.
#
# Build from the repository's root:
#
#     docker build -f container/sandbox/Dockerfile -t fridolin-brain:dev .

# sbx's Claude kit image (Ubuntu, Node, Python, git), pinned by digest.
ARG BASE_IMAGE=docker/sandbox-templates:claude-code-docker@sha256:4a94d9144a70df744fd9247e0c365a14cfb97024afab8db7597c8ba3e0637642
FROM ${BASE_IMAGE}

# The same as the core image (container/Dockerfile): at least
# internal/claudecli's TestedClaudeVersion.
# renovate: datasource=npm depName=@anthropic-ai/claude-code
ARG CLAUDE_CODE_VERSION=2.1.303
# At least the Codex the bridge is tested against.
# renovate: datasource=npm depName=@openai/codex
ARG CODEX_VERSION=0.163.0

USER root
# ffmpeg for tools/transcribe.py; the kit has python3. The kit's own Claude
# Code in ~/.local/bin gives way to the pinned one, which never updates
# itself (DISABLE_AUTOUPDATER; the node also denies downloads.claude.ai).
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ffmpeg \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" "@openai/codex@${CODEX_VERSION}" \
 && npm cache clean --force \
 && root="$(npm root -g)" \
 && ln -sf "$root/@anthropic-ai/claude-code/bin/claude.exe" /usr/local/bin/claude \
 && rm -f ~agent/.local/bin/claude \
 && ln -s /usr/local/bin/claude ~agent/.local/bin/claude \
 && mkdir -p /opt/fridolin/bin /opt/fridolin/tools

COPY --chmod=0644 tools/*.py /opt/fridolin/tools/

ENV DISABLE_AUTOUPDATER=1
USER agent
RUN claude --version && codex --version && ffmpeg -version >/dev/null && python3 --version
