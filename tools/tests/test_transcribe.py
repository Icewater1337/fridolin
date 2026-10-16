"""Tests for transcribe.py. No model or API is ever called."""

from __future__ import annotations

import json
import sys
import tempfile
import types
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


class KeepOriginalTest(TranscribeTestCase):
    def test_flag(self):
        cases = [
            ("default", [], True),
            ("--keep-original", ["--keep-original"], True),
            ("--no-keep-original", ["--no-keep-original"], False),
            ("the last one wins", ["--no-keep-original", "--keep-original"], True),
        ]
        for name, flags, kept in cases:
            with self.subTest(name):
                code, out, err = self.transcribe(str(self.media("note.ogg", b"audio")), *flags)
                self.assertEqual(code, 0, err)
                self.assertEqual(out["audio_file"] is not None, kept)
                if kept:
                    self.assertEqual(Path(out["audio_file"]).read_bytes(), b"audio")

    def test_one_documented_switch(self):
        # --keep-original used to be a separate store_true flag that defaulted
        # to on, so passing it did nothing.
        code, out, _ = support.run_main(transcribe, ["--help"])
        self.assertEqual(code, 0)
        self.assertIn("--keep-original, --no-keep-original", out)


START = "<|startofprev|>"


class FakeProcessor:
    """One token per word of the prompt, after Whisper's start-of-prompt token."""

    def get_prompt_ids(self, text, return_tensors="np"):
        self.return_tensors = return_tensors
        return [START, *text.split()]


class FakePipeline:
    def __init__(self):
        self.calls = []

    def __call__(self, audio, generate_kwargs=None):
        self.calls.append((audio, generate_kwargs))
        return {"text": " Hello there. "}


class LocalEngineTest(TempDirTestCase):
    """The local engine, with a fake pipeline in place of the Whisper model."""

    def setUp(self):
        super().setUp()
        self.pipe = FakePipeline()
        self.processor = FakeProcessor()
        for target, value in [("_PIPE", self.pipe), ("_PROCESSOR", self.processor),
                              ("local_available", lambda: True), ("groq_available", lambda: False),
                              ("probe_duration", lambda path: 5.0), ("date", FixedDate)]:
            patcher = mock.patch.object(transcribe, target, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        # The fake prompt IDs are a list, so torch.cat joins lists here.
        fake_torch = types.SimpleNamespace(cat=lambda parts: [t for part in parts for t in part])
        patcher = mock.patch.dict(sys.modules, {"torch": fake_torch})
        patcher.start()
        self.addCleanup(patcher.stop)
        self.audio = self.dir / "note.ogg"
        self.audio.write_bytes(b"audio")

    def run_tool(self, *flags: str) -> tuple[int, str, str]:
        self.pipe.calls.clear()
        return support.run_main(transcribe, [str(self.audio), "--output-dir", str(self.dir / "out"), *flags])

    def test_prompt_and_language_reach_the_model(self):
        cases = [
            ("prompt and language", ["--prompt", "Names: Quoxel", "--language", "de"],
             {"language": "de", "prompt_ids": [START, "Names:", "Quoxel"]}),
            ("prompt only", ["--prompt", "Names: Quoxel"],
             {"prompt_ids": [START, "Names:", "Quoxel"]}),
            ("neither", [], {}),
        ]
        for name, flags, want in cases:
            with self.subTest(name):
                code, out, err = self.run_tool(*flags)
                self.assertEqual(code, 0, err)
                self.assertEqual(json.loads(out)["transcript"], "Hello there.")
                (_, generate_kwargs), = self.pipe.calls
                self.assertEqual(generate_kwargs, want)
        self.assertEqual(self.processor.return_tensors, "pt", "generate() takes a torch tensor")

    def test_a_long_prompt_keeps_its_end(self):
        # Whisper's decoder has room for 223 prompt tokens; more fails every
        # window, or leaves no room for the transcript.
        words = [f"w{i}" for i in range(300)]
        cases = [
            # name, prompt words, the words the model gets
            ("within the budget", words[:200], words[:200]),
            ("exactly the budget", words[:223], words[:223]),
            ("over the budget", words, words[-223:]),
        ]
        for name, prompt, want in cases:
            with self.subTest(name):
                code, _, err = self.run_tool("--prompt", " ".join(prompt))
                self.assertEqual(code, 0, err)
                (_, generate_kwargs), = self.pipe.calls
                self.assertEqual(generate_kwargs["prompt_ids"], [START, *want])
                self.assertEqual("reads only the last 223" in err, len(prompt) > 223, err)


if __name__ == "__main__":
    unittest.main()
