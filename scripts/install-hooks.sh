#!/usr/bin/env bash
# Turns on the repository's git hooks (.githooks/): a personal-data check on
# every commit and every push. Run once per clone if you plan to contribute.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
git config core.hooksPath .githooks
chmod +x .githooks/* scripts/*.sh
echo "Hooks enabled. Put your own names, numbers and emails in"
echo "  ~/.config/whatsapp-claude-bridge/personal-denylist"
echo "(one per line) so they can never be committed."
