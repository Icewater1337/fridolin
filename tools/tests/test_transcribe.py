"""Tests for transcribe.py. No model or API is ever called."""

from __future__ import annotations

import unittest

import support  # noqa: F401  (puts tools/ on sys.path)
import transcribe

DAY = "2026-01-15"


class BuildFilenameTest(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("plain sentence", "Notes from the planning call.", f"{DAY} - Notes from the planning call"),
            ("path characters are replaced", 'a/b\\c:d*e?"f"<g>|h', f"{DAY} - a b c d e f g h"),
            ("punctuation only", "...", f"{DAY} - transcript"),
            ("a leading dot would hide the file", ". hello", f"{DAY} - hello"),
            ("long text is cut with an ellipsis", "word " * 30, f"{DAY} - " + ("word " * 12).rstrip() + "…"),
        ]
        for name, text, want in cases:
            with self.subTest(name):
                self.assertEqual(transcribe.build_filename(text, DAY), want)


if __name__ == "__main__":
    unittest.main()
