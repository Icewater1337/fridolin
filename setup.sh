#!/usr/bin/env bash
#
# Sets up Fridolin on this machine, or upgrades it after a `git pull`. The
# checkout only builds: `fridolin service install` copies the build to
# ~/.local/lib/fridolin and runs it from there. `./setup.sh --help` says more.

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./setup.sh [--yes] [--no-service] [--skip-tests]

Sets up Fridolin on this machine, or upgrades it after `git pull`:
  1. checks the prerequisites (Go, a C compiler, the Claude CLI; rclone optional)
  2. builds the bridge from this checkout and runs the tests on the build
  3. writes a starter config if there is none
  4. installs the build to ~/.local/lib/fridolin, keeping the one it replaces
     as fridolin.prev, installs the background service (a launchd agent on
     macOS, a systemd user unit on Linux), restarts it and waits for the bridge
  5. opens the dashboard

Pairing WhatsApp, adding brains, allowing chats and Google Drive backups
happen in the dashboard. Safe to re-run: configuration and brains stay. A
failed build or test run installs nothing.

Options:
  -y, --yes       accept every default: run the tests, install, restart
  --no-service    build into ./bin/fridolin and stop there; run it yourself
  --skip-tests    install without running the test suite first
  -h, --help      show this help

Environment:
  DATA_DIR        data folder for a new config (default ~/.fridolin)
  FRIDOLIN_CONFIG config file (default $DATA_DIR/config.json)
  INSTALL_DIR     install folder (default ~/.local/lib/fridolin)

Afterwards:
  ~/.local/lib/fridolin/fridolin service status     what runs, and is it up
  ~/.local/lib/fridolin/fridolin service rollback   back to the previous build
EOF
}

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/lib/fridolin}"
# A data folder given by the caller goes into a new config: the service
# never sees this script's environment.
DATA_DIR_GIVEN="${DATA_DIR:-}"
# Installs from before the rename keep their data folder.
if [ -z "${DATA_DIR:-}" ] && [ ! -d "$HOME/.fridolin" ] && [ -d "$HOME/.whatsapp-claude-bridge" ]; then
  DATA_DIR="$HOME/.whatsapp-claude-bridge"
fi
DATA_DIR="${DATA_DIR:-$HOME/.fridolin}"
# The config file the service is given: FRIDOLIN_CONFIG, else the data dir's.
CONFIG="${FRIDOLIN_CONFIG:-$DATA_DIR/config.json}"

ASSUME_YES=false
NO_SERVICE=false
SKIP_TESTS=false
for arg in "$@"; do
  case "$arg" in
    -y|--yes) ASSUME_YES=true ;;
    --no-service) NO_SERVICE=true ;;
    --skip-tests) SKIP_TESTS=true ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $arg" >&2; usage >&2; exit 2 ;;
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

# CONFIG_FILE was the old name of FRIDOLIN_CONFIG, but other software
# exports it too. Going on without it would point the service at another
# config than the one it was set up with, and only the person running this
# knows which of the two it is, so stop and name both ways on.
if [ -n "${CONFIG_FILE:-}" ]; then
  fail "CONFIG_FILE is no longer read. If it points at Fridolin's config, re-run as FRIDOLIN_CONFIG=\"$CONFIG_FILE\" $0 $*; if it belongs to other software, re-run as env -u CONFIG_FILE $0 $*"
fi

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

# The oldest Go release that builds the bridge is the go directive in go.mod.
GO_MIN="$(sed -nE 's/^go (1\.[0-9]+).*/\1/p' "$REPO_DIR/go.mod")"
if ! command -v go >/dev/null 2>&1; then
  fail "Go is not installed. Install Go ${GO_MIN}+ (macOS: brew install go; Linux: https://go.dev/dl/), then re-run."
fi
# Ask from the repo folder, where the build below runs: there go may switch
# to the release go.mod asks for (GOTOOLCHAIN=auto), so the answer must not
# depend on where setup.sh was started from.
GO_VERSION="$(cd "$REPO_DIR" && go env GOVERSION)" || fail "Could not run go in $REPO_DIR (see the error above)."
GO_MINOR="$(printf '%s\n' "$GO_VERSION" | sed -E 's/^go1\.([0-9]+).*/\1/')"
if [ "${GO_MINOR:-0}" -lt "${GO_MIN#1.}" ]; then
  fail "Go $GO_VERSION is too old; the bridge needs Go ${GO_MIN} or newer."
fi
ok "Go $GO_VERSION"

if [ "$OS" = Darwin ]; then
  # /usr/bin/cc exists without the Command Line Tools too: it only offers
  # to install them, and the cgo build fails later.
  xcode-select -p >/dev/null 2>&1 || fail "No C compiler (needed for SQLite). Run: xcode-select --install"
