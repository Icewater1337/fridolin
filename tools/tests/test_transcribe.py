"""Tests for transcribe.py. No model or API is ever called."""

from __future__ import annotations

import json
import os
import shlex
import shutil
import sys
import tempfile
import types
import unittest
from datetime import date
from pathlib import Path
from unittest import mock

import support  # first: it puts tools/ on sys.path
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

    def __call__(self, path, args, available=None):
        self.calls.append((path, args))
        return self.text, self.engine


class TranscribeTestCase(TempDirTestCase):
    """Runs transcribe.main() on scratch media, with the engine faked."""

    def setUp(self):
        super().setUp()
        self.out = self.dir / "transcripts"
        self.engine = FakeEngine()
        for target, value in [("transcribe_chunk", self.engine), ("date", FixedDate),
                              ("probe_duration", lambda path: 5.0), ("local_available", lambda: True)]:
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


class Counter:
    """A stand-in function that counts its calls and returns or raises."""

    def __init__(self, result=None, raises=None):
        self.result, self.raises, self.calls = result, raises, 0

    def __call__(self, *args, **kwargs):
        self.calls += 1
        if self.raises is not None:
            raise self.raises
        return self.result(*args) if callable(self.result) else self.result


class EngineChoiceTest(TempDirTestCase):
    """Engine selection and fallback over a three-chunk recording."""

    def setUp(self):
        super().setUp()
        self.local_available = Counter(True)
        self.groq_available = Counter(True)
        self.load = Counter("pipeline")
        self.local = Counter(lambda path, *rest: f"local {path.stem}")
        self.groq = Counter(lambda path, *rest: f"groq {path.stem}")
        self.extract = Counter(lambda path: path)
        self.chunks = [self.dir / "chunks" / f"chunk-{i:03d}.wav" for i in range(3)]
        self.write_chunks()
        for target, value in [
            ("local_available", self.local_available), ("groq_available", self.groq_available),
            ("load_local_pipeline", self.load), ("transcribe_local", self.local),
            ("transcribe_groq", self.groq), ("extract_audio", self.extract),
            ("probe_duration", lambda path: 1800.0), ("split_audio", lambda path, seconds: list(self.chunks)),
            ("date", FixedDate),
        ]:
            patcher = mock.patch.object(transcribe, target, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        # As if ffmpeg were installed, so the recording is split.
        patcher = mock.patch.object(transcribe.shutil, "which", lambda name: f"/usr/bin/{name}")
        patcher.start()
        self.addCleanup(patcher.stop)
        self.source = self.dir / "meeting.ogg"
        self.source.write_bytes(b"audio")

    def write_chunks(self):
        # main() removes the chunk directory when it is done, as with real chunks.
        self.chunks[0].parent.mkdir(exist_ok=True)
        for c in self.chunks:
            c.write_bytes(b"pcm")

    def run_tool(self, *flags: str) -> tuple[int, dict, str]:
        code, out, err = support.run_main(transcribe, [str(self.source), "--output-dir",
                                                       str(self.dir / "out"), *flags])
        return code, json.loads(out), err

    def test_engines_are_checked_once(self):
        code, out, err = self.run_tool()
        self.assertEqual(code, 0, err)
        self.assertEqual(out["transcript"], "local chunk-000 local chunk-001 local chunk-002")
        self.assertEqual((self.local_available.calls, self.groq_available.calls), (1, 1))

    def test_a_model_that_fails_to_load_is_not_loaded_again(self):
        self.load.raises = RuntimeError("out of memory")
        code, out, err = self.run_tool()
        self.assertEqual(code, 0, err)
        self.assertEqual(self.load.calls, 1)
        self.assertEqual((self.local.calls, self.groq.calls), (0, 3))
        self.assertEqual(out["engine"], "groq:whisper-large-v3")
        self.assertEqual(err.count("could not be loaded"), 1)

    def test_one_failed_chunk_falls_back_for_that_chunk_only(self):
        def flaky(path, *rest):
            if path.stem == "chunk-001":
                raise RuntimeError("NaN logits")
            return f"local {path.stem}"

        self.local.result = flaky
        code, out, err = self.run_tool()
        self.assertEqual(code, 0, err)
        self.assertEqual(out["transcript"], "local chunk-000 groq chunk-001 local chunk-002")
        self.assertEqual(out["engine"], "groq:whisper-large-v3+local:openai/whisper-large-v3-turbo")

    def test_engine_errors_are_json(self):
        cases = [
            ("a Groq error", ["--engine", "groq"], self.groq, RuntimeError("401 bad key"),
             "Groq transcription failed: 401 bad key"),
            ("a model that cannot load, without a fallback", ["--engine", "local"], self.load,
             RuntimeError("no such model"),
             "The local model could not be loaded and no fallback is configured: no such model"),
        ]
        for name, flags, failing, error, want in cases:
            with self.subTest(name):
                self.write_chunks()
                failing.raises = error
                code, out, err = self.run_tool(*flags)
                failing.raises = None
                self.assertEqual(code, 1)
                self.assertEqual(out["error"], want)
                self.assertNotIn("Traceback", err)
                self.assertFalse(any(c.exists() for c in self.chunks), "chunks are cleaned up")

    def test_no_engine_at_all(self):
        self.local_available.result = self.groq_available.result = False
        code, out, _ = self.run_tool()
        self.assertEqual(code, 1)
        self.assertIn("No transcription engine available", out["error"])

    def test_install_hints_name_the_requirements_file_itself(self):
        # Brains run the tool from their own folder, where tools/ is no path.
        want = support.TOOLS_DIR / "requirements-transcription.txt"
        self.assertTrue(want.is_file())
        self.local_available.result = self.groq_available.result = False
        for name, flags in [("--engine local", ["--engine", "local"]), ("no engine at all", [])]:
            with self.subTest(name):
                code, out, _ = self.run_tool(*flags)
                self.assertEqual(code, 1)
                self.assertIn(f"pip install -r {shlex.quote(str(want))}", out["error"])

    def test_groq_hints_name_child_env_passthrough(self):
        # Brains get GROQ_API_KEY only when the bridge's config names it.
        self.local_available.result = self.groq_available.result = False
        for name, flags in [("--engine groq", ["--engine", "groq"]), ("no engine at all", [])]:
            with self.subTest(name):
                code, out, _ = self.run_tool(*flags)
                self.assertEqual(code, 1)
                self.assertIn("child_env_passthrough", out["error"])
                self.assertIn("docs/CONFIGURATION.md#child_env_passthrough", out["error"])

    def test_a_missing_engine_fails_before_extracting_video_audio(self):
        self.source = self.dir / "clip.mp4"
        self.source.write_bytes(b"video")
        self.local_available.result = False
        code, out, _ = self.run_tool("--engine", "local")
        self.assertEqual(code, 1)
        self.assertIn("--engine local, but transformers/torch are not installed", out["error"])
        self.assertEqual(self.extract.calls, 0)


class FakeGroq:
    """Stands in for the groq package: records each client and each upload.

    fail_with, when set, is what every upload raises.
    """

    def __init__(self):
        self.clients: list[dict] = []
        self.uploads: list[dict] = []
        self.fail_with: Exception | None = None
        fake = self

        class Groq:
            def __init__(self, **kwargs):
                fake.clients.append(kwargs)
                self.audio = types.SimpleNamespace(transcriptions=types.SimpleNamespace(create=self.create))

            def create(self, **kwargs):
                fake.uploads.append(kwargs)
                if fake.fail_with is not None:
                    raise fake.fail_with
                return types.SimpleNamespace(text=f" text of {kwargs['file'][0]} ")

        self.module = types.ModuleType("groq")
        self.module.Groq = Groq


class GroqEngineTest(TempDirTestCase):
    """The Groq engine over a three-chunk recording, with the SDK faked."""

    def setUp(self):
        super().setUp()
        self.groq = FakeGroq()
        self.converted: list[Path] = []
        self.chunks = [self.dir / "chunks" / f"chunk-{i:03d}.wav" for i in range(3)]
        self.chunks[0].parent.mkdir()
        for c in self.chunks:
            c.write_bytes(b"pcm")
        for target, value in [
            ("local_available", lambda: False), ("_GROQ", None), ("to_flac", self.to_flac),
            ("probe_duration", lambda path: 1800.0), ("split_audio", lambda path, seconds: list(self.chunks)),
            ("date", FixedDate),
        ]:
            patcher = mock.patch.object(transcribe, target, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        for patcher in (mock.patch.dict(sys.modules, {"groq": self.groq.module}),
                        mock.patch.dict(os.environ, {"GROQ_API_KEY": "test-groq-key"}),
                        mock.patch.object(transcribe.shutil, "which", lambda name: f"/usr/bin/{name}")):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.source = self.dir / "meeting.ogg"
        self.source.write_bytes(b"audio")

    def to_flac(self, path: Path) -> Path:
        out = self.dir / f"{path.stem}.flac"
        out.write_bytes(b"flac")
        self.converted.append(out)
        return out

    def run_tool(self, *flags: str) -> tuple[int, dict, str]:
        code, out, err = support.run_main(transcribe, [str(self.source), "--output-dir", str(self.dir / "out"), *flags])
        return code, json.loads(out), err

    def test_one_client_whose_sdk_retries(self):
        code, out, err = self.run_tool("--engine", "groq", "--language", "de")
        self.assertEqual(code, 0, err)
        self.assertEqual(out["transcript"], "text of chunk-000.flac text of chunk-001.flac text of chunk-002.flac")
        self.assertEqual(self.groq.clients, [{"api_key": "test-groq-key", "max_retries": transcribe.GROQ_RETRIES}])
        self.assertEqual([u["file"] for u in self.groq.uploads],
                         [(f"chunk-00{i}.flac", b"flac") for i in range(3)])
        self.assertTrue(all(u["language"] == "de" and u["model"] == "whisper-large-v3" for u in self.groq.uploads))
        self.assertFalse(any(p.exists() for p in self.converted), "the FLAC copies are removed")

    def test_errors_are_not_retried_again(self):
        # The SDK has retried a rate limit already; a second loop here made
        # one chunk about 15 requests, and took "sample rate" for a limit.
        for msg in ("429 rate limit reached", "unsupported sample rate"):
            with self.subTest(msg):
                self.groq.uploads.clear()
                self.groq.fail_with = RuntimeError(msg)
                code, out, _err = self.run_tool("--engine", "groq")
                self.assertEqual(code, 1)
                self.assertEqual(out["error"], f"Groq transcription failed: {msg}")
                self.assertEqual(len(self.groq.uploads), 1)

    def test_an_upload_over_groqs_limit(self):
        with mock.patch.object(transcribe, "GROQ_MAX_UPLOAD", 3):
            code, out, _ = self.run_tool("--engine", "groq")
        self.assertEqual(code, 1)
        self.assertIn("pass a smaller --chunk-seconds", out["error"])
        self.assertEqual(self.groq.uploads, [])


class FFmpegTest(TempDirTestCase):
    """What transcribe.py asks ffmpeg for, with ffmpeg itself faked."""

    def setUp(self):
        super().setUp()
        self.calls: list[list[str]] = []
        for patcher in (mock.patch.object(transcribe.subprocess, "run", self.ffmpeg),
                        mock.patch.object(transcribe.shutil, "which", lambda name: f"/usr/bin/{name}")):
            patcher.start()
            self.addCleanup(patcher.stop)

    def ffmpeg(self, cmd, **kwargs):
        self.calls.append(cmd)
        out = Path(cmd[-1])
        if "%03d" in out.name:
            for i in range(2):
                (out.parent / (out.name % i)).write_bytes(b"pcm")
        else:
            out.write_bytes(b"out")
        return types.SimpleNamespace(returncode=0, stdout="", stderr="")

    def test_groq_gets_16khz_mono_flac(self):
        out = transcribe.to_flac(self.dir / "voice.ogg")
        self.addCleanup(out.unlink, missing_ok=True)
        (cmd,) = self.calls
        self.assertEqual(cmd[:4], ["ffmpeg", "-y", "-i", str(self.dir / "voice.ogg")])
        self.assertIn("-ac 1 -ar 16000 -c:a flac", " ".join(cmd))
        self.assertEqual(out.suffix, ".flac")

    def test_chunks_are_decoded_not_copied(self):
        chunks = transcribe.split_audio(self.dir / "meeting.m4a", 600)
        self.addCleanup(shutil.rmtree, chunks[0].parent, True)
        (cmd,) = self.calls
        self.assertNotIn("copy", cmd)
        self.assertIn("-acodec pcm_s16le -ac 1 -ar 16000", " ".join(cmd))
        self.assertEqual([c.name for c in chunks], ["chunk-000.wav", "chunk-001.wav"])


class ErrorBoundaryTest(TranscribeTestCase):
    def test_anything_unexpected_is_a_json_error(self):
        with mock.patch.object(transcribe, "build_filename", side_effect=KeyError("boom")):
            code, out, err = self.transcribe(str(self.media("note.ogg", b"audio")))
        self.assertEqual(code, 1)
        self.assertEqual(out["error"], "Transcription failed: KeyError: 'boom'")
        self.assertIn("Traceback", err)  # for whoever debugs it, on stderr

    def test_a_usage_mistake_is_a_json_error(self):
        code, out, err = support.run_main(transcribe, ["--engine", "whisper"])
        self.assertEqual(code, 1)
        self.assertIn("invalid choice", json.loads(out)["error"])
        self.assertIn("usage:", err)


if __name__ == "__main__":
    unittest.main()
