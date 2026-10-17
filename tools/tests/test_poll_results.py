"""Tests for poll_results.py, against a scratch copy of the bridge's tables."""

from __future__ import annotations

import json
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import support  # first: it puts tools/ on sys.path
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

        # Every connection the tool opens, to check it closes them.
        self.conns: list[sqlite3.Connection] = []
        open_db = poll_results.open_db

        def open_and_track():
            conn = open_db()
            self.conns.append(conn)
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


class ArgumentsTest(PollResultsTestCase):
    def test_id_does_not_combine_with_list_options(self):
        cases = [
            ("--recent", ["--id", "poll1", "--recent", "5"]),
            ("--chat", ["--id", "poll1", "--chat", CHAT]),
            ("both", ["--id", "poll1", "--recent", "5", "--chat", CHAT]),
        ]
        for name, argv in cases:
            with self.subTest(name):
                code, out, err = support.run_main(poll_results, argv)
                self.assertEqual(code, 2)
                self.assertEqual(out, "")
                self.assertIn("cannot be combined with --recent or --chat", err)
        self.assertEqual(self.conns, [], "a usage error opens no database")

    def test_recent_limits_the_listing(self):
        conn = sqlite3.connect(self.db)
        conn.execute("INSERT INTO polls VALUES (?, ?, ?, ?, ?)",
                     ("poll2", CHAT, "Where?", json.dumps(["Here"]), "2026-01-06 10:00:00"))
        conn.commit()
        conn.close()
        cases = [("default", [], 2), ("--recent 1", ["--recent", "1"], 1)]
        for name, argv, want in cases:
            with self.subTest(name):
                self.assertEqual(self.results(argv)["count"], want)

    def test_the_database_is_closed_after_every_command(self):
        cases = [
            ("a poll", ["--id", "poll1"], 0),
            ("an unknown poll", ["--id", "nope"], 1),
            ("the listing", ["--recent", "5"], 0),
        ]
        for name, argv, want_code in cases:
            with self.subTest(name):
                self.conns.clear()
                code, _, err = support.run_main(poll_results, argv)
                self.assertEqual(code, want_code, err)
                self.assertEqual(len(self.conns), 1)
                with self.assertRaises(sqlite3.ProgrammingError):
                    self.conns[0].execute("SELECT 1")


class DBPathTest(unittest.TestCase):
    """Where the tool looks for the bridge's DB."""

    def test_db_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp)
            # Only the data dir from before the rename exists. Whether an
            # install still uses it is the bridge's call, which it passes on
            # in BRIDGE_DB_PATH: the tool must not pick it by itself.
            (home / ".whatsapp-claude-bridge").mkdir()
            cases = [
                ("nothing set", {}, home / ".fridolin" / "bridge.db"),
                ("empty data dir", {"BRIDGE_DATA_DIR": ""}, home / ".fridolin" / "bridge.db"),
                ("data dir", {"BRIDGE_DATA_DIR": "/srv/data"}, Path("/srv/data/bridge.db")),
                ("db path", {"BRIDGE_DATA_DIR": "/srv/data", "BRIDGE_DB_PATH": "/srv/db/b.db"}, Path("/srv/db/b.db")),
            ]
            for name, env, want in cases:
                with self.subTest(name), mock.patch.dict(os.environ, {"HOME": str(home)}):
                    os.environ.pop("BRIDGE_DATA_DIR", None)
                    os.environ.pop("BRIDGE_DB_PATH", None)
                    os.environ.update(env)
                    self.assertEqual(poll_results.db_path(), want)


if __name__ == "__main__":
    unittest.main()
