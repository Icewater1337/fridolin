"""Tests for calendar_tool.py, against a fake Calendar service."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock
from zoneinfo import ZoneInfo

import support  # noqa: F401  (puts tools/ on sys.path)
import calendar_tool

BERLIN = ZoneInfo("Europe/Berlin")


class CalendarTestCase(unittest.TestCase):
    """Pins the module's configuration so no test depends on the host."""

    allowed: list[str] = []
    default = "primary"

    def setUp(self):
        for name, value in {
            "ALLOWED": list(self.allowed),
            "DEFAULT_CALENDAR": self.default,
            "_timezone": BERLIN,
            "_profile": None,
        }.items():
            patcher = mock.patch.object(calendar_tool, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)


class TokenTestCase(CalendarTestCase):
    """A credentials dir holding one brain's token, and fake Google modules."""

    profile = "work"

    def setUp(self):
        super().setUp()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        # realpath: the macOS sandbox matches resolved paths (/private/var/…).
        self.dir = Path(os.path.realpath(tmp.name))
        self.google = support.FakeGoogle().install(self)
        for name, value in {"DATA_DIR": self.dir, "_profile": self.profile}.items():
            patcher = mock.patch.object(calendar_tool, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.creds_file, self.token_file = calendar_tool.credential_paths()

    def write_token(self, **fields):
        data = {"token": "old", "refresh_token": "r", **fields}
        self.token_file.write_text(json.dumps(data), encoding="utf-8")
        self.token_file.chmod(0o600)


class TokenRefreshTest(TokenTestCase):
    def test_refresh_when_the_directory_cannot_be_created(self):
        # The brain sandbox denies the credentials dir itself, so mkdir there
        # fails with EPERM even though the directory exists.
        self.write_token(expired=True)
        with mock.patch("pathlib.Path.mkdir", side_effect=PermissionError(1, "Operation not permitted")):
            code, out, err = support.run(calendar_tool.get_service)
        self.assertEqual((code, out), (0, ""), err)
        self.assertEqual(self.google.built_with.token, "refreshed")
        self.assertEqual(json.loads(self.token_file.read_text())["token"], "refreshed")

    @unittest.skipIf(hasattr(os, "geteuid") and os.geteuid() == 0, "root ignores file modes")
    def test_a_token_that_cannot_be_saved_only_warns(self):
        self.write_token(expired=True)
        self.token_file.chmod(0o400)
        code, out, err = support.run(calendar_tool.get_service)
        self.assertEqual((code, out), (0, ""), "stdout must stay free for the command's JSON")
        self.assertIn("could not save the refreshed Google token", err)
        self.assertEqual(self.google.refreshed, 1)
        self.assertEqual(json.loads(self.token_file.read_text())["token"], "old", "the old token is kept")

    def test_a_valid_token_is_not_rewritten(self):
        self.write_token()
        before = self.token_file.stat().st_mtime_ns
        code, out, err = support.run(calendar_tool.get_service)
        self.assertEqual((code, out, err), (0, "", ""))
        self.assertEqual(self.google.refreshed, 0)
        self.assertEqual(self.token_file.stat().st_mtime_ns, before)


def sandbox_exec_works() -> bool:
    if sys.platform != "darwin" or not shutil.which("sandbox-exec"):
        return False
    r = subprocess.run(["sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true"],
                       capture_output=True)
    return r.returncode == 0


@unittest.skipUnless(sandbox_exec_works(), "needs macOS sandbox-exec")
class SandboxedRefreshTest(TokenTestCase):
    """The token refresh under the profile sandbox.go generates for a brain."""

    def profile_text(self) -> str:
        # The same shape as sandbox.go: deny the whole credentials dir, then
        # re-allow reading this brain's two files and writing its token.
        d, creds, token = (json.dumps(str(p)) for p in (self.dir, self.creds_file, self.token_file))
        return "\n".join([
            "(version 1)", "(allow default)",
            f"(deny file-read* (subpath {d}))", f"(deny file-write* (subpath {d}))",
            f"(allow file-read* (literal {creds}))", f"(allow file-read* (literal {token}))",
            f"(allow file-write* (literal {token}))",
        ])

    def test_refresh_inside_the_sandbox(self):
        self.write_token(expired=True)
        self.creds_file.write_text("{}", encoding="utf-8")
        script = "\n".join([
            "import sys",
            f"sys.path.insert(0, {str(support.TESTS_DIR)!r})",
            "import support",
            "fake = support.FakeGoogle()",
            "sys.modules.update(fake.modules)",
            "import calendar_tool",
            f"calendar_tool._profile = {self.profile!r}",
            "assert calendar_tool.get_service() is fake.service",
            "print('ok')",
        ])
        env = {**os.environ, "BRIDGE_BRAIN_ID": self.profile, "BRIDGE_CREDENTIALS_DIR": str(self.dir),
               "PYTHONDONTWRITEBYTECODE": "1"}
        r = subprocess.run(["sandbox-exec", "-p", self.profile_text(), sys.executable, "-c", script],
                           capture_output=True, text=True, env=env, timeout=60)
        self.assertEqual((r.returncode, r.stdout), (0, "ok\n"), r.stderr)
        self.assertEqual(json.loads(self.token_file.read_text())["token"], "refreshed")


class TargetCalendarsTest(CalendarTestCase):
    allowed = ["me@example.com", "team@example.com"]
    default = "me@example.com"

    def test_resolution(self):
        cases = [
            ("read commands default to every allowed calendar", None, True,
             ["me@example.com", "team@example.com"]),
            ("write commands default to the default calendar", None, False, ["me@example.com"]),
            ("primary maps to the brain's default", "primary", False, ["me@example.com"]),
            ("an allowed calendar is used as given", "team@example.com", True,
             ["team@example.com"]),
        ]
        for name, requested, many, want in cases:
            with self.subTest(name):
                self.assertEqual(calendar_tool.target_calendars(requested, many=many), want)

    def test_refuses_a_calendar_the_brain_may_not_use(self):
        code, out, _ = support.run(calendar_tool.target_calendars, "other@example.com")
        self.assertEqual(code, 1)
        self.assertIn("not enabled for this brain", json.loads(out)["error"])


class EnsureTimezoneTest(CalendarTestCase):
    def test_cases(self):
        cases = [
            ("naive gets the local offset", "2026-01-06T14:00:00", "2026-01-06T14:00:00+01:00"),
            ("summer time offset", "2026-07-06T14:00:00", "2026-07-06T14:00:00+02:00"),
            ("an explicit offset is kept", "2026-01-06T14:00:00-05:00", "2026-01-06T14:00:00-05:00"),
            ("UTC is kept", "2026-01-06T14:00:00Z", "2026-01-06T14:00:00Z"),
            ("an all-day date is kept", "2026-01-06", "2026-01-06"),
        ]
        for name, given, want in cases:
            with self.subTest(name):
                self.assertEqual(calendar_tool.ensure_timezone(given), want)

    def test_rejects_garbage(self):
        code, out, _ = support.run(calendar_tool.ensure_timezone, "2026-01-06Tnoon")
        self.assertEqual(code, 1)
        self.assertIn("Cannot parse datetime", json.loads(out)["error"])


class FormatEventTest(unittest.TestCase):
    def test_timed_and_all_day(self):
        timed = calendar_tool.format_event({
            "id": "e1", "summary": "Standup",
            "start": {"dateTime": "2026-01-06T09:00:00+01:00"},
            "end": {"dateTime": "2026-01-06T09:15:00+01:00"},
            "attendees": [{"email": "you@example.com"}],
        })
        self.assertEqual(timed["title"], "Standup")
        self.assertEqual(timed["start"], "2026-01-06T09:00:00+01:00")
        self.assertEqual(timed["attendees"], ["you@example.com"])

        all_day = calendar_tool.format_event({"id": "e2", "start": {"date": "2026-01-06"},
                                              "end": {"date": "2026-01-07"}})
        self.assertEqual(all_day["title"], "(no title)")
        self.assertEqual((all_day["start"], all_day["end"]), ("2026-01-06", "2026-01-07"))


if __name__ == "__main__":
    unittest.main()
