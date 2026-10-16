"""Tests for poll_results.py, against a scratch copy of the bridge's tables."""

from __future__ import annotations

import json
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import support  # noqa: F401  (puts tools/ on sys.path)
import poll_results

# The two tables poll_results.py reads, as store.go creates them.
SCHEMA = """
CREATE TABLE polls (
    msg_id     TEXT PRIMARY KEY,
    chat_jid   TEXT NOT NULL,
    question   TEXT NOT NULL,
    options    TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
CREATE TABLE poll_votes (
    poll_msg_id      TEXT NOT NULL,
    voter_jid        TEXT NOT NULL,
    selected_options TEXT NOT NULL,
    voted_at         DATETIME NOT NULL,
    PRIMARY KEY (poll_msg_id, voter_jid)
);
"""

CHAT = "120363000000000000@g.us"
ALICE = "15551234567@s.whatsapp.net"
BOB = "15559876543@s.whatsapp.net"


class PollResultsTestCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.db = Path(tmp.name) / "bridge.db"
        conn = sqlite3.connect(self.db)
        conn.executescript(SCHEMA)
        conn.execute("INSERT INTO polls VALUES (?, ?, ?, ?, ?)",
                     ("poll1", CHAT, "When?", json.dumps(["Mon", "Tue"]), "2026-01-05 10:00:00"))
        conn.commit()
        conn.close()
        patcher = mock.patch.dict(os.environ, {"BRIDGE_DB_PATH": str(self.db)})
        patcher.start()
        self.addCleanup(patcher.stop)

        # The tool leaves its connection to the process exit; close it here.
        open_db = poll_results.open_db

        def open_and_track():
            conn = open_db()
            self.addCleanup(conn.close)
            return conn

        patcher = mock.patch.object(poll_results, "open_db", open_and_track)
        patcher.start()
        self.addCleanup(patcher.stop)

    def vote(self, voter: str, selected_json: str):
        conn = sqlite3.connect(self.db)
        conn.execute("INSERT OR REPLACE INTO poll_votes VALUES (?, ?, ?, ?)",
                     ("poll1", voter, selected_json, "2026-01-05 10:05:00"))
        conn.commit()
        conn.close()

    def results(self, argv=("--id", "poll1")) -> dict:
        code, out, err = support.run_main(poll_results, list(argv))
        self.assertEqual(code, 0, err)
        return json.loads(out)


class TallyTest(PollResultsTestCase):
    def test_counts_votes_per_option(self):
        self.vote(ALICE, json.dumps(["Mon"]))
        self.vote(BOB, json.dumps(["Mon", "Tue"]))
        poll = self.results()
        self.assertEqual(poll["total_voters"], 2)
        self.assertEqual(poll["results"], [
            {"option": "Mon", "votes": 2, "voters": [ALICE, BOB]},
            {"option": "Tue", "votes": 1, "voters": [BOB]},
        ])

    def test_a_retracted_vote_is_not_counted(self):
        cases = [
            ("retracted (stored as JSON null)", "null"),
            ("empty selection", "[]"),
            ("not JSON", "garbage"),
            ("not a list", '{"Mon": true}'),
        ]
        for name, stored in cases:
            with self.subTest(name):
                self.vote(ALICE, json.dumps(["Mon"]))
                self.vote(BOB, stored)
                poll = self.results()
                self.assertEqual(poll["total_voters"], 1)
                self.assertEqual(poll["results"][0], {"option": "Mon", "votes": 1, "voters": [ALICE]})
                # The listing, which tallies every poll, must not break either.
                self.assertEqual(self.results(["--recent", "5"])["count"], 1)

    def test_lists_recent_polls(self):
        self.vote(ALICE, json.dumps(["Tue"]))
        listing = self.results(["--chat", CHAT])
        self.assertEqual(listing["count"], 1)
        self.assertEqual(listing["polls"][0]["question"], "When?")

    def test_unknown_poll_is_a_json_error(self):
        code, out, _ = support.run_main(poll_results, ["--id", "nope"])
        self.assertEqual(code, 1)
        self.assertIn("not found", json.loads(out)["error"])


if __name__ == "__main__":
    unittest.main()
