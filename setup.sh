#!/usr/bin/env bash
#
# Sets up Fridolin on this machine:
#   1. checks prerequisites (Go, a C compiler, the Claude CLI; rclone optional)
#   2. builds the binary and runs the tests
#   3. optionally installs a background service (launchd on macOS,
#      a systemd user unit on Linux) so it starts at login
#   4. starts the bridge and opens the dashboard
#
# Everything else — pairing WhatsApp, adding brains, allowing chats, Google
# Drive backups — happens in the dashboard.
#
#   ./setup.sh                 guided
#   ./setup.sh --yes           accept every default (installs the service)
#   ./setup.sh --no-service    build only; run ./fridolin yourself
#
# Safe to re-run: it rebuilds and leaves your configuration and brains alone.

set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="$REPO_DIR/fridolin"
# Installs from before the rename keep their data folder.
if [ -z "${DATA_DIR:-}" ] && [ ! -d "$HOME/.fridolin" ] && [ -d "$HOME/.whatsapp-claude-bridge" ]; then
  DATA_DIR="$HOME/.whatsapp-claude-bridge"
fi
DATA_DIR="${DATA_DIR:-$HOME/.fridolin}"
CONFIG_FILE="${CONFIG_FILE:-$DATA_DIR/config.json}"
LABEL="ai.fridolin.bridge"

ASSUME_YES=false
NO_SERVICE=false
for arg in "$@"; do
  case "$arg" in
    --yes|-y) ASSUME_YES=true ;;
    --no-service) NO_SERVICE=true ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

# ── output helpers ────────────────────────────────────────────────────────
if [ -t 1 ]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; BLUE=$'\033[34m'; RESET=$'\033[0m'
else
  BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; BLUE=""; RESET=""
fi
step() { printf '\n%s▸ %s%s\n' "$BOLD$BLUE" "$*" "$RESET"; }
info() { printf '  %s\n' "$*"; }
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
fail() { printf '\n%s✗ %s%s\n\n' "$RED" "$*" "$RESET" >&2; exit 1; }

# confirm PROMPT DEFAULT(y|n)
confirm() {
  local prompt="$1" def="$2" ans
  if $ASSUME_YES || [ ! -t 0 ]; then [ "$def" = y ]; return; fi
  read -r -p "  $prompt [$( [ "$def" = y ] && echo Y/n || echo y/N )] " ans
  ans="$(printf '%s' "${ans:-$def}" | tr '[:upper:]' '[:lower:]')"
  [ "$ans" = y ] || [ "$ans" = yes ]
}

OS="$(uname)"

# ── 1. prerequisites ──────────────────────────────────────────────────────
step "Checking prerequisites"

if ! command -v go >/dev/null 2>&1; then
  fail "Go is not installed. Install Go 1.25+ (macOS: brew install go; Linux: https://go.dev/dl/), then re-run."
fi
GO_MINOR="$(go env GOVERSION | sed -E 's/^go1\.([0-9]+).*/\1/')"
if [ "${GO_MINOR:-0}" -lt 25 ]; then
  fail "Go $(go env GOVERSION) is too old; the bridge needs Go 1.25 or newer."
fi
ok "Go $(go env GOVERSION)"

if ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1 && ! command -v clang >/dev/null 2>&1; then
  if [ "$OS" = Darwin ]; then
    fail "No C compiler (needed for SQLite). Run: xcode-select --install"
  else
    fail "No C compiler (needed for SQLite). Install build-essential (Debian/Ubuntu) or gcc."
  fi
fi
ok "C compiler"

CLAUDE_BIN="$(command -v claude 2>/dev/null || true)"
if [ -z "$CLAUDE_BIN" ]; then
  for c in "$HOME/.local/bin/claude" "$HOME/.claude/local/claude" /opt/homebrew/bin/claude /usr/local/bin/claude; do
    [ -x "$c" ] && CLAUDE_BIN="$c" && break
  done
fi
if [ -n "$CLAUDE_BIN" ]; then
  ok "Claude Code CLI: $CLAUDE_BIN"
else
  warn "Claude Code CLI not found. Install it (https://claude.com/claude-code) and log in;"
  warn "then set its path under Settings → Claude in the dashboard."
fi

if command -v rclone >/dev/null 2>&1; then
  ok "rclone (for Google Drive backups)"
else
  warn "rclone not installed — only needed for Google Drive backups."
  [ "$OS" = Darwin ] && info "${DIM}brew install rclone${RESET}" || info "${DIM}https://rclone.org/install/${RESET}"
fi

# ── 2. build and test ─────────────────────────────────────────────────────
step "Building"
( cd "$REPO_DIR" && go build -o "$BINARY" . ) || fail "Build failed — see the Go errors above."
ok "built $BINARY"

