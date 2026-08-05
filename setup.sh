#!/usr/bin/env bash
#
# Interactive setup for the WhatsApp–Claude bridge.
#
# Walks through prerequisites, writes config.json, scaffolds each brain's
# working directory, builds the binary, and optionally installs a background
# service. Safe to re-run: it shows current values as defaults and asks before
# overwriting anything.
#
#   ./setup.sh              # full guided setup
#   ./setup.sh --reconfigure # only rewrite config.json
#
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG_FILE="$REPO_DIR/config.json"
BINARY="$REPO_DIR/whatsapp-claude-bridge"
DEFAULT_DATA_DIR="$HOME/.whatsapp-claude-bridge"

RECONFIGURE_ONLY=false
[[ "${1:-}" == "--reconfigure" ]] && RECONFIGURE_ONLY=true

# ── input source ──────────────────────────────────────────────────────────
# Prompts read from fd 3. When stdin is a terminal that's just stdin; when the
# script is piped (curl … | bash) we reopen the terminal; and when there is no
# terminal at all — CI, a scripted run — we fall back to stdin so answers can
# be piped in.
# The probe runs in a subshell because a failed exec redirection would
# otherwise kill this script outright.
if [[ -t 0 ]]; then
  exec 3<&0
elif ( exec 3</dev/tty ) 2>/dev/null; then
  exec 3</dev/tty
else
  exec 3<&0
fi

# ── output helpers ────────────────────────────────────────────────────────
if [[ -t 1 ]]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; RED=$'\033[31m'; GREEN=$'\033[32m'
  YELLOW=$'\033[33m'; BLUE=$'\033[34m'; RESET=$'\033[0m'
else
  BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; BLUE=""; RESET=""
fi

