"""Tests for transcribe.py. No model or API is ever called."""

from __future__ import annotations

import json
import tempfile
import unittest
from datetime import date
from pathlib import Path
from unittest import mock

import support  # noqa: F401  (puts tools/ on sys.path)
import transcribe

DAY = "2026-01-15"


class FixedDate(date):
    @classmethod
    def today(cls):
        return cls(2026, 1, 15)


class TempDirTestCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)

    def touch(self, *names: str):
        for name in names:
            (self.dir / name).write_text("earlier", encoding="utf-8")

    def names(self) -> list[str]:
        return sorted(p.name for p in self.dir.iterdir())


class FakeEngine:
    """Stands in for transcribe_chunk: no model, no API."""

    def __init__(self, text: str = "Okay.", engine: str = "local:fake"):
        self.text, self.engine, self.calls = text, engine, []

    def __call__(self, path, args):
        self.calls.append((path, args))
        return self.text, self.engine


class TranscribeTestCase(TempDirTestCase):
    """Runs transcribe.main() on scratch media, with the engine faked."""

    def setUp(self):
        super().setUp()
        self.out = self.dir / "transcripts"
        self.engine = FakeEngine()
        for target, value in [("transcribe_chunk", self.engine), ("date", FixedDate),
                              ("probe_duration", lambda path: 5.0)]:
            patcher = mock.patch.object(transcribe, target, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def media(self, name: str, content: bytes) -> Path:
        path = self.dir / name
        path.write_bytes(content)
        return path

    def transcribe(self, *argv: str) -> tuple[int, dict, str]:
        code, out, err = support.run_main(transcribe, [*argv, "--output-dir", str(self.out)])
        return code, json.loads(out), err


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


class ClaimPathsTest(TempDirTestCase):
    def test_picks_a_free_stem(self):
        stem = f"{DAY} - Okay"
        cases = [
            # name, files already there, media suffix, transcript, media
            ("nothing there yet", [], ".ogg", f"{stem}.txt", f"{stem}.ogg"),
            ("an earlier transcript", [f"{stem}.txt", f"{stem}.ogg"], ".ogg",
             f"{stem} (2).txt", f"{stem} (2).ogg"),
            ("two earlier ones", [f"{stem}.txt", f"{stem} (2).txt"], ".ogg",
             f"{stem} (3).txt", f"{stem} (3).ogg"),
            ("only the media name is taken", [f"{stem}.m4a"], ".m4a", f"{stem} (2).txt", f"{stem} (2).m4a"),
            ("no media copy wanted", [f"{stem}.m4a"], None, f"{stem}.txt", None),
        ]
        for name, existing, suffix, want_txt, want_media in cases:
            with self.subTest(name):
                for p in self.dir.iterdir():
                    p.unlink()
                self.touch(*existing)
                txt, media = transcribe.claim_paths(self.dir, stem, suffix)
                self.assertEqual(txt.name, want_txt)
                self.assertEqual(media.name if media else None, want_media)
                for p in existing:
                    self.assertEqual((self.dir / p).read_text(), "earlier", f"{p} must not change")
                # Only the claimed files are new; a half-claimed stem is released.
                new = sorted(set(self.names()) - set(existing))
                self.assertEqual(new, sorted(n for n in (want_txt, want_media) if n))

    def test_the_media_copy_never_takes_the_transcript_name(self):
        self.assertEqual(transcribe.media_suffix_of(Path("notes.TXT")), ".audio")
        self.assertEqual(transcribe.media_suffix_of(Path("recording")), ".audio")
        self.assertEqual(transcribe.media_suffix_of(Path("voice.ogg")), ".ogg")


class OutputFilesTest(TranscribeTestCase):
    def test_same_day_short_notes_keep_their_own_files(self):
        first = self.media("first.ogg", b"first recording")
        second = self.media("second.ogg", b"second recording")

        code, out1, err = self.transcribe(str(first))
        self.assertEqual(code, 0, err)
        self.engine.text = "Okay"  # a different note with the same stem
        code, out2, err = self.transcribe(str(second))
        self.assertEqual(code, 0, err)

        self.assertEqual(Path(out1["transcript_file"]).name, f"{DAY} - Okay.txt")
        self.assertEqual(Path(out2["transcript_file"]).name, f"{DAY} - Okay (2).txt")
        self.assertEqual(Path(out1["transcript_file"]).read_text(encoding="utf-8"), "Okay.")
        self.assertEqual(Path(out2["transcript_file"]).read_text(encoding="utf-8"), "Okay")
        self.assertEqual(Path(out1["audio_file"]).read_bytes(), b"first recording")
        self.assertEqual(Path(out2["audio_file"]).read_bytes(), b"second recording")


if __name__ == "__main__":
    unittest.main()
