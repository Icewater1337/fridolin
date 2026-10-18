#!/usr/bin/env python3
"""Deprecated: search a brain's history with `"$FRIDOLIN_BIN" history` now.

This wrapper hands its arguments on to `fridolin history`, for one release,
so that prompts and scripts written for it keep working: the flags are the
same, the default is still one line per hit, and --json still prints a
list of hits. An error is now {"error": ...} on stdout with exit status 1,
as with every other tool.

    history_search.py --since 7d invoice      # fridolin history --text --since 7d invoice
"""

from __future__ import annotations

import json
import subprocess
import sys

import toolkit


def translate(argv: list[str]) -> tuple[list[str], bool]:
    """The fridolin arguments for argv, and whether it asked for JSON."""
    flags = argv[:argv.index("--")] if "--" in argv else argv
    if "--json" in flags:
        return ["history", *argv], True
    return ["history", "--text", *argv], False


def main() -> None:
    args, as_json = translate(sys.argv[1:])
    if not as_json:
        toolkit.run_fridolin("history_search.py", args)
    # fridolin history prints {"hits": [...], ...}; this tool printed the list.
    exe = toolkit.fridolin_binary()
    toolkit.log('note: history_search.py is deprecated and goes away in the next release; '
                'run "$FRIDOLIN_BIN" history instead.')
    r = subprocess.run([exe, *args], stdout=subprocess.PIPE, text=True)
    try:
        hits = json.loads(r.stdout)["hits"] if r.returncode == 0 else None
    except (ValueError, KeyError, TypeError):
        hits = None
    if hits is None:
        # An error, or no hits to unwrap: -h prints only the usage, on stderr.
        sys.stdout.write(r.stdout)
        sys.exit(r.returncode)
    print(json.dumps(hits, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
