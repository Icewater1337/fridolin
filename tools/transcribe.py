#!/usr/bin/env python3
"""Transcribe an audio or video file to text.

Handles the things that go wrong with long recordings so the assistant does
not have to: video audio extraction, splitting long files into chunks the
model can actually hold, running a local Whisper model when one is installed,
and falling back to the Groq API per chunk, which the Groq SDK retries with
backoff.

Usage
-----
    transcribe.py <file> [--output-dir DIR] [options]

    transcribe.py voice-note.ogg --output-dir ~/assistant/personal/transcripts
    transcribe.py meeting.mp4 --language de --output-dir ./transcripts
    transcribe.py interview.m4a --engine groq --output-dir ./transcripts

Output is JSON on stdout: transcript, transcript_file, audio_file, language,
engine, duration_seconds. An error is {"error": ...} on stdout with exit
status 1, whatever failed.

Both the transcript and a copy of the original media are written to the output
directory under the same stem, so they stay paired:

    2026-01-15 - Notes from the planning call.txt
    2026-01-15 - Notes from the planning call.ogg

--move-original moves the original there instead of copying it, which is how
a file in the bridge's media inbox leaves it; --no-keep-original leaves it
where it is and writes the transcript alone. The tool never moves a file
unless it is told to.

Nothing is overwritten: when a stem is already taken (two short "Okay" notes
on one day), the new pair gets "2026-01-15 - Okay (2)", then "(3)", and so on.

Engines
-------
`--engine auto` (the default) uses a local model if `transformers` and `torch`
are installed, and falls back to Groq per chunk when the local run fails (a
model that cannot be loaded at all is not retried for every chunk). `local`
and `groq` force one engine. `--prompt` reaches the model with either; keep
it to a few sentences, since Whisper reads only the last 223 tokens of it.

    Local:  pip install -r tools/requirements-transcription.txt
            First run downloads the model (~1.5 GB for large-v3-turbo).
    Groq:   pip install groq          (no local model needed, much lighter)
            and set GROQ_API_KEY: in the bridge's environment, listed in its
            config's child_env_passthrough, or brains do not get it
            (docs/CONFIGURATION.md#child_env_passthrough). Each file goes up
            as 16 kHz mono FLAC, as Groq recommends, which keeps a chunk of
            the default ten minutes well under Groq's upload limit.

`--model` takes any Whisper-compatible model ID from the Hugging Face hub.
A dialect- or language-specific fine-tune usually beats the base model by a
wide margin — worth searching for one that matches how people actually speak
in your recordings.

Requires ffmpeg on PATH for every run: each Groq upload is converted with
it, and the local model reads audio through it. ffprobe is needed for video
input and to split a long file into chunks; without it a file goes whole.
"""

from __future__ import annotations

import argparse
import os
import re
import shlex
import shutil
import subprocess
import tempfile
import traceback
from dataclasses import dataclass
from datetime import date
from pathlib import Path
from typing import Any

from toolkit import ArgumentParser, emit_json, fail, log

DEFAULT_MODEL = os.environ.get("TRANSCRIBE_MODEL", "openai/whisper-large-v3-turbo")
VIDEO_EXTS = {".mp4", ".mov", ".webm", ".3gp", ".mkv", ".avi", ".m4v"}
DEFAULT_CHUNK_SECONDS = 600  # split anything longer than ~10 minutes
GROQ_MODEL = "whisper-large-v3"
# The SDK retries a request that hit the rate limit, a 5xx or a timeout,
# with exponential backoff and Groq's retry-after; its default is 2.
GROQ_RETRIES = 5
# Groq refuses uploads over 25 MB (on its free tier). Ten minutes of 16 kHz
# mono FLAC are a few MB, so only a much longer --chunk-seconds gets there.
GROQ_MAX_UPLOAD = 25 * 1000 * 1000
# Whisper's decoder holds 448 tokens in all: the prompt, a few start tokens
# and the text of one 30 s window. Like openai-whisper, give the prompt at
# most half.
PROMPT_TOKENS = 448 // 2 - 1
# Brains run this tool from their own folder, so an install hint names the
# requirements file next to it rather than a path relative to the repo.
LOCAL_REQUIREMENTS = Path(__file__).resolve().parent / "requirements-transcription.txt"
# A key exported in some shell does not reach a brain: the bridge hands
# brains only the variables on its own list and those its config names.
GROQ_KEY_HINT = ("set GROQ_API_KEY in the bridge's environment and list it in the bridge config's "
                 "child_env_passthrough (docs/CONFIGURATION.md#child_env_passthrough)")


