"""What the Python tools in tools/ share.

Every tool prints one JSON document on stdout, which the assistant reads,
and reports an error as {"error": "..."} on stdout with exit status 1, in
the same place. Progress and warnings go to stderr.

calendar_tool.py, poll_results.py and history_search.py are wrappers around
the bridge's own commands now (fridolin calendar, polls and history), kept
for one release so that prompts and scripts written for them keep working.
"""

from __future__ import annotations

import json
import os
import shutil
import sys
from pathlib import Path
from typing import Any, NoReturn

TOOLS_DIR = Path(__file__).resolve().parent


def fail(msg: str) -> NoReturn:
    """Report msg as the command's JSON error and exit with status 1."""
    emit_json({"error": msg})
    sys.exit(1)


def emit_json(doc: Any) -> None:
    """Print doc as the command's one JSON document."""
    print(json.dumps(doc, ensure_ascii=False, indent=2))


def log(msg: str) -> None:
    """Progress and warnings go to stderr, so stdout stays one JSON document."""
    print(msg, file=sys.stderr)


def fridolin_binary() -> str:
    """The bridge's binary: FRIDOLIN_BIN, which every brain's run sets, else
    fridolin on PATH, else the one next to tools/, as in the folder
    `fridolin service install` fills, else the checkout's `make build` in
    bin/."""
    if exe := os.environ.get("FRIDOLIN_BIN"):
        return exe
    if exe := shutil.which("fridolin"):
        return exe
    for built in (TOOLS_DIR.parent / "fridolin", TOOLS_DIR.parent / "bin" / "fridolin"):
        if built.is_file() and os.access(built, os.X_OK):
            return str(built)
    fail("Cannot find the fridolin binary: run this in a brain, where FRIDOLIN_BIN is set, "
         "put fridolin on PATH, or build it with make build.")


def run_fridolin(old: str, args: list[str]) -> NoReturn:
    """Run `fridolin ARGS` in place of this process, after a note on stderr
    that old is deprecated. Its output and exit status are the tool's."""
    exe = fridolin_binary()
    log(f'note: {old} is deprecated and goes away in the next release; run "$FRIDOLIN_BIN" {args[0]} instead.')
    sys.stdout.flush()
    try:
        os.execv(exe, [exe, *args])
    except OSError as e:
        fail(f"Cannot run {exe}: {e}")
