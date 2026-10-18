"""Tests for toolkit.py, what the tools share."""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import support  # first: it puts tools/ on sys.path
import toolkit


class OutputTest(unittest.TestCase):
    def test_fail_is_a_json_error_with_exit_1(self):
        code, out, err = support.run(toolkit.fail, "Nope: ünïcode")
        self.assertEqual((code, json.loads(out), err), (1, {"error": "Nope: ünïcode"}, ""))
        self.assertIn("ünïcode", out)  # not \\u escapes, which are harder to read

    def test_emit_json(self):
        code, out, _ = support.run(toolkit.emit_json, {"a": [1, "é"]})
        self.assertEqual((code, json.loads(out)), (0, {"a": [1, "é"]}))


class FindFridolinTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)

    def program(self, name: str) -> Path:
        p = self.dir / name / "fridolin"
        p.parent.mkdir(parents=True)
        p.write_text("#!/bin/sh\n", encoding="utf-8")
        p.chmod(0o700)
        return p

    def find(self, env: dict[str, str], tools_dir: Path | None = None) -> tuple[int, str, str]:
        with mock.patch.dict(os.environ, env, clear=True), \
             mock.patch.object(toolkit, "TOOLS_DIR", tools_dir or self.dir / "nowhere" / "tools"):
            return support.run(lambda: print(toolkit.fridolin_binary()))

    def test_order(self):
        on_path, installed = self.program("path"), self.program("install")
        built = self.program("checkout/bin")
        install_tools, checkout_tools = installed.parent / "tools", built.parent.parent / "tools"
        cases = [
            ("FRIDOLIN_BIN first", {"FRIDOLIN_BIN": "/opt/fridolin/fridolin", "PATH": str(on_path.parent)},
             install_tools, "/opt/fridolin/fridolin"),
            ("then PATH", {"PATH": str(on_path.parent)}, install_tools, str(on_path)),
            ("then the one next to tools/", {"PATH": ""}, install_tools, str(installed)),
            # make build writes bin/fridolin.
            ("then the checkout's build", {"PATH": ""}, checkout_tools, str(built)),
        ]
        for name, env, tools_dir, want in cases:
            with self.subTest(name):
                code, out, _ = self.find(env, tools_dir=tools_dir)
                self.assertEqual((code, out.strip()), (0, want))

    def test_none_is_a_json_error(self):
        code, out, _ = self.find({"PATH": ""})
        self.assertEqual(code, 1)
        self.assertIn("FRIDOLIN_BIN", json.loads(out)["error"])


if __name__ == "__main__":
    unittest.main()
