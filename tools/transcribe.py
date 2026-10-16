#!/usr/bin/env python3
"""Transcribe an audio or video file to text.

Handles the things that go wrong with long recordings so the assistant does
not have to: video audio extraction, splitting long files into chunks the
model can actually hold, running a local Whisper model when one is installed,
and falling back to the Groq API per chunk with rate-limit backoff.

Usage
-----
    transcribe.py <file> [--output-dir DIR] [options]

    transcribe.py voice-note.ogg --output-dir ~/assistant/personal/transcripts
    transcribe.py meeting.mp4 --language de --output-dir ./transcripts
    transcribe.py interview.m4a --engine groq --output-dir ./transcripts

Output is JSON on stdout: transcript, transcript_file, audio_file, language,
engine, duration_seconds.

Both the transcript and a copy of the original media are written to the output
directory under the same stem, so they stay paired:

    2026-01-15 - Notes from the planning call.txt
    2026-01-15 - Notes from the planning call.ogg

Nothing is overwritten: when a stem is already taken (two short "Okay" notes
on one day), the new pair gets "2026-01-15 - Okay (2)", then "(3)", and so on.

Engines
-------
`--engine auto` (the default) uses a local model if `transformers` and `torch`
are installed, and falls back to Groq per chunk when the local run fails.
`local` and `groq` force one engine.

    Local:  pip install -r tools/requirements-transcription.txt
            First run downloads the model (~1.5 GB for large-v3-turbo).
    Groq:   export GROQ_API_KEY=...     (no local model needed, much lighter)

`--model` takes any Whisper-compatible model ID from the Hugging Face hub.
A dialect- or language-specific fine-tune usually beats the base model by a
wide margin — worth searching for one that matches how people actually speak
in your recordings.

Requires ffmpeg and ffprobe on PATH for video input and for chunking.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
from datetime import date
from pathlib import Path

DEFAULT_MODEL = os.environ.get("TRANSCRIBE_MODEL", "openai/whisper-large-v3-turbo")
VIDEO_EXTS = {".mp4", ".mov", ".webm", ".3gp", ".mkv", ".avi", ".m4v"}
DEFAULT_CHUNK_SECONDS = 600  # split anything longer than ~10 minutes
GROQ_MODEL = "whisper-large-v3"
GROQ_MIN_INTERVAL = 6.0      # seconds between Groq calls, to stay under the rate limit


def die(msg: str) -> None:
    print(json.dumps({"error": msg}))
    sys.exit(1)


def log(msg: str) -> None:
    """Progress goes to stderr so stdout stays parseable JSON."""
    print(msg, file=sys.stderr)


def require_binary(name: str) -> None:
    if shutil.which(name) is None:
        die(f"{name} not found on PATH. Install ffmpeg (macOS: brew install ffmpeg).")


# ── media preparation ─────────────────────────────────────────────────────

def probe_duration(path: Path) -> float:
    """Duration in seconds via ffprobe, or 0.0 if it can't be determined."""
    r = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "default=noprint_wrappers=1:nokey=1", str(path)],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        return 0.0
    try:
        return float(r.stdout.strip())
    except ValueError:
        return 0.0


