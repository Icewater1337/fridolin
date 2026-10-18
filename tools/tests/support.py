"""Shared helpers for the tools tests.

Importing this module puts tools/ on sys.path, so the tests can import the
tools as modules. fake_fridolin stands in for the bridge's binary, which
the wrapper tools run, so no test needs a build of it, a bridge or the
network.
"""

from __future__ import annotations

import contextlib
import io
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
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


def fake_fridolin(testcase: unittest.TestCase, stdout: str = '{"ok": true}', code: int = 0) -> tuple[Path, Path]:
    """Write a stand-in for the fridolin binary that records its arguments.

    It prints stdout and exits with code. Returns the program and the file
    its arguments are written to, as a JSON list.
    """
    tmp = tempfile.TemporaryDirectory()
    testcase.addCleanup(tmp.cleanup)
    program, argv = Path(tmp.name) / "fridolin", Path(tmp.name) / "argv.json"
    program.write_text(
        f"#!{sys.executable}\n"
        "import json, sys\n"
        f"open({str(argv)!r}, 'w').write(json.dumps(sys.argv[1:]))\n"
        f"print({stdout!r})\n"
        f"sys.exit({code})\n",
        encoding="utf-8")
    program.chmod(0o700)
    return program, argv


def run_tool(name: str, argv: list[str], env: dict[str, str]) -> tuple[int, str, str]:
    """Run a scratch copy, without a real checkout or installed binary nearby.

    Only explicit environment entries reach the child. In particular, the
    wrappers must never discover and execute a user's fridolin build while
    testing how they behave when no binary is available.
    """
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        tools = root / "tools"
        tools.mkdir()
        for source in TOOLS_DIR.glob("*.py"):
            shutil.copyfile(source, tools / source.name)
        home = root / "home"
        home.mkdir()
        r = subprocess.run([sys.executable, str(tools / name), *argv], capture_output=True, text=True,
                           cwd=root, env={"PATH": "", "HOME": str(home), **env}, timeout=60)
    return r.returncode, r.stdout, r.stderr