elif ! command -v cc >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1 && ! command -v clang >/dev/null 2>&1; then
  fail "No C compiler (needed for SQLite). Install build-essential (Debian/Ubuntu) or gcc."
fi
ok "C compiler"

CLAUDE_BIN="$(command -v claude 2>/dev/null || true)"
if [ -z "$CLAUDE_BIN" ]; then
  for c in "$HOME/.local/bin/claude" "$HOME/.claude/local/claude" /opt/homebrew/bin/claude /usr/local/bin/claude; do
    [ -x "$c" ] && CLAUDE_BIN="$c" && break
  done
fi
if [ -z "$CLAUDE_BIN" ]; then
  warn "Claude Code CLI not found. Install it (https://claude.com/claude-code) and log in;"
  warn "then set its path under Settings → Claude in the dashboard."
fi

if command -v rclone >/dev/null 2>&1; then
  ok "rclone (for Google Drive backups)"
else
  warn "rclone not installed — only needed for Google Drive backups."
  if [ "$OS" = Darwin ]; then info "${DIM}brew install rclone${RESET}"; else info "${DIM}https://rclone.org/install/${RESET}"; fi
fi

# ── 2. build and test ─────────────────────────────────────────────────────
step "Building"
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/fridolin-setup.XXXXXX")"
trap 'rm -rf "$BUILD_DIR"' EXIT
VERSION="$(git -C "$REPO_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
NEW="$BUILD_DIR/fridolin"
# The main package is at the repo root or in ./cmd/fridolin.
PKG=.
if [ -d "$REPO_DIR/cmd/fridolin" ]; then PKG=./cmd/fridolin; fi
( cd "$REPO_DIR" && go build -trimpath -ldflags "-X main.version=$VERSION" -o "$NEW" "$PKG" ) || fail "Build failed — see the Go errors above. Nothing was installed."
ok "built $VERSION"

if ! $SKIP_TESTS && confirm "Run the test suite before installing (recommended, about a minute)?" y; then
  # A name of its own: two runs, or another user's file in a shared /tmp,
  # must not meet.
  TEST_LOG="$(mktemp "${TMPDIR:-/tmp}/fridolin-setup-tests.XXXXXX")"
  if ( cd "$REPO_DIR" && go test -count=1 ./... ) >"$TEST_LOG" 2>&1; then
    rm -f "$TEST_LOG"
    ok "tests pass"
  else
    tail -n 30 "$TEST_LOG" | sed 's/^/    /'
    fail "Tests failed, so nothing was installed. Full output: $TEST_LOG"
  fi
fi

if [ -d "$REPO_DIR/.git" ] && [ "$(git -C "$REPO_DIR" config core.hooksPath || true)" != ".githooks" ]; then
  info "${DIM}Contributing? Run scripts/install-hooks.sh to block personal data from commits.${RESET}"
fi

# ── 3. configuration ──────────────────────────────────────────────────────
step "Configuration"
# A new data folder is private from the start. An existing one is the
# bridge's to fix when it starts: it leaves the home folder (or one above
# it) and another user's folder alone, which a chmod here would not.
mkdir -p "$(dirname "$DATA_DIR")"
[ -d "$DATA_DIR" ] || mkdir -m 700 "$DATA_DIR"
if [ -f "$CONFIG" ]; then
  ok "using $CONFIG"
else
  # Seed the CLI path so brains work under launchd/systemd's minimal PATH.
  "$NEW" init --config "$CONFIG" ${CLAUDE_BIN:+--claude-path "$CLAUDE_BIN"} ${DATA_DIR_GIVEN:+--data-dir "$DATA_DIR_GIVEN"}
  ok "wrote a starter config to $CONFIG"
fi
# A config this build does not load (inline brains, say) stops here,
# before anything is installed, with check's error above. Its claude line
# is the CLI brains will run, with the version checked against the
# release the bridge was tested with.
CHECK_OUT="$("$NEW" check --config "$CONFIG")" || fail "This build cannot load $CONFIG (see the error above). Nothing was installed."
CLAUDE_LINE="$(printf '%s\n' "$CHECK_OUT" | sed -n 's/^  claude: *//p')"
case "$CLAUDE_LINE" in
  "") ;;
  *"not found"*|*"older than"*) warn "Claude Code CLI: $CLAUDE_LINE" ;;
  *) ok "Claude Code CLI: $CLAUDE_LINE" ;;
esac

# ── 4. install ────────────────────────────────────────────────────────────
INSTALLED=false
FAILED=false
SWAPPED=false
if ! $NO_SERVICE && [ "$OS" != Darwin ] && ! command -v systemctl >/dev/null 2>&1; then
  warn "systemd is not available, so there is no background service to install."
  NO_SERVICE=true