def extract_audio(video_path: Path) -> Path:
    """Extract a video's audio track to a temporary 16 kHz mono PCM wav.

    PCM is what the local pipeline ultimately consumes, and decoding it up
    front avoids the segment-boundary decode failures that compressed formats
    hit when a file is later split into chunks.
    """
    require_binary("ffmpeg")
    fd, tmp_name = tempfile.mkstemp(suffix=".wav", prefix="transcribe-")
    os.close(fd)
    tmp = Path(tmp_name)

    r = subprocess.run(
        ["ffmpeg", "-y", "-i", str(video_path), "-vn",
         "-acodec", "pcm_s16le", "-ac", "1", "-ar", "16000", str(tmp)],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        tmp.unlink(missing_ok=True)
        die(f"ffmpeg could not extract audio: {r.stderr[-500:]}")
    return tmp


def split_audio(audio_path: Path, chunk_seconds: int) -> list[Path]:
    """Split audio into chunks of at most chunk_seconds, returning their paths."""
    require_binary("ffmpeg")
    tmpdir = Path(tempfile.mkdtemp(prefix="transcribe-chunks-"))
    pattern = str(tmpdir / "chunk-%03d.wav")

    # Stream copy is fast and lossless, and safe for PCM because samples are
    # independently decodable. Re-encode only if that fails.
    r = subprocess.run(
        ["ffmpeg", "-y", "-i", str(audio_path), "-f", "segment",
         "-segment_time", str(chunk_seconds), "-c", "copy", pattern],
        capture_output=True, text=True,
    )
    if r.returncode != 0 or not list(tmpdir.glob("chunk-*.wav")):
        for f in tmpdir.glob("chunk-*.wav"):
            f.unlink(missing_ok=True)
        r = subprocess.run(
            ["ffmpeg", "-y", "-i", str(audio_path), "-f", "segment",
             "-segment_time", str(chunk_seconds), "-acodec", "pcm_s16le",
             "-ac", "1", "-ar", "16000", pattern],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            shutil.rmtree(tmpdir, ignore_errors=True)
            die(f"ffmpeg could not split the audio: {r.stderr[-500:]}")

    return sorted(tmpdir.glob("chunk-*.wav"))


# ── local engine ──────────────────────────────────────────────────────────

_PIPE = None


def local_available() -> bool:
    try:
        import torch  # noqa: F401
        import transformers  # noqa: F401
        return True
    except ImportError:
        return False


def load_local_pipeline(model_id: str):
    """Load the local ASR pipeline once and reuse it across chunks."""
    global _PIPE
    if _PIPE is not None:
        return _PIPE

    import torch
    from transformers import AutoModelForSpeechSeq2Seq, AutoProcessor, pipeline

    # CPU + float32 deliberately. Apple's MPS backend NaN-propagates on chunked
    # Whisper, and the logits collapse into a single repeated token — output
    # that looks like a transcript but is garbage. Slower and correct beats
    # faster and silently wrong.
    device = "cpu"
    torch_dtype = torch.float32

    log(f"Loading {model_id} (first run downloads the model)…")
    model = AutoModelForSpeechSeq2Seq.from_pretrained(
        model_id, torch_dtype=torch_dtype,
        low_cpu_mem_usage=True, use_safetensors=True,
    )
    model.to(device)
    processor = AutoProcessor.from_pretrained(model_id)

    # chunk_length_s + stride_length_s opt into pipeline-level chunking: audio
    # is sliced at the model's native 30 s context with 5 s overlap and the
    # results stitched, which avoids Whisper's long-form generation path.
    _PIPE = pipeline(
        "automatic-speech-recognition",
        model=model,
        tokenizer=processor.tokenizer,
        feature_extractor=processor.feature_extractor,
        torch_dtype=torch_dtype,
        device=device,
        chunk_length_s=30,
        stride_length_s=5,
    )
    return _PIPE


def transcribe_local(audio_path: Path, model_id: str, language: str | None) -> str:
    pipe = load_local_pipeline(model_id)
    generate_kwargs = {"language": language} if language else {}
    result = pipe(str(audio_path), generate_kwargs=generate_kwargs)
    return result["text"].strip()


# ── Groq engine ───────────────────────────────────────────────────────────

_LAST_GROQ_CALL = 0.0


def groq_available() -> bool:
    if not os.environ.get("GROQ_API_KEY"):
        return False
    try:
        import groq  # noqa: F401
        return True
    except ImportError:
        return False


def transcribe_groq(audio_path: Path, language: str | None, prompt: str | None) -> str:
    """Transcribe via the Groq API, paced and retried on rate limits."""
    global _LAST_GROQ_CALL

    api_key = os.environ.get("GROQ_API_KEY")
    if not api_key:
        raise RuntimeError("GROQ_API_KEY is not set")

    from groq import Groq
    client = Groq(api_key=api_key)

    # Groq limits audio-seconds per minute, so pace calls rather than firing
    # every chunk at once and eating a wall of 429s.
    delta = time.monotonic() - _LAST_GROQ_CALL
    if delta < GROQ_MIN_INTERVAL:
        time.sleep(GROQ_MIN_INTERVAL - delta)

    attempts, delay = 5, 10
    for i in range(attempts):
        try:
            with open(audio_path, "rb") as f:
                kwargs = {"file": (audio_path.name, f.read()), "model": GROQ_MODEL}
                if language:
                    kwargs["language"] = language
                if prompt:
                    kwargs["prompt"] = prompt
                transcription = client.audio.transcriptions.create(**kwargs)
            _LAST_GROQ_CALL = time.monotonic()
            return transcription.text.strip()
        except Exception as e:
            msg = str(e).lower()
            if ("rate" in msg or "429" in msg) and i < attempts - 1:
                log(f"Groq rate-limited; retrying in {delay}s (attempt {i + 1}/{attempts})")
                time.sleep(delay)
                delay = min(delay * 2, 120)
                continue
            raise
    return ""


# ── orchestration ─────────────────────────────────────────────────────────

def transcribe_chunk(path: Path, args) -> tuple[str, str]:
    """Transcribe one file, returning (text, engine_used)."""
    use_local = args.engine in ("auto", "local") and local_available()
    use_groq = args.engine in ("auto", "groq") and groq_available()

    if args.engine == "local" and not use_local:
        die("--engine local, but transformers/torch are not installed. "
            "Run: pip install -r tools/requirements-transcription.txt")
    if args.engine == "groq" and not use_groq:
        die("--engine groq, but GROQ_API_KEY is unset or the groq package is missing. "
            "Run: pip install groq  and export GROQ_API_KEY=...")
    if not use_local and not use_groq:
        die("No transcription engine available. Either install the local model "
            "(pip install -r tools/requirements-transcription.txt) or set "
            "GROQ_API_KEY and install the groq package.")

    if use_local:
        try:
            return transcribe_local(path, args.model, args.language), f"local:{args.model}"
        except Exception as e:
            if not use_groq:
                die(f"Local transcription failed and no fallback is configured: {e}")
            log(f"Local model failed ({e}); falling back to Groq")

    return transcribe_groq(path, args.language, args.prompt), f"groq:{GROQ_MODEL}"


def build_filename(text: str, when: str) -> str:
    """Make a readable, sortable stem from the start of the transcript."""
    summary = re.sub(r'[/\\:*?"<>|\r\n]', " ", text[:100])
    summary = " ".join(summary.split())

    # A near-silent recording transcribes to punctuation like "." or "...",
    # which would produce filenames such as "2026-01-15 - ..txt". Anything
    # without a letter or digit is not a usable summary.
    if not re.search(r"\w", summary, re.UNICODE):
        return f"{when} - transcript"

    if len(summary) > 60:
        # Trailing dots double up against the file extension, so trim them
        # before adding the single-character ellipsis.
        summary = summary[:59].rstrip(". ") + "…"

    # A leading dot would make the file hidden; trailing dots double up against
    # the extension. The "…" added above is neither, so it survives this.
    summary = summary.strip(". ")

    return f"{when} - {summary}"


def media_suffix_of(source: Path) -> str:
    """The extension for the media copy; never .txt, which is the transcript's."""
    suffix = source.suffix
    return suffix if suffix and suffix.lower() != ".txt" else ".audio"


def claim_paths(output_dir: Path, stem: str, media_suffix: str | None) -> tuple[Path, Path | None]:
    """Create the transcript file, and the media copy's, under a stem nothing uses yet.

    Short or near-silent notes from the same day share a stem ("2026-01-15 -
    Okay"), and writing to a fixed name would replace an earlier recording's
    transcript and media. Try "stem", then "stem (2)", "stem (3)", …; each
    file is created with O_EXCL, so checking a name and taking it is one step
    even when two transcriptions finish at once.
    """
    n = 0
    while True:
        n += 1
        candidate = stem if n == 1 else f"{stem} ({n})"
        transcript = output_dir / f"{candidate}.txt"
        media = output_dir / f"{candidate}{media_suffix}" if media_suffix else None
        try:
            transcript.open("x").close()
        except FileExistsError:
            continue
        if media is not None:
            try:
                media.open("xb").close()
            except FileExistsError:
                transcript.unlink()
                continue
        return transcript, media


def main() -> None:
    ap = argparse.ArgumentParser(
        description="Transcribe an audio or video file to text.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    ap.add_argument("file", help="audio or video file to transcribe")
    ap.add_argument("--output-dir", default="transcripts",
                    help="where to write the transcript and a copy of the media "
                         "(default: ./transcripts)")
    ap.add_argument("--engine", choices=["auto", "local", "groq"], default="auto",
                    help="auto (default) prefers a local model and falls back to Groq")
    ap.add_argument("--model", default=DEFAULT_MODEL,
                    help=f"local Whisper model ID (default: {DEFAULT_MODEL})")
    ap.add_argument("--language", default=os.environ.get("TRANSCRIBE_LANGUAGE"),
                    help="ISO language code, e.g. en, de, fr. Omit to auto-detect.")
    ap.add_argument("--prompt", default=None,
                    help="hint passed to the model — names, jargon, or the dialect "
                         "spoken, which measurably improves accuracy")
    ap.add_argument("--chunk-seconds", type=int, default=DEFAULT_CHUNK_SECONDS,
                    help=f"split longer files into chunks of this size (default: {DEFAULT_CHUNK_SECONDS})")
    ap.add_argument("--keep-original", action="store_true", default=True,
                    help="copy the source media next to the transcript (default: on)")
    ap.add_argument("--no-keep-original", dest="keep_original", action="store_false")
    args = ap.parse_args()

    source = Path(args.file).expanduser()
    if not source.exists():
        die(f"File not found: {source}")

    output_dir = Path(args.output_dir).expanduser()
    output_dir.mkdir(parents=True, exist_ok=True)

    # Video needs its audio track pulled out first.
    working = source
    tmp_audio = None
    if source.suffix.lower() in VIDEO_EXTS:
        require_binary("ffprobe")
        log("Extracting the audio track…")
        tmp_audio = extract_audio(source)
        working = tmp_audio

    chunk_dir = None
    engines: set[str] = set()
    duration = 0.0

    try:
        if shutil.which("ffprobe"):
            duration = probe_duration(working)

        # The 1.1 factor avoids splitting a file that is only just over the
        # limit into one large chunk and one tiny one.
        if duration > args.chunk_seconds * 1.1:
            chunks = split_audio(working, args.chunk_seconds)
            if not chunks:
                die("Splitting produced no chunks")
            chunk_dir = chunks[0].parent

            segments = []
            for i, chunk in enumerate(chunks, 1):
                log(f"Chunk {i}/{len(chunks)}…")
                seg_text, engine = transcribe_chunk(chunk, args)
                segments.append(seg_text)
                engines.add(engine)
            text = " ".join(s for s in segments if s).strip()
        else:
            text, engine = transcribe_chunk(working, args)
            engines.add(engine)
    finally:
        if tmp_audio is not None:
            tmp_audio.unlink(missing_ok=True)
        if chunk_dir is not None:
            shutil.rmtree(chunk_dir, ignore_errors=True)

    if not text:
        die("Transcription produced no text")

    stem = build_filename(text, date.today().strftime("%Y-%m-%d"))
    media_suffix = media_suffix_of(source) if args.keep_original else None
    transcript_path, audio_dest = claim_paths(output_dir, stem, media_suffix)

    transcript_path.write_text(text, encoding="utf-8")
    if audio_dest is not None:
        shutil.copy2(source, audio_dest)

    print(json.dumps({
        "transcript": text,
        "transcript_file": str(transcript_path),
        "audio_file": str(audio_dest) if audio_dest else None,
        "language": args.language or "auto-detected",
        "engine": "+".join(sorted(engines)),
        "duration_seconds": round(duration, 1) if duration else None,
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
