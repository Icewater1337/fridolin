#!/usr/bin/env python3
"""Query WhatsApp poll results from the bridge's SQLite database.

The bridge decrypts incoming poll votes and stores them, so the assistant can
read the tally at any time and summarise it in plain language.

Usage:
  poll_results.py                        # the 10 most recent polls
  poll_results.py --id MSG_ID            # one poll by message ID
  poll_results.py --recent 5             # the N most recent polls
  poll_results.py --chat CHAT_JID        # only polls in one chat

--id shows one poll, so it does not combine with --recent or --chat.

Output is JSON: question, options, total_voters, and per-option votes + voters.
total_voters counts people with a current vote; someone who retracted theirs
is left out.

The database location follows BRIDGE_DB_PATH, then BRIDGE_DATA_DIR/bridge.db,
then ~/.fridolin/bridge.db. The bridge sets both variables for brains, so
inside one the tool opens the DB the bridge runs with, wherever data_dir and
db_path put it.
"""

from __future__ import annotations

import argparse
import contextlib
import json
import os
import sqlite3
import sys
from pathlib import Path
from typing import Any


def db_path() -> Path:
    if p := os.environ.get("BRIDGE_DB_PATH"):
        return Path(p)
    # Only the bridge knows an install's data dir (it still finds one from
    # before the rename): outside a brain, name it in BRIDGE_DATA_DIR.
    data_dir = os.environ.get("BRIDGE_DATA_DIR") or Path.home() / ".fridolin"
    return Path(data_dir) / "bridge.db"


def open_db() -> sqlite3.Connection:
    path = db_path()
    if not path.exists():
        print(json.dumps({"error": f"Database not found: {path}"}))
        sys.exit(1)
    # Read-only: this tool must never be able to corrupt the live bridge DB.
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def json_list(text: str) -> list:
    """Decode a JSON array column, treating anything else as empty.

    The bridge stores a retracted vote (every option deselected) as JSON
    null, not as [].
    """
    try:
        value = json.loads(text)
    except (json.JSONDecodeError, TypeError):
        return []
    return value if isinstance(value, list) else []


def tally_votes(conn: sqlite3.Connection, poll_msg_id: str,
                options: list[str]) -> tuple[dict[str, dict[str, Any]], int]:
    """Return per-option counts and voters, plus the number of people who voted.

    Someone who retracted their vote has a row but no selection, and is not
    counted.
    """
    tally: dict[str, dict[str, Any]] = {opt: {"count": 0, "voters": []} for opt in options}
    rows = conn.execute(
        "SELECT voter_jid, selected_options FROM poll_votes WHERE poll_msg_id = ?",
        (poll_msg_id,),
    ).fetchall()
    voters = 0
    for row in rows:
        chosen = json_list(row["selected_options"])
        if chosen:
            voters += 1
        for opt in chosen:
            if opt in tally:
                tally[opt]["count"] += 1
                tally[opt]["voters"].append(row["voter_jid"])
    return tally, voters


def format_poll(conn: sqlite3.Connection, row: sqlite3.Row) -> dict:
    options = json_list(row["options"])
    tally, total_voters = tally_votes(conn, row["msg_id"], options)
    return {
        "msg_id": row["msg_id"],
        "chat_jid": row["chat_jid"],
        "question": row["question"],
        "options": options,
        "total_voters": total_voters,
        "results": [
            {
                "option": opt,
                "votes": tally[opt]["count"],
                "voters": tally[opt]["voters"],
            }
            for opt in options
        ],
        "created_at": row["created_at"],
    }


def cmd_list(conn: sqlite3.Connection, recent: int, chat: str | None) -> None:
    query = "SELECT * FROM polls"
    params: list[str | int] = []
    if chat:
        query += " WHERE chat_jid = ?"
        params.append(chat)
    query += " ORDER BY created_at DESC LIMIT ?"
    params.append(recent)

    rows = conn.execute(query, params).fetchall()
    polls = [format_poll(conn, r) for r in rows]
    print(json.dumps({"polls": polls, "count": len(polls)}, indent=2))


def cmd_get(conn: sqlite3.Connection, poll_id: str) -> None:
    row = conn.execute("SELECT * FROM polls WHERE msg_id = ?", (poll_id,)).fetchone()
    if not row:
        print(json.dumps({"error": f"Poll {poll_id!r} not found"}))
        sys.exit(1)
    print(json.dumps(format_poll(conn, row), indent=2))


def main() -> None:
    parser = argparse.ArgumentParser(description="Query WhatsApp poll results")
    parser.add_argument("--id", help="Results for one poll, by message ID")
    parser.add_argument("--recent", type=int, help="How many recent polls to list (default 10)")
    parser.add_argument("--chat", help="Only polls in this chat JID")
    args = parser.parse_args()
    # Both would be ignored with --id; say so rather than answer a question
    # that was not asked.
    if args.id and (args.recent is not None or args.chat):
        parser.error("--id shows one poll; it cannot be combined with --recent or --chat")

    with contextlib.closing(open_db()) as conn:
        if args.id:
            cmd_get(conn, args.id)
        else:
            cmd_list(conn, 10 if args.recent is None else args.recent, args.chat)


if __name__ == "__main__":
    main()