step()  { printf '\n%s▸ %s%s\n' "$BOLD$BLUE" "$*" "$RESET"; }
info()  { printf '  %s\n' "$*"; }
hint()  { printf '  %s%s%s\n' "$DIM" "$*" "$RESET"; }
ok()    { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
fail()  { printf '\n%s✗ %s%s\n\n' "$RED" "$*" "$RESET" >&2; exit 1; }

# ask PROMPT DEFAULT -> echoes the answer
ask() {
  local prompt="$1" default="${2:-}" answer
  if [[ -n "$default" ]]; then
    read -r -u 3 -p "  $prompt [$default]: " answer
    echo "${answer:-$default}"
  else
    read -r -u 3 -p "  $prompt: " answer
    echo "$answer"
  fi
}

# confirm PROMPT DEFAULT(y|n) -> exit status
# Lowercasing goes through tr rather than ${var,,} so this runs on the bash 3.2
# that macOS still ships.
confirm() {
  local prompt="$1" default="${2:-y}" answer hintstr="[Y/n]"
  [[ "$default" == "n" ]] && hintstr="[y/N]"
  read -r -u 3 -p "  $prompt $hintstr " answer
  answer="$(printf '%s' "${answer:-$default}" | tr '[:upper:]' '[:lower:]')"
  [[ "$answer" == y* ]]
}

# json_escape STRING -> JSON-safe string body (no surrounding quotes)
json_escape() {
  python3 -c 'import json,sys; sys.stdout.write(json.dumps(sys.argv[1])[1:-1])' "$1"
}

# json_array ITEM... -> ["a","b"]
json_array() {
  local out="" item
  for item in "$@"; do
    [[ -z "$item" ]] && continue
    [[ -n "$out" ]] && out+=", "
    out+="\"$(json_escape "$item")\""
  done
  echo "[$out]"
}

printf '\n%s╭──────────────────────────────────────────────╮%s\n' "$BOLD" "$RESET"
printf '%s│   WhatsApp–Claude bridge — guided setup      │%s\n' "$BOLD" "$RESET"
printf '%s╰──────────────────────────────────────────────╯%s\n' "$BOLD" "$RESET"

# ══════════════════════════════════════════════════════════════════════════
# 1. Prerequisites
# ══════════════════════════════════════════════════════════════════════════
step "Checking prerequisites"

command -v python3 >/dev/null || fail "python3 not found. Install Python 3.9+ and re-run."
ok "python3 — $(python3 --version 2>&1)"

if command -v go >/dev/null; then
  ok "go — $(go version | awk '{print $3}')"
else
  warn "Go is not installed. It is needed to build the bridge."
  hint "macOS:  brew install go"
  hint "Linux:  https://go.dev/dl/"
  fail "Install Go and re-run."
fi

# Locate the Claude Code CLI.
step "Locating the Claude Code CLI"

CLAUDE_PATH=""
if [[ -f "$CONFIG_FILE" ]]; then
  CLAUDE_PATH="$(python3 -c 'import json,sys
try: print(json.load(open(sys.argv[1])).get("claude_path",""))
except Exception: print("")' "$CONFIG_FILE")"
fi

if [[ -z "$CLAUDE_PATH" ]] && command -v claude >/dev/null; then
  CLAUDE_PATH="$(command -v claude)"
fi

# The macOS desktop app ships the CLI inside a versioned bundle.
if [[ -z "$CLAUDE_PATH" ]]; then
  APP_DIR="$HOME/Library/Application Support/Claude/claude-code"
  if [[ -d "$APP_DIR" ]]; then
    LATEST="$(ls "$APP_DIR" 2>/dev/null | sort -V | tail -1)"
    CANDIDATE="$APP_DIR/$LATEST/claude.app/Contents/MacOS/claude"
    [[ -x "$CANDIDATE" ]] && CLAUDE_PATH="$CANDIDATE"
  fi
fi

if [[ -n "$CLAUDE_PATH" ]]; then
  ok "found: $CLAUDE_PATH"
  CLAUDE_PATH="$(ask "Claude CLI path" "$CLAUDE_PATH")"
else
  warn "Could not find the Claude Code CLI automatically."
  hint "Install it from https://claude.com/claude-code, then give the path here."
  hint "If 'claude' is on your PATH, just enter: claude"
  CLAUDE_PATH="$(ask "Claude CLI path" "claude")"
fi

if ! command -v "$CLAUDE_PATH" >/dev/null && [[ ! -x "$CLAUDE_PATH" ]]; then
  warn "'$CLAUDE_PATH' is not executable right now — saving it anyway."
  hint "Fix the path in config.json before starting the bridge."
fi

# ══════════════════════════════════════════════════════════════════════════
# 2. WhatsApp account
# ══════════════════════════════════════════════════════════════════════════
step "WhatsApp account"

cat <<'EOS'
  The bridge links to a WhatsApp account as a companion device, exactly like
  WhatsApp Web. You will scan a QR code at the end of this setup.

  Strongly recommended: use a SECOND phone number for the bridge, not your
  personal one. The bridge replies automatically, and a dedicated number keeps
  your own chats out of it.
EOS
echo

info "Which numbers may talk to the bridge in a direct message?"
hint "Country code, no + and no spaces — e.g. 15551234567"
hint "Comma-separate several. Leave empty to allow anyone who can message the account."
ALLOWED_NUMBERS_RAW="$(ask "Allowed numbers" "")"

IFS=',' read -ra NUMS <<< "$ALLOWED_NUMBERS_RAW"
CLEAN_NUMS=()
for n in "${NUMS[@]:-}"; do
  n="$(echo "$n" | tr -cd '0-9')"
  [[ -n "$n" ]] && CLEAN_NUMS+=("$n")
done
if [[ ${#CLEAN_NUMS[@]} -eq 0 ]]; then
  warn "No allowlist set — anyone who can DM this account will reach Claude."
fi

echo
info "Group chats are opt-in: the bridge ignores every group not listed here."
hint "You usually don't know the group JID yet. Leave this empty, start the"
hint "bridge, send one message in the group, and the JID appears in the log."
ALLOWED_GROUPS_RAW="$(ask "Allowed group JIDs (…@g.us)" "")"

IFS=',' read -ra GRPS <<< "$ALLOWED_GROUPS_RAW"
CLEAN_GROUPS=()
for g in "${GRPS[@]:-}"; do
  g="$(echo "$g" | tr -d '[:space:]')"
  [[ -n "$g" ]] && CLEAN_GROUPS+=("$g")
done

echo
info "In a group, the bridge answers only when addressed."
hint "It always answers an @-mention of its own account. These keywords work too."
TRIGGERS_RAW="$(ask "Trigger keywords (comma-separated)" "@claude")"
IFS=',' read -ra TRIGS <<< "$TRIGGERS_RAW"
CLEAN_TRIGGERS=()
for t in "${TRIGS[@]}"; do
  t="$(echo "$t" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"
  [[ -n "$t" ]] && CLEAN_TRIGGERS+=("$t")
done
[[ ${#CLEAN_TRIGGERS[@]} -eq 0 ]] && CLEAN_TRIGGERS=("@claude")

# ══════════════════════════════════════════════════════════════════════════
# 3. Brains
# ══════════════════════════════════════════════════════════════════════════
step "Brains"

cat <<'EOS'
  A "brain" is one isolated assistant: its own working directory, its own
  system prompt, its own memory, its own conversation history. Brains cannot
  read each other's files.

  One brain per context you want kept separate — for example a private one for
  your DMs and a shared one for a group chat.
EOS
echo

BRAIN_ROOT="$(ask "Where should brain directories live?" "$HOME/assistant")"
BRAIN_ROOT="${BRAIN_ROOT/#\~/$HOME}"

# The sandbox denies reads into these, so a brain living inside one could not
# read its own prompt or memories.
for denied in Documents Downloads Desktop Pictures Movies Music; do
  if [[ "$BRAIN_ROOT" == "$HOME/$denied"* ]]; then
    warn "$BRAIN_ROOT sits inside ~/$denied, which the sandbox blocks."
    hint "The brain would not be able to read its own files. Pick somewhere else,"
    hint "or turn the sandbox off later in config.json."
    BRAIN_ROOT="$(ask "Where should brain directories live?" "$HOME/assistant")"
    BRAIN_ROOT="${BRAIN_ROOT/#\~/$HOME}"
    break
  fi
done

BRAIN_NAMES=()
BRAIN_DIRS=()
BRAIN_DMS=()
BRAIN_GROUPS=()

echo
info "First brain — this one will handle your direct messages."
name="$(ask "Brain name" "personal")"
BRAIN_NAMES+=("$name")
BRAIN_DIRS+=("$BRAIN_ROOT/$name")
BRAIN_DMS+=("true")
BRAIN_GROUPS+=("")

while confirm "Add another brain?" "n"; do
  echo
  name="$(ask "Brain name" "work")"
  # Reject duplicates: two brains with one name would collide in the DB.
  dup=false
  for existing in "${BRAIN_NAMES[@]}"; do
    [[ "$existing" == "$name" ]] && dup=true
  done
  if $dup; then
    warn "A brain named '$name' already exists — skipping."
    continue
  fi

  groups="$(ask "Group JIDs for '$name' (comma-separated, can be empty)" "")"
  BRAIN_NAMES+=("$name")
  BRAIN_DIRS+=("$BRAIN_ROOT/$name")
  BRAIN_DMS+=("false")
  BRAIN_GROUPS+=("$groups")
done

# Any group assigned to a brain must also be in allowed_groups, or the bridge
# would ignore it before routing ever happens. Fold them in rather than leaving
# a config that looks right and silently does nothing.
for groups in "${BRAIN_GROUPS[@]}"; do
  IFS=',' read -ra gs <<< "$groups"
  for g in "${gs[@]:-}"; do
    g="$(echo "$g" | tr -d '[:space:]')"
    [[ -z "$g" ]] && continue
    already=false
    for existing in "${CLEAN_GROUPS[@]:-}"; do
      [[ "$existing" == "$g" ]] && already=true
    done
    $already || CLEAN_GROUPS+=("$g")
  done
done

# ══════════════════════════════════════════════════════════════════════════
# 4. Runtime options
# ══════════════════════════════════════════════════════════════════════════
step "Runtime options"

DATA_DIR="$(ask "Data directory (database, media, credentials)" "$DEFAULT_DATA_DIR")"
DATA_DIR="${DATA_DIR/#\~/$HOME}"

echo
info "Model for the brains — leave empty for the CLI default."
hint "e.g. sonnet, opus, haiku"
BRAIN_MODEL="$(ask "Model" "")"

echo
SANDBOX_ENABLED=false
if [[ "$(uname)" == "Darwin" ]]; then
  cat <<'EOS'
  The bridge can wrap each Claude run in macOS sandbox-exec. That stops a
  message crafted to hijack the assistant from reading your SSH keys, browser
  data, mail, or another brain's files. Recommended.
EOS
  confirm "Enable the sandbox?" "y" && SANDBOX_ENABLED=true
else
  hint "Sandboxing uses macOS sandbox-exec and is unavailable on $(uname)."
fi

echo
HTTP_LISTEN="127.0.0.1:8765"
if confirm "Enable the local HTTP API for sending messages from scripts?" "y"; then
  HTTP_LISTEN="$(ask "Listen address (localhost only)" "127.0.0.1:8765")"
else
  HTTP_LISTEN=""
fi

echo
OTP_GUARD=true
cat <<'EOS'
  The OTP guard drops a message that is nothing but a numeric code when the
  assistant just asked for one, so a two-factor code typed into the chat is
  never written to history or sent to the model.
EOS
confirm "Enable the OTP guard?" "y" || OTP_GUARD=false

# ══════════════════════════════════════════════════════════════════════════
# 5. Write config.json
# ══════════════════════════════════════════════════════════════════════════
step "Writing configuration"

if [[ -f "$CONFIG_FILE" ]]; then
  BACKUP="$CONFIG_FILE.backup.$(date +%s)"
  cp "$CONFIG_FILE" "$BACKUP"
  ok "existing config backed up to $(basename "$BACKUP")"
fi

BRAINS_JSON=""
for i in "${!BRAIN_NAMES[@]}"; do
  [[ -n "$BRAINS_JSON" ]] && BRAINS_JSON+=",
"
  IFS=',' read -ra gs <<< "${BRAIN_GROUPS[$i]}"
  clean_gs=()
  for g in "${gs[@]:-}"; do
    g="$(echo "$g" | tr -d '[:space:]')"
    [[ -n "$g" ]] && clean_gs+=("$g")
  done
  BRAINS_JSON+="    {
      \"name\": \"$(json_escape "${BRAIN_NAMES[$i]}")\",
      \"work_dir\": \"$(json_escape "${BRAIN_DIRS[$i]}")\",
      \"handles_dms\": ${BRAIN_DMS[$i]},
      \"groups\": $(json_array "${clean_gs[@]:-}")
    }"
done

cat > "$CONFIG_FILE" <<EOF
{
  "allowed_numbers": $(json_array "${CLEAN_NUMS[@]:-}"),
  "allowed_groups": $(json_array "${CLEAN_GROUPS[@]:-}"),
  "triggers": $(json_array "${CLEAN_TRIGGERS[@]}"),

  "claude_path": "$(json_escape "$CLAUDE_PATH")",
  "data_dir": "$(json_escape "$DATA_DIR")",

  "brain_mode": true,
  "brain_model": "$(json_escape "$BRAIN_MODEL")",
  "brains": [
$BRAINS_JSON
  ],

  "sandbox": {
    "enabled": $SANDBOX_ENABLED,
    "deny_paths": [],
    "allow_paths": []
  },

  "dangerously_skip_permissions": true,
  "otp_guard": $OTP_GUARD,
  "viewer": false,
  "http_listen": "$(json_escape "$HTTP_LISTEN")",
  "max_chunk_size": 3900,
  "log_level": "info"
}
EOF

chmod 600 "$CONFIG_FILE"
ok "wrote $CONFIG_FILE"

mkdir -p "$DATA_DIR"
chmod 700 "$DATA_DIR"
ok "data directory ready: $DATA_DIR"

# ══════════════════════════════════════════════════════════════════════════
# 6. Scaffold brain directories
# ══════════════════════════════════════════════════════════════════════════
step "Setting up brain directories"

for i in "${!BRAIN_NAMES[@]}"; do
  dir="${BRAIN_DIRS[$i]}"
  name="${BRAIN_NAMES[$i]}"
  mkdir -p "$dir/memories/people"

  prompt_file="$dir/brain-prompt.md"
  if [[ -f "$prompt_file" ]]; then
    hint "$name: brain-prompt.md already exists — left untouched"
  else
    sed "s/{{BRAIN_NAME}}/$name/g" "$REPO_DIR/examples/brain-prompt.example.md" > "$prompt_file"
    ok "$name: created $prompt_file"
  fi

  identity_map="$dir/memories/people/whatsapp-identity-map.md"
  if [[ ! -f "$identity_map" ]]; then
    cat > "$identity_map" <<'EOS'
# WhatsApp identity map

Maps WhatsApp numbers to names, so chat history reads as names instead of raw
numbers. Optional — the bridge works fine without it.

| WhatsApp Number | Person | Notes |
| --------------- | ------ | ----- |
EOS
  fi
done

# ══════════════════════════════════════════════════════════════════════════
# 7. Build
# ══════════════════════════════════════════════════════════════════════════
if ! $RECONFIGURE_ONLY; then
  step "Building the bridge"
  info "This takes a minute the first time (downloading dependencies)…"
  if (cd "$REPO_DIR" && go build -o "$BINARY" .); then
    ok "built $BINARY"
  else
    fail "Build failed. Check the Go errors above."
  fi

  step "Validating the configuration"
  if "$BINARY" --config "$CONFIG_FILE" --check; then
    ok "configuration is valid"
  else
    fail "Configuration check failed."
  fi
fi

# ══════════════════════════════════════════════════════════════════════════
# 8. Optional extras
# ══════════════════════════════════════════════════════════════════════════
step "Optional: Google Calendar"

if confirm "Show the Google Calendar setup steps?" "n"; then
  cat <<EOS

  1. Install the Python packages:
       pip3 install -r "$REPO_DIR/tools/requirements.txt"

  2. In console.cloud.google.com:
       - create a project and enable the Google Calendar API
       - APIs & Services → Credentials → Create OAuth 2.0 Client ID (Desktop app)
       - download the JSON

  3. Save it as one file per brain, named after the brain:
EOS
  for name in "${BRAIN_NAMES[@]}"; do
    echo "       $DATA_DIR/google_calendar_credentials_$name.json"
  done
  cat <<EOS

  4. Authorise once per brain (a browser window opens):
       python3 "$REPO_DIR/tools/calendar_tool.py" --profile ${BRAIN_NAMES[0]} list

  The sandbox grants each brain only its own calendar files.

EOS
fi

step "Optional: run in the background"

if [[ "$(uname)" == "Darwin" ]]; then
  if confirm "Install a launchd service so the bridge starts at login?" "n"; then
    PLIST_SRC="$REPO_DIR/deploy/launchd.plist.template"
    PLIST_DST="$HOME/Library/LaunchAgents/com.whatsapp-claude-bridge.plist"
    mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs/whatsapp-claude-bridge"
    sed -e "s|{{BINARY}}|$BINARY|g" \
        -e "s|{{WORKDIR}}|$REPO_DIR|g" \
        -e "s|{{CONFIG}}|$CONFIG_FILE|g" \
        -e "s|{{HOME}}|$HOME|g" \
        "$PLIST_SRC" > "$PLIST_DST"
    ok "wrote $PLIST_DST"
    hint "Pair the bridge first (below), then start it with:"
    hint "  launchctl load $PLIST_DST"
  fi
else
  if confirm "Show the systemd unit for running the bridge as a service?" "n"; then
    sed -e "s|{{BINARY}}|$BINARY|g" \
        -e "s|{{WORKDIR}}|$REPO_DIR|g" \
        -e "s|{{CONFIG}}|$CONFIG_FILE|g" \
        -e "s|{{USER}}|$(id -un)|g" \
        "$REPO_DIR/deploy/systemd.service.template"
    echo
    hint "Save that to ~/.config/systemd/user/whatsapp-claude-bridge.service"
  fi
fi

# ══════════════════════════════════════════════════════════════════════════
# Done
# ══════════════════════════════════════════════════════════════════════════
cat <<EOS

$BOLD$GREEN Setup complete.$RESET

 ${BOLD}Next: pair with WhatsApp${RESET}

   cd "$REPO_DIR"
   ./whatsapp-claude-bridge

 A QR code appears in the terminal. On the phone holding the bridge's WhatsApp
 account: Settings → Linked Devices → Link a Device, and scan it. The pairing
 is stored, so this is a one-time step.

 ${BOLD}Then try it${RESET}

   • DM the bridge's number from an allowed number.
   • For a group: add the bridge account to the group, send any message there,
     and copy the group JID from the bridge's log into "allowed_groups" in
     config.json. Restart, then say "${CLEAN_TRIGGERS[0]} hello".

 ${BOLD}Useful${RESET}

   ./whatsapp-claude-bridge --check     validate config
   ./setup.sh --reconfigure             rewrite config.json
   docs/CONFIGURATION.md                every option explained
   $(printf '%s' "${BRAIN_DIRS[0]}")/brain-prompt.md
                                        the assistant's personality — edit this

EOS