def require_binary(name: str) -> None:
    if shutil.which(name) is None:
        fail(f"{name} not found on PATH. Install ffmpeg (macOS: brew install ffmpeg).")


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
        fail(f"ffmpeg could not extract audio: {r.stderr[-500:]}")
    return tmp


def split_audio(audio_path: Path, chunk_seconds: int) -> list[Path]:
    """Split audio into chunks of at most chunk_seconds, returning their paths."""
    require_binary("ffmpeg")
    tmpdir = Path(tempfile.mkdtemp(prefix="transcribe-chunks-"))
    pattern = str(tmpdir / "chunk-%03d.wav")

    # Decoded to 16 kHz mono PCM, which both engines take: a stream copy of
    # a compressed format would cut it where it cannot be decoded.
    r = subprocess.run(
        ["ffmpeg", "-y", "-i", str(audio_path), "-f", "segment",
         "-segment_time", str(chunk_seconds), "-acodec", "pcm_s16le",
         "-ac", "1", "-ar", "16000", pattern],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        shutil.rmtree(tmpdir, ignore_errors=True)
        fail(f"ffmpeg could not split the audio: {r.stderr[-500:]}")

    return sorted(tmpdir.glob("chunk-*.wav"))


# ── local engine ──────────────────────────────────────────────────────────

_PIPE: Any = None
_PROCESSOR: Any = None  # kept next to _PIPE: it turns --prompt into prompt token IDs


def local_available() -> bool:
    try:
        import torch  # noqa: F401
        import transformers  # noqa: F401
        return True
    except ImportError:
        return False


def load_local_pipeline(model_id: str) -> Any:
    """Load the local ASR pipeline once and reuse it across chunks."""
    global _PIPE, _PROCESSOR
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
    _PROCESSOR = processor
    return _PIPE


def transcribe_local(audio_path: Path, model_id: str, language: str | None, prompt: str | None) -> str:
    pipe = load_local_pipeline(model_id)
    generate_kwargs = {"language": language} if language else {}
    if prompt:
        # Whisper reads the prompt as text that came before the audio, which
        # steers spelling and vocabulary. Each 30 s window is conditioned on
        # it, and the pipeline strips it from the output again.
        generate_kwargs["prompt_ids"] = fit_prompt(_PROCESSOR.get_prompt_ids(prompt, return_tensors="pt"))
    result = pipe(str(audio_path), generate_kwargs=generate_kwargs)
    return result["text"].strip()


def fit_prompt(ids: Any) -> Any:
    """Cut Whisper prompt IDs to PROMPT_TOKENS, keeping the start-of-prompt token.

    A prompt that fills the decoder fails every window, and --engine auto
    would then quietly send the whole recording to Groq; one just under the
    limit leaves no room for the transcript, which comes out as the prompt
    repeated. Keep the end of the text, as openai-whisper does: it is what
    the model reads right before the audio.
    """
    if len(ids) <= PROMPT_TOKENS + 1:
        return ids
    import torch
    log(f"The prompt is {len(ids) - 1} tokens; Whisper reads only the last {PROMPT_TOKENS}.")
    return torch.cat([ids[:1], ids[-PROMPT_TOKENS:]])


# ── Groq engine ───────────────────────────────────────────────────────────

_GROQ: Any = None  # one client for the whole run


def groq_available() -> bool:
    if not os.environ.get("GROQ_API_KEY"):
        return False
    try:
        import groq  # noqa: F401
        return True
    except ImportError:
        return False


def groq_client() -> Any:
    """The run's Groq client, made on first use.

    The SDK retries what is worth retrying (rate limits, 5xx, timeouts) and
    waits as long as Groq's retry-after says, so nothing here retries again.
    """
    global _GROQ
    if _GROQ is None:
        api_key = os.environ.get("GROQ_API_KEY")
        if not api_key:
            raise RuntimeError("GROQ_API_KEY is not set")
        from groq import Groq
        _GROQ = Groq(api_key=api_key, max_retries=GROQ_RETRIES)
    return _GROQ


def to_flac(audio_path: Path) -> Path:
    """A 16 kHz mono FLAC copy of audio_path, which Groq recommends: it is
    what the model hears anyway, and far smaller than the WAV chunks or a
    stereo recording."""
    require_binary("ffmpeg")
    fd, tmp_name = tempfile.mkstemp(suffix=".flac", prefix="transcribe-")
    os.close(fd)
    tmp = Path(tmp_name)
    r = subprocess.run(
        ["ffmpeg", "-y", "-i", str(audio_path), "-vn", "-ac", "1", "-ar", "16000", "-c:a", "flac", str(tmp)],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        tmp.unlink(missing_ok=True)
        raise RuntimeError(f"ffmpeg could not convert the audio for Groq: {r.stderr[-500:]}")
    return tmp


def transcribe_groq(audio_path: Path, language: str | None, prompt: str | None) -> str:
    """Transcribe via the Groq API."""
    client = groq_client()
    upload = to_flac(audio_path)
    try:
        size = upload.stat().st_size
        if size > GROQ_MAX_UPLOAD:
            raise RuntimeError(f"the audio is {size / 1e6:.0f} MB as FLAC, over Groq's "
                               f"{GROQ_MAX_UPLOAD / 1e6:.0f} MB; pass a smaller --chunk-seconds")
        kwargs = {"file": (upload.name, upload.read_bytes()), "model": GROQ_MODEL}
        if language:
            kwargs["language"] = language
        if prompt:
            kwargs["prompt"] = prompt
        return client.audio.transcriptions.create(**kwargs).text.strip()
    finally:
        upload.unlink(missing_ok=True)


# ── orchestration ─────────────────────────────────────────────────────────

@dataclass
class Engines:
    """The engines this run may use, decided once before any work starts."""
    local: bool
    groq: bool


def choose_engines(engine: str) -> Engines:
    use_local = engine in ("auto", "local") and local_available()
    use_groq = engine in ("auto", "groq") and groq_available()

    install_local = f"pip install -r {shlex.quote(str(LOCAL_REQUIREMENTS))}"
    if engine == "local" and not use_local:
        fail(f"--engine local, but transformers/torch are not installed. Run: {install_local}")
    if engine == "groq" and not use_groq:
        fail("--engine groq, but GROQ_API_KEY is unset or the groq package is missing. "
            f"Run: pip install groq, and {GROQ_KEY_HINT}.")
    if not use_local and not use_groq:
        fail(f"No transcription engine available. Either install the local model "
            f"({install_local}) or install the groq package and {GROQ_KEY_HINT}.")
    return Engines(local=use_local, groq=use_groq)


def transcribe_chunk(path: Path, args: argparse.Namespace, available: Engines) -> tuple[str, str]:
    """Transcribe one file, returning (text, engine_used)."""
    if available.local:
        try:
            load_local_pipeline(args.model)
        except Exception as e:
            if not available.groq:
                fail(f"The local model could not be loaded and no fallback is configured: {e}")
            # Loading again for every chunk would only fail again, slowly.
            log(f"The local model could not be loaded ({e}); using Groq for the rest")
            available.local = False
    if available.local:
        try:
            return transcribe_local(path, args.model, args.language, args.prompt), f"local:{args.model}"
        except Exception as e:
            if not available.groq:
                fail(f"Local transcription failed and no fallback is configured: {e}")
            log(f"Local model failed ({e}); falling back to Groq")

    try:
        return transcribe_groq(path, args.language, args.prompt), f"groq:{GROQ_MODEL}"
    except Exception as e:
        fail(f"Groq transcription failed: {e}")


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
    ap = ArgumentParser(
        description="Transcribe an audio or video file to text.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    ap.add_argument("file", help="audio or video file to transcribe")
    ap.add_argument("--output-dir", default="transcripts",
                    help="where to write the transcript and a copy of the media "
                         "(default: transcripts in the current folder, which is the "
                         "brain's own folder when a brain runs this)")
    ap.add_argument("--engine", choices=["auto", "local", "groq"], default="auto",
                    help="auto (default) prefers a local model and falls back to Groq")
    ap.add_argument("--model", default=DEFAULT_MODEL,
                    help=f"local Whisper model ID (default: {DEFAULT_MODEL})")
    ap.add_argument("--language", default=os.environ.get("TRANSCRIBE_LANGUAGE"),
                    help="ISO language code, e.g. en, de, fr. Omit to auto-detect.")
    ap.add_argument("--prompt", default=None,
                    help="hint passed to the model — names, jargon, or the dialect "
                         "spoken, which measurably improves accuracy. Keep it to a "
                         f"few sentences: Whisper reads only its last {PROMPT_TOKENS} tokens")
    ap.add_argument("--chunk-seconds", type=int, default=DEFAULT_CHUNK_SECONDS,
                    help=f"split longer files into chunks of this size (default: {DEFAULT_CHUNK_SECONDS})")
    ap.add_argument("--keep-original", action=argparse.BooleanOptionalAction, default=True,
                    help="copy the source media next to the transcript")
    ap.add_argument("--move-original", action="store_true",
                    help="move the source media next to the transcript instead of copying it, "
                         "such as a file out of the bridge's media inbox")
    args = ap.parse_args()
    if args.move_original and not args.keep_original:
        ap.error("--move-original keeps the original next to the transcript; it cannot go with --no-keep-original")
    # The one exception boundary: whatever goes wrong, stdout is a JSON error
    # the assistant can read, never just a traceback on stderr.
    try:
        run(args)
    except Exception as e:
        traceback.print_exc()
        fail(f"Transcription failed: {type(e).__name__}: {e}")


def run(args: argparse.Namespace) -> None:
    source = Path(args.file).expanduser()
    if not source.exists():
        fail(f"File not found: {source}")

    output_dir = Path(args.output_dir).expanduser()
    output_dir.mkdir(parents=True, exist_ok=True)

    # Before any slow work, so a missing engine fails fast.
    available = choose_engines(args.engine)

    # Video needs its audio track pulled out first.
    working = source
    tmp_audio = None
    if source.suffix.lower() in VIDEO_EXTS:
        require_binary("ffprobe")
        log("Extracting the audio track…")
        tmp_audio = extract_audio(source)
        working = tmp_audio

    chunk_dir = None
    used: set[str] = set()  # the engine labels that produced text
    duration = 0.0

    try:
        if shutil.which("ffprobe"):
            duration = probe_duration(working)

        # The 1.1 factor avoids splitting a file that is only just over the
        # limit into one large chunk and one tiny one.
        if duration > args.chunk_seconds * 1.1:
            chunks = split_audio(working, args.chunk_seconds)
            if not chunks:
                fail("Splitting produced no chunks")
            chunk_dir = chunks[0].parent

            segments = []
            for i, chunk in enumerate(chunks, 1):
                log(f"Chunk {i}/{len(chunks)}…")
                seg_text, engine = transcribe_chunk(chunk, args, available)
                segments.append(seg_text)
                used.add(engine)
            text = " ".join(s for s in segments if s).strip()
        else:
            text, engine = transcribe_chunk(working, args, available)
            used.add(engine)
    finally:
        if tmp_audio is not None:
            tmp_audio.unlink(missing_ok=True)
        if chunk_dir is not None:
            shutil.rmtree(chunk_dir, ignore_errors=True)

    if not text:
        fail("Transcription produced no text")

    stem = build_filename(text, date.today().strftime("%Y-%m-%d"))
    media_suffix = media_suffix_of(source) if args.keep_original else None
    transcript_path, audio_dest = claim_paths(output_dir, stem, media_suffix)

    transcript_path.write_text(text, encoding="utf-8")
    if audio_dest is not None and args.move_original:
        # Over the empty file claim_paths made: a rename on one file system,
        # else a copy, then the original is removed.
        shutil.move(str(source), str(audio_dest))
    elif audio_dest is not None:
        shutil.copy2(source, audio_dest)

    emit_json({
        "transcript": text,
        "transcript_file": str(transcript_path),
        "audio_file": str(audio_dest) if audio_dest else None,
        "language": args.language or "auto-detected",
        "engine": "+".join(sorted(used)),
        "duration_seconds": round(duration, 1) if duration else None,
    })


if __name__ == "__main__":
    main()
