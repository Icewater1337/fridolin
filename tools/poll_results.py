#!/usr/bin/env python3
"""Query WhatsApp poll results from the bridge's SQLite database.

The bridge decrypts incoming poll votes and stores them, so the assistant can
read the tally at any time and summarise it in plain language.

Usage:
  poll_results.py                        # the 10 most recent polls
  poll_results.py --id MSG_ID            # one poll by message ID
  poll_results.py --recent 5             # the N most recent polls
  poll_results.py --chat CHAT_JID        # only polls in one chat

Output is JSON: question, options, total_voters, and per-option votes + voters.
total_voters counts people with a current vote; someone who retracted theirs
is left out.

The database location follows BRIDGE_DB_PATH, then BRIDGE_DATA_DIR/bridge.db,
then ~/.fridolin/bridge.db.
"""

import argparse
import json
import os
import sqlite3
import sys
from pathlib import Path


def db_path() -> Path:
    if p := os.environ.get("BRIDGE_DB_PATH"):
        return Path(p)
    default = next((p for p in (Path.home() / ".fridolin", Path.home() / ".whatsapp-claude-bridge") if p.is_dir()),
                   Path.home() / ".fridolin")
    data_dir = Path(os.environ.get("BRIDGE_DATA_DIR", default))
    return data_dir / "bridge.db"


def open_db() -> sqlite3.Connection:
    path = db_path()
    if not path.exists():
        print(json.dumps({"error": f"Database not found: {path}"}))
        sys.exit(1)
    # Read-only: this tool must never be able to corrupt the live bridge DB.
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def json_list(text) -> list:
    """Decode a JSON array column, treating anything else as empty.

    The bridge stores a retracted vote (every option deselected) as JSON
    null, not as [].
    """
    try:
        value = json.loads(text)
    except (json.JSONDecodeError, TypeError):
        return []
    return value if isinstance(value, list) else []


def tally_votes(conn, poll_msg_id, options):
    """Return per-option counts and voters, plus the number of people who voted.

    Someone who retracted their vote has a row but no selection, and is not
    counted.
    """
    tally = {opt: {"count": 0, "voters": []} for opt in options}
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


def format_poll(conn, row) -> dict:
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


def cmd_list(args) -> None:
    conn = open_db()
    query = "SELECT * FROM polls"
    params = []
    if args.chat:
        query += " WHERE chat_jid = ?"
        params.append(args.chat)
    query += " ORDER BY created_at DESC LIMIT ?"
    params.append(args.recent)

    rows = conn.execute(query, params).fetchall()
    polls = [format_poll(conn, r) for r in rows]
    print(json.dumps({"polls": polls, "count": len(polls)}, indent=2))


def cmd_get(args) -> None:
    conn = open_db()
    row = conn.execute("SELECT * FROM polls WHERE msg_id = ?", (args.id,)).fetchone()
    if not row:
        print(json.dumps({"error": f"Poll {args.id!r} not found"}))
        sys.exit(1)
    print(json.dumps(format_poll(conn, row), indent=2))


def main() -> None:
    parser = argparse.ArgumentParser(description="Query WhatsApp poll results")
    parser.add_argument("--id", help="Results for one poll, by message ID")
    parser.add_argument("--recent", type=int, default=10, help="How many recent polls to list (default 10)")
    parser.add_argument("--chat", help="Only polls in this chat JID")
    args = parser.parse_args()

    cmd_get(args) if args.id else cmd_list(args)


if __name__ == "__main__":
    main()
