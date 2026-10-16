"""Shared helpers for the tools tests.

Importing this module puts tools/ on sys.path, so the tests can import the
tools as modules. FakeGoogle stands in for the few google-auth and
googleapiclient names calendar_tool.py uses, so no test needs the real
packages or the network.
"""

from __future__ import annotations

import contextlib
import io
import json
import sys
import types
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

TESTS_DIR = Path(__file__).resolve().parent
TOOLS_DIR = TESTS_DIR.parent
if str(TOOLS_DIR) not in sys.path:
    sys.path.insert(0, str(TOOLS_DIR))


def run(fn, *args, **kwargs) -> tuple[int, str, str]:
    """Call fn, returning (exit code, stdout, stderr).

    The exit code is SystemExit's, else fn's return value if it is an int
    (history_search.main returns one), else 0.
    """
    out, errs = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(errs):
        try:
            ret = fn(*args, **kwargs)
            code = ret if isinstance(ret, int) else 0
        except SystemExit as e:
            code = e.code if isinstance(e.code, int) else 1
    return code, out.getvalue(), errs.getvalue()


def run_main(module, argv: list[str]) -> tuple[int, str, str]:
    """Run module.main() as if invoked with argv."""
    with mock.patch.object(sys, "argv", [module.__name__ + ".py", *argv]):
        return run(module.main)


class FakeCalendar:
    """An in-memory stand-in for the Calendar v3 service.

    events maps a calendar ID to its events, already in start order; busy
    maps a calendar ID to its freebusy slots. errors maps a method name
    ("list", "insert", "delete", "freebusy", "calendarList") to an exception
    its request raises on execute().
    """

    def __init__(self, events=None, busy=None, calendars=None):
        self.events_by_cal = events or {}
        self.busy = busy or {}
        self.calendar_items = calendars or []
        self.errors: dict[str, Exception] = {}
        self.calls: list[tuple[str, dict]] = []

    def _request(self, name, kwargs, result):
        self.calls.append((name, kwargs))

        def execute():
            if name in self.errors:
                raise self.errors[name]
            return result()

        return SimpleNamespace(execute=execute, name=name, kwargs=kwargs)

    def events(self):
        fake = self

        class Events:
            def list(self, **kw):
                def page():
                    items = fake.events_by_cal.get(kw["calendarId"], [])
                    offset = int(kw.get("pageToken") or 0)
                    size = kw.get("maxResults", 250)
                    resp = {"items": items[offset:offset + size]}
                    if offset + size < len(items):
                        resp["nextPageToken"] = str(offset + size)
                    return resp

                return fake._request("list", kw, page)

            def list_next(self, previous_request, previous_response):
                token = previous_response.get("nextPageToken")
                if not token:
                    return None
                return self.list(**{**previous_request.kwargs, "pageToken": token})

            def insert(self, **kw):
                return fake._request("insert", kw, lambda: {"id": "new1", **kw["body"]})

            def delete(self, **kw):
                return fake._request("delete", kw, lambda: "")

        return Events()

    def freebusy(self):
        fake = self

        class FreeBusy:
            def query(self, body):
                return fake._request("freebusy", body, lambda: {
                    "calendars": {c["id"]: {"busy": fake.busy.get(c["id"], [])} for c in body["items"]},
                })

        return FreeBusy()

    def calendarList(self):
        fake = self

        class CalendarList:
            def list(self, **kw):
                return fake._request("calendarList", kw, lambda: {"items": fake.calendar_items})

        return CalendarList()


class FakeGoogle:
    """Fake google-auth / googleapiclient modules for calendar_tool.py.

    A token file is read as JSON; "expired": true marks it as needing a
    refresh. refresh_error, if set, is raised by Credentials.refresh. Every
    InstalledAppFlow that is started is recorded in flows.
    """

    def __init__(self, service: FakeCalendar | None = None):
        self.service = service or FakeCalendar()
        self.refresh_error: Exception | None = None
        self.flows: list = []
        self.refreshed = 0
        self.modules = self._modules()

    def install(self, testcase) -> "FakeGoogle":
        patcher = mock.patch.dict(sys.modules, self.modules)
        patcher.start()
        testcase.addCleanup(patcher.stop)
        return self

    def http_error(self, status: int, message: str) -> Exception:
        """An HttpError shaped like the one googleapiclient raises."""
        content = json.dumps({"error": {"code": status, "message": message}}).encode()
        return self.HttpError(SimpleNamespace(status=status, reason="HTTP reason"), content)

    def _modules(self) -> dict[str, types.ModuleType]:
        fake = self

        class GoogleAuthError(Exception):
            pass

        class RefreshError(GoogleAuthError):
            pass

        class TransportError(GoogleAuthError):
            pass

        class Credentials:
            def __init__(self, data):
                self.data = data
                self.token = data.get("token")
                self.refresh_token = data.get("refresh_token")
                self.expired = bool(data.get("expired"))

            @property
            def valid(self):
                return bool(self.token) and not self.expired

            @classmethod
            def from_authorized_user_file(cls, filename, scopes=None):
                with open(filename, encoding="utf-8") as f:
                    return cls(json.load(f))

            def refresh(self, request):
                if fake.refresh_error is not None:
                    raise fake.refresh_error
                fake.refreshed += 1
                self.token, self.expired = "refreshed", False

            def to_json(self):
                return json.dumps({**self.data, "token": self.token, "expired": self.expired})

        class Request:
            pass

        class InstalledAppFlow:
            @classmethod
            def from_client_secrets_file(cls, client_secrets_file, scopes=None):
                flow = cls()
                flow.client_secrets_file = client_secrets_file
                fake.flows.append(flow)
                return flow

            def run_local_server(self, **kwargs):
                return Credentials({"token": "consented", "refresh_token": "r"})

        class HttpError(Exception):
            def __init__(self, resp, content, uri=None):
                super().__init__(resp, content)
                self.resp, self.content, self.uri = resp, content, uri
                try:
                    self.reason = json.loads(content.decode())["error"]["message"]
                except (ValueError, KeyError):
                    self.reason = resp.reason

            @property
            def status_code(self):
                return self.resp.status

        def build(name, version, credentials=None, **kwargs):
            fake.built_with = credentials
            return fake.service

        self.HttpError = HttpError
        self.RefreshError = RefreshError
        self.TransportError = TransportError

        def module(name, **attrs):
            m = types.ModuleType(name)
            m.__path__ = []  # importable as a package, so submodules resolve
            m.__dict__.update(attrs)
            return m

        return {
            "google": module("google"),
            "google.oauth2": module("google.oauth2"),
            "google.oauth2.credentials": module("google.oauth2.credentials", Credentials=Credentials),
            "google.auth": module("google.auth"),
            "google.auth.exceptions": module(
                "google.auth.exceptions", GoogleAuthError=GoogleAuthError,
                RefreshError=RefreshError, TransportError=TransportError),
            "google.auth.transport": module("google.auth.transport"),
            "google.auth.transport.requests": module("google.auth.transport.requests", Request=Request),
            "google_auth_oauthlib": module("google_auth_oauthlib"),
            "google_auth_oauthlib.flow": module("google_auth_oauthlib.flow", InstalledAppFlow=InstalledAppFlow),
            "googleapiclient": module("googleapiclient"),
            "googleapiclient.discovery": module("googleapiclient.discovery", build=build),
            "googleapiclient.errors": module("googleapiclient.errors", HttpError=HttpError),
        }
