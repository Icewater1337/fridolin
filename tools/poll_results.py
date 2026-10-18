#!/usr/bin/env python3
"""Deprecated: a brain's poll results are `"$FRIDOLIN_BIN" polls` now.

The votes are in the bridge's bridge.db, which the sandbox denies every
brain, so `fridolin polls` asks the bridge, which answers with the polls of
the brain's own chats. This wrapper hands its arguments on, for one release,
so that prompts written for it keep working: --id, --recent and --chat, and
the JSON, are the same. It works inside a brain's run only.

    poll_results.py --recent 5        # fridolin polls --recent 5
"""

from __future__ import annotations

import sys

import toolkit


def main() -> None:
    toolkit.run_fridolin("poll_results.py", ["polls", *sys.argv[1:]])


if __name__ == "__main__":
    main()
