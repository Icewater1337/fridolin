"""Tests for calendar_tool.py, against a fake Calendar service."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
import warnings
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
        # As if run by hand, unless a test sets the bridge's variables.
        env = mock.patch.dict(os.environ, {"CALENDAR_TZ": "Europe/Berlin"})
        env.start()
        self.addCleanup(env.stop)
        for name in ("BRIDGE_BRAIN_ID", "BRIDGE_CREDENTIALS_DIR"):
            os.environ.pop(name, None)
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


class NoInteractiveSignInTest(TokenTestCase):
    """Commands use the saved token or fail with JSON; they never open a browser."""

    def in_brain(self):
        patcher = mock.patch.dict(os.environ, {"BRIDGE_BRAIN_ID": self.profile,
                                               "BRIDGE_CREDENTIALS_DIR": str(self.dir)})
        patcher.start()
        self.addCleanup(patcher.stop)

    def assert_json_error(self, *want: str) -> str:
        code, out, _ = support.run(calendar_tool.get_service)
        self.assertEqual(code, 1)
        message = json.loads(out)["error"]
        for w in want:
            self.assertIn(w, message)
        self.assertEqual(self.google.flows, [], "no browser sign-in may start")
        return message

    def refused(self, error: Exception):
        self.write_token(expired=True)
        self.google.refresh_error = error

    def test_failures_in_a_brain_point_to_the_dashboard(self):
        old_google_auth = self.google.RefreshError("invalid_grant")
        del old_google_auth.retryable  # releases before the attribute existed
        cases = {
            # Disconnect deletes the token but leaves the copied OAuth client.
            "no token": lambda: None,
            "revoked or expired sign-in": lambda: self.refused(self.google.RefreshError("invalid_grant")),
            "a refusal from an older google-auth": lambda: self.refused(old_google_auth),
            "no refresh token": lambda: self.write_token(expired=True, refresh_token=None),
            "corrupt token file": lambda: self.token_file.write_text("{not json", encoding="utf-8"),
        }
        self.in_brain()
        for name, arrange in cases.items():
            with self.subTest(name):
                self.token_file.unlink(missing_ok=True)
                self.google.refresh_error = None
                self.creds_file.write_text("{}", encoding="utf-8")
                arrange()
                self.assert_json_error("reconnect the calendar in the dashboard")

    def test_a_refusal_gives_google_s_reason(self):
        # google-auth raises RefreshError(message, response); only the
        # message is for people.
        self.in_brain()
        self.refused(self.google.RefreshError("invalid_grant: Token has been expired or revoked.",
                                              {"error": "invalid_grant"}))
        message = self.assert_json_error("(invalid_grant: Token has been expired or revoked.)")
        self.assertNotIn("{", message)

    def test_temporary_trouble_is_reported_as_temporary(self):
        g = self.google
        cases = [
            ("a dropped connection", g.TransportError("connection reset"), "Could not reach Google"),
            # google-auth retries a 5xx or 429 from the token endpoint a few
            # times, then gives up with a retryable RefreshError.
            ("a token endpoint outage", g.RefreshError("Service Unavailable", retryable=True),
             "Google could not refresh the calendar sign-in just now (Service Unavailable)"),
        ]
        self.in_brain()
        for name, error, want in cases:
            with self.subTest(name):
                self.refused(error)
                message = self.assert_json_error(want, "try again")
                self.assertNotIn("reconnect", message)

    def test_by_hand_it_points_to_auth(self):
        self.creds_file.write_text("{}", encoding="utf-8")
        self.assert_json_error("calendar_tool.py --profile work auth")


class ReconnectHintTest(CalendarTestCase):
    def test_names_buttons_the_dashboard_shows(self):
        # The brain's Calendar card offers "Connect Google Calendar" until it
        # is connected, and "Sign in again" once Google refuses the sign-in.
        js = (support.TOOLS_DIR.parent / "web" / "assets" / "calendar.js").read_text(encoding="utf-8")
        with mock.patch.dict(os.environ, {"BRIDGE_BRAIN_ID": "work"}):
            hint = calendar_tool.reconnect_hint()
        for label in ("Sign in again", "Connect Google Calendar"):
            with self.subTest(label):
                self.assertIn(label, hint)
                self.assertIn(f"'{label}'", js, "the dashboard has no such button")


class AuthCommandTest(TokenTestCase):
    def auth(self) -> tuple[int, dict, str]:
        code, out, err = support.run_main(calendar_tool, ["--profile", self.profile, "auth"])
        return code, json.loads(out), err

    def test_signs_in_and_saves_a_private_token(self):
        self.creds_file.write_text("{}", encoding="utf-8")
        code, out, err = self.auth()
        self.assertEqual(code, 0, err)
        self.assertEqual(out, {"authorized": True, "token_file": str(self.token_file)})
        self.assertEqual(len(self.google.flows), 1)
        self.assertIn("Please visit this URL", err, "the sign-in prompt goes to stderr")
        self.assertEqual(json.loads(self.token_file.read_text())["token"], "consented")
        self.assertEqual(self.token_file.stat().st_mode & 0o777, 0o600)

    def test_refuses_inside_a_brain(self):
        self.creds_file.write_text("{}", encoding="utf-8")
        with mock.patch.dict(os.environ, {"BRIDGE_BRAIN_ID": self.profile}):
            code, out, _ = self.auth()
        self.assertEqual(code, 1)
        self.assertIn("dashboard", out["error"])
        self.assertEqual(self.google.flows, [])

    def test_needs_the_oauth_client(self):
        code, out, _ = self.auth()
        self.assertEqual(code, 1)
        self.assertIn("Credentials file not found", out["error"])


class ApiErrorTest(TokenTestCase):
    """Errors from Google come back as JSON on stdout, not as a traceback."""

    def test_errors_are_json(self):
        g = self.google
        cases = [
            ("delete of an unknown event", "delete", g.http_error(404, "Not Found"),
             ["delete", "--event-id", "nope"],
             "Google Calendar 404: Not Found. Check the event and calendar IDs."),
            ("a calendar the account cannot read", "list", g.http_error(403, "Forbidden"),
             ["list", "--from", "2026-01-05"], "Google Calendar 403: Forbidden"),
            ("an empty range", "insert", g.http_error(400, "The specified time range is empty."),
             ["create", "--title", "x", "--start", "2026-01-06", "--end", "2026-01-06"],
             "Google Calendar 400: The specified time range is empty."),
            ("a sign-in revoked mid-run", "calendarList", g.RefreshError("invalid_grant"),
             ["calendars"], "calendar_tool.py --profile work auth"),
            ("a token endpoint outage mid-run", "calendarList",
             g.RefreshError("Service Unavailable", retryable=True), ["calendars"], "try again"),
            ("a dropped connection", "freebusy", g.TransportError("reset"),
             ["free", "--date", "2026-01-06"], "try again"),
        ]
        self.write_token()
        for name, method, error, argv, want in cases:
            with self.subTest(name):
                g.service.errors = {method: error}
                code, out, err = support.run_main(calendar_tool, ["--profile", self.profile, *argv])
                self.assertEqual(code, 1)
                self.assertIn(want, json.loads(out)["error"])
                self.assertNotIn("Traceback", err)

    def test_an_unexpected_error_is_json_with_the_traceback_on_stderr(self):
        self.write_token()
        self.google.service.errors = {"delete": RuntimeError("boom")}
        code, out, err = support.run_main(calendar_tool, ["--profile", self.profile, "delete",
                                                          "--event-id", "e1"])
        self.assertEqual(code, 1)
        self.assertEqual(json.loads(out), {"error": "RuntimeError: boom"})
        self.assertIn("Traceback", err)


class RealHttpErrorTest(CalendarTestCase):
    def test_describes_googleapiclient_errors(self):
        # The fake mirrors googleapiclient's HttpError; check against the real
        # one where it is installed.
        with warnings.catch_warnings():
            warnings.simplefilter("ignore")  # google-auth warns about old Pythons
            try:
                import httplib2
                from googleapiclient.errors import HttpError
            except ImportError:
                self.skipTest("googleapiclient is not installed")
            e = HttpError(httplib2.Response({"status": 404}), b'{"error": {"code": 404, "message": "Not Found"}}')
            self.assertEqual(calendar_tool.describe_error(e),
                             "Google Calendar 404: Not Found. Check the event and calendar IDs.")


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