if confirm "Run the test suite (about a minute)?" y; then
  ( cd "$REPO_DIR" && go test -count=1 ./... >/dev/null ) && ok "tests pass" || fail "Tests failed — run: go test ./..."
fi

if [ -d "$REPO_DIR/.git" ] && [ "$(git -C "$REPO_DIR" config core.hooksPath || true)" != ".githooks" ]; then
  info "${DIM}Contributing? Run scripts/install-hooks.sh to block personal data from commits.${RESET}"
fi

# ── 3. configuration ──────────────────────────────────────────────────────
step "Configuration"
mkdir -p "$DATA_DIR"
chmod 700 "$DATA_DIR"
if [ -f "$CONFIG_FILE" ]; then
  ok "using $CONFIG_FILE"
else
  # Seed the CLI path so brains work under launchd/systemd's minimal PATH.
  "$BINARY" init --config "$CONFIG_FILE" ${CLAUDE_BIN:+--claude-path "$CLAUDE_BIN"}
  ok "wrote a starter config to $CONFIG_FILE"
fi

# ── 4. background service ─────────────────────────────────────────────────
SERVICE=false
if ! $NO_SERVICE; then
  step "Background service"
  if [ "$OS" = Darwin ]; then
    if confirm "Start the bridge at login with launchd (recommended)?" y; then
      PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
      mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs/fridolin"
      sed -e "s|{{BINARY}}|$BINARY|g" \
          -e "s|{{WORKDIR}}|$DATA_DIR|g" \
          -e "s|{{CONFIG}}|$CONFIG_FILE|g" \
          -e "s|{{HOME}}|$HOME|g" \
          "$REPO_DIR/deploy/launchd.plist.template" > "$PLIST"
      launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
      # The service's name before the rename.
      OLD_PLIST="$HOME/Library/LaunchAgents/com.whatsapp-claude-bridge.plist"
      if [ -f "$OLD_PLIST" ]; then
        launchctl bootout "gui/$(id -u)/com.whatsapp-claude-bridge" 2>/dev/null || true
        mv "$OLD_PLIST" "$OLD_PLIST.replaced-by-fridolin"
      fi
      SERVICE=true
      ok "installed $PLIST"
    fi
  else
    if command -v systemctl >/dev/null 2>&1 && confirm "Start the bridge at login with a systemd user unit (recommended)?" y; then
      UNIT_DIR="$HOME/.config/systemd/user"
      mkdir -p "$UNIT_DIR"
      sed -e "s|{{BINARY}}|$BINARY|g" \
          -e "s|{{WORKDIR}}|$DATA_DIR|g" \
          -e "s|{{CONFIG}}|$CONFIG_FILE|g" \
          -e "s|{{USER}}|$(id -un)|g" \
          "$REPO_DIR/deploy/systemd.service.template" > "$UNIT_DIR/fridolin.service"
      systemctl --user daemon-reload
      SERVICE=true
      ok "installed $UNIT_DIR/fridolin.service"
      info "${DIM}To keep it running after you log out: sudo loginctl enable-linger $(id -un)${RESET}"
    fi
  fi
fi

# ── 5. start ──────────────────────────────────────────────────────────────
if $SERVICE; then
  step "Starting"
  if [ "$OS" = Darwin ]; then
    launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/$LABEL.plist"
  else
    systemctl --user enable --now fridolin
  fi
  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:8765/health" >/dev/null 2>&1 && break
    sleep 1
  done
  if curl -fsS "http://127.0.0.1:8765/health" >/dev/null 2>&1; then
    ok "the bridge is running"
    "$BINARY" open --config "$CONFIG_FILE" || true
  else
    warn "the bridge did not answer yet — check the log:"
    [ "$OS" = Darwin ] && info "tail -f ~/Library/Logs/fridolin/stderr.log" || info "journalctl --user -u fridolin -f"
  fi
fi

cat <<EOS

${BOLD}${GREEN}Setup complete.${RESET}

EOS
if $SERVICE; then
  cat <<EOS
 The dashboard is open in your browser (or run: ${BOLD}$BINARY open${RESET}).
EOS
else
  cat <<EOS
 Start the bridge:   ${BOLD}$BINARY --open${RESET}
EOS
fi
cat <<EOS

 In the dashboard:
   1. ${BOLD}Pair WhatsApp${RESET} — scan the QR code with the phone that holds the bridge's
      account (Settings → Linked Devices → Link a Device). Use a second number,
      not your personal one.
   2. ${BOLD}Add a brain${RESET} — pick a folder; it gets a brain.json and a starter prompt.
   3. ${BOLD}Allow your number${RESET} under Settings → Access, then DM the bridge.
   4. Optional: ${BOLD}Backup${RESET} → Connect Google Drive.

EOS