fi
# Whether a bridge answers on the configured address: status exits 0 when
# one does, 4 when http_listen is off and it cannot ask.
STATUS_RC=0
"$NEW" service status --config "$CONFIG" --dir "$INSTALL_DIR" >/dev/null 2>&1 || STATUS_RC=$?
case $STATUS_RC in
  0) BRIDGE=running ;;
  4) BRIDGE=unknown ;;
  *) BRIDGE=down ;;
esac
if ! $NO_SERVICE; then
  step "Installing"
  if [ "$OS" = Darwin ]; then
    PROMPT="Install to $INSTALL_DIR and start the bridge at login with launchd (recommended)?"
  else
    PROMPT="Install to $INSTALL_DIR and start the bridge at login with a systemd user unit (recommended)?"
  fi
  case $BRIDGE in
    running)
      info "A bridge is running. Installing restarts it, which cuts off any reply it is working on."
      PROMPT="Install $VERSION and restart the bridge now?" ;;
    unknown)
      info "If a bridge is running, installing restarts it, which cuts off any reply it is working on." ;;
  esac
  if confirm "$PROMPT" y; then
    # Whether the install replaced another build, so that a rollback has
    # one to go back to.
    REPLACES=false
    if [ -e "$INSTALL_DIR/fridolin" ] && ! cmp -s "$NEW" "$INSTALL_DIR/fridolin"; then REPLACES=true; fi
    INSTALL_RC=0
    "$NEW" service install --config "$CONFIG" --dir "$INSTALL_DIR" --source "$REPO_DIR" || INSTALL_RC=$?
    case $INSTALL_RC in
      0)
        INSTALLED=true
        if [ "$OS" != Darwin ]; then
          info "${DIM}To keep it running after you log out: sudo loginctl enable-linger $(id -un)${RESET}"
        fi ;;
      # Refused before it changed anything: a rollback would swap a
      # working install for the one before it.
      5) fail "The install was refused (see the error above). Nothing was installed, and the bridge that ran before, if any, still runs." ;;
      *)
        FAILED=true
        if $REPLACES && cmp -s "$NEW" "$INSTALL_DIR/fridolin" && [ -e "$INSTALL_DIR/fridolin.prev" ]; then SWAPPED=true; fi ;;
    esac
  else
    NO_SERVICE=true
  fi
fi
if $NO_SERVICE && ! $FAILED; then
  mkdir -p "$REPO_DIR/bin"
  mv -f "$NEW" "$REPO_DIR/bin/fridolin"
  ok "built $REPO_DIR/bin/fridolin (not installed)"
fi

# ── 5. open ───────────────────────────────────────────────────────────────
OPENED=false
if $INSTALLED && "$INSTALL_DIR/fridolin" open --config "$CONFIG" >/dev/null 2>&1; then
  OPENED=true
fi

if $FAILED; then
  if $SWAPPED; then
    fail "The new build is installed, but the install or the restart failed (see above). Go back to the build that ran before with: $INSTALL_DIR/fridolin service rollback"
  fi
  fail "The install or the restart failed (see above)."
fi

cat <<EOS

${BOLD}${GREEN}Setup complete.${RESET}

EOS
if $OPENED; then
  echo " The dashboard is open in your browser (or run: ${BOLD}$INSTALL_DIR/fridolin open${RESET})."
elif $INSTALLED; then
  echo " Open the dashboard: ${BOLD}$INSTALL_DIR/fridolin open${RESET}"
elif [ "$BRIDGE" = running ]; then
  # A second bridge would find the data folder and the port taken.
  echo " Not installed: the running bridge keeps its old build until you re-run ./setup.sh."
  echo " Open its dashboard: ${BOLD}$REPO_DIR/bin/fridolin open${RESET}"
elif [ "$BRIDGE" = unknown ]; then
  # With http_listen off there was no asking whether one runs.
  echo " Not installed: a bridge that is running keeps its old build until you re-run ./setup.sh."
  echo " If none is running, start it: ${BOLD}$REPO_DIR/bin/fridolin --open${RESET}"
else
  echo " Start the bridge:   ${BOLD}$REPO_DIR/bin/fridolin --open${RESET}"
fi
cat <<EOS

 In the dashboard:
   1. ${BOLD}Pair WhatsApp${RESET} — scan the QR code with the phone that holds the bridge's
      account (Settings → Linked Devices → Link a Device). Use a second number,
      not your personal one.
   2. ${BOLD}Add a brain${RESET} — pick a folder; it gets a brain.json and a starter prompt.
   3. ${BOLD}Allow your number${RESET} under Chats → Allowed numbers, then DM the bridge.
   4. Optional: ${BOLD}Backup${RESET} → Connect Google Drive.

EOS
if $INSTALLED; then
  cat <<EOS
 Upgrade later with ${BOLD}git pull && ./setup.sh${RESET}; go back to the previous build
 with ${BOLD}$INSTALL_DIR/fridolin service rollback${RESET}.

EOS
fi
