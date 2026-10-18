#!/usr/bin/env python3
"""Deprecated: a brain's calendar is `"$FRIDOLIN_BIN" calendar` now.

`fridolin calendar` asks the bridge, which holds the brain's Google token
and keeps the brain to the calendars ticked on its page (docs/CALENDAR.md).
This wrapper hands its arguments on, for one release, so that prompts
written for it keep working: the commands, their flags and their JSON are
the same. --profile is ignored, since the bridge knows which brain runs, and
signing in (auth) is done in the dashboard.

    calendar_tool.py list --from 2026-01-05      # fridolin calendar list --from 2026-01-05
"""

from __future__ import annotations

import sys

import toolkit


def translate(argv: list[str]) -> list[str]:
    """The fridolin arguments for calendar_tool.py's argv."""
    out: list[str] = []
    skip = False
    for i, arg in enumerate(argv):
        if skip:
            skip = False
        elif arg == "--profile":
            skip = i + 1 < len(argv)
        elif not arg.startswith("--profile="):
            out.append(arg)
    return ["calendar", *out]


def main() -> None:
    toolkit.run_fridolin("calendar_tool.py", translate(sys.argv[1:]))


if __name__ == "__main__":
    main()
