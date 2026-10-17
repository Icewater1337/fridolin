#!/usr/bin/env python3
"""Search a brain's conversation-history.jsonl by substring or regex.

Each brain appends every message it sees to conversation-history.jsonl in its
working directory. Only the most recent messages are put into the prompt, so
this is how a brain looks further back.

Point it at a history file with --file, or at a brain's working directory with
--brain-dir. Run from inside a brain's working directory and it defaults to
./conversation-history.jsonl, which is what the brain itself will usually do.

Examples:
  # Last 7 days, case-insensitive substring, from the current brain's history
  history_search.py --since 7d "invoice"

  # Regex over another brain's history, JSON output
  history_search.py --brain-dir ~/assistant/work --regex --json "deadlines?"

  # The 20 most recent hits, all time
  history_search.py --file ~/assistant/personal/conversation-history.jsonl --limit 20 "dentist"
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

HISTORY_FILENAME = "conversation-history.jsonl"


def _fromiso(s: str) -> datetime:
    """datetime.fromisoformat, aware, and accepting a trailing Z.

    The bridge writes RFC 3339 timestamps, which end in Z on a host that runs
    in UTC, and fromisoformat only accepts Z from Python 3.11 on. A naive
    timestamp is taken as local time.
    """
    if s.endswith(("Z", "z")):
        s = s[:-1] + "+00:00"
    dt = datetime.fromisoformat(s)
    return dt if dt.tzinfo else dt.astimezone()


def parse_since(s: str) -> datetime:
    """Parse '7d', '24h', '90m', or an ISO timestamp into an aware datetime.

    Used as an argparse type, so a bad value is a usage error rather than a
    traceback.
    """
    s = s.strip()
    if m := re.fullmatch(r"(\d+)\s*([smhdw])", s):
        n, unit = int(m.group(1)), m.group(2)
        delta = {
            "s": timedelta(seconds=n),
            "m": timedelta(minutes=n),
            "h": timedelta(hours=n),
            "d": timedelta(days=n),
            "w": timedelta(weeks=n),
        }[unit]
        return datetime.now(timezone.utc) - delta
    try:
        return _fromiso(s)
    except ValueError:
        raise argparse.ArgumentTypeError(
            f"use 30m, 24h, 7d, 2w or an ISO timestamp such as 2026-01-05T09:00 (got {s!r})") from None


def parse_ts(s: str) -> datetime | None:
    try:
        return _fromiso(s)
    except (ValueError, TypeError, AttributeError):
        return None


def resolve_history(args: argparse.Namespace) -> Path:
    if args.file:
        return Path(args.file).expanduser()
    if args.brain_dir:
        return Path(args.brain_dir).expanduser() / HISTORY_FILENAME
    return Path.cwd() / HISTORY_FILENAME


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("query", help="substring (default) or regex (with --regex)")

    src = ap.add_mutually_exclusive_group()
    src.add_argument("--file", help=f"path to a {HISTORY_FILENAME}")
    src.add_argument("--brain-dir", help="a brain's working directory")

    ap.add_argument("--since", type=parse_since, help="time window: 7d / 24h / 90m / ISO timestamp")
    ap.add_argument("--limit", type=int, default=50, help="max hits (default 50, 0 = unlimited)")
    ap.add_argument("--regex", action="store_true", help="treat the query as a regex")
    ap.add_argument("--case-sensitive", action="store_true", help="default is case-insensitive")
    ap.add_argument("--json", action="store_true", help="emit JSON instead of pretty text")
    ap.add_argument("--chat", help="only messages in this chat JID")
    ap.add_argument("--from-only", action="store_true", help="only inbound (type=in) messages")
    ap.add_argument("--reverse", action="store_true", help="oldest first (default: newest first)")
    args = ap.parse_args(argv)

    path = resolve_history(args)
    if not path.exists():
        print(f"history file not found: {path}", file=sys.stderr)
        print("Pass --file or --brain-dir, or run from a brain's working directory.", file=sys.stderr)
        return 2

    flags = 0 if args.case_sensitive else re.IGNORECASE
    try:
        pat = re.compile(args.query if args.regex else re.escape(args.query), flags)
    except re.error as e:
        print(f"invalid regex: {e}", file=sys.stderr)
        return 2

    since = args.since

    hits: list[dict] = []
    with path.open("r", encoding="utf-8") as f:
        for lineno, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError:
                continue

            text = row.get("text") or ""
            if not text:
                continue
            if args.from_only and row.get("type") != "in":
                continue
            if args.chat and row.get("chat") != args.chat:
                continue
            if since:
                ts = parse_ts(row.get("ts", ""))
                if ts is None or ts < since:
                    continue
            if not pat.search(text):
                continue

            hits.append({"line": lineno, **row})

    if not args.reverse:
        hits.reverse()
    if args.limit > 0:
        hits = hits[: args.limit]

    if args.json:
        json.dump(hits, sys.stdout, ensure_ascii=False, indent=2)
        sys.stdout.write("\n")
        return 0

    for h in hits:
        text = h.get("text", "").replace("\n", " ⏎ ")
        if len(text) > 400:
            text = text[:400] + "…"
        print(f"{h.get('ts', '?')} [{h.get('type', '?')} {h.get('from', '?')}] {text}")
    print(f"\n— {len(hits)} hit(s) — file: {path}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
