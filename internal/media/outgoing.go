package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// Outgoing files: what a brain sends into its chat (reply_file). This
// says what a file is, makes a voice note of audio with ffmpeg, and reads
// a voice note's length; the caller finds ffmpeg and sends the file.

// SniffLen is how much of a file SniffType looks at.
const SniffLen = 512

// SniffType is the MIME type, without parameters, of the file named name
// whose first bytes are head: what its content says, or what its name
// says where the content says only that it is some zip, text or bytes (an
// Office document is a zip, a CSV text). Only the content makes a file
// JPEG or PNG, which WhatsApp shows as a photo: a name never does.
func SniffType(name string, head []byte) string {
	t := bareType(http.DetectContentType(head))
	switch t {
	case "application/octet-stream", "application/zip", "text/plain":
		if byName := bareType(mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))); byName != "" && byName != "image/jpeg" && byName != "image/png" {
			return byName
		}
	case "application/ogg":
		if IsOggOpus(head) {
			return "audio/ogg"
		}
	}
	return t
}

func bareType(t string) string {
	if t == "" {
		return ""
	}
	if mt, _, err := mime.ParseMediaType(t); err == nil {
		return mt
	}
	return t
}

// IsOggOpus reports whether head is the start of Ogg Opus audio, which
// WhatsApp plays as a voice note as it is.
func IsOggOpus(head []byte) bool {
	return bytes.HasPrefix(head, []byte("OggS")) && bytes.Contains(head[:min(len(head), 64)], []byte("OpusHead"))
}

// voiceNoteFormats are the demuxers ffmpeg may read a voice note's audio
// with: audio containers only, never a playlist, which could name other
// files to read.
const voiceNoteFormats = "mp3,wav,ogg,flac,aac,mov,matroska,aiff,amr,caf,au,w64"

// voiceNoteArgs are ffmpeg's arguments for a voice note: from its
// standard input, which is the only thing it may open, to Ogg Opus,
// mono, on its standard output.
var voiceNoteArgs = []string{
	"-hide_banner", "-loglevel", "error",
	"-protocol_whitelist", "pipe", "-format_whitelist", voiceNoteFormats,
	"-i", "pipe:0",
	"-vn", "-map_metadata", "-1", "-ac", "1", "-ar", "48000",
	"-c:a", "libopus", "-b:a", "32k", "-application", "voip",
	"-f", "ogg", "pipe:1",
}

// ToVoiceNote converts the audio in gives to a voice note, Ogg Opus, into
// out, with the ffmpeg at the path ffmpeg. ffmpeg reads nothing but in:
// a file made to look like a playlist that names other files gets it to
// read none of them.
func ToVoiceNote(ctx context.Context, ffmpeg string, in io.Reader, out io.Writer) error {
	cmd := exec.CommandContext(ctx, ffmpeg, voiceNoteArgs...)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(textx.Tail(stderr.String(), 300)); msg != "" {
			return fmt.Errorf("ffmpeg: %w: %s", err, msg)
		}
		return fmt.Errorf("ffmpeg: %w", err)
	}
	return nil
}

// errNotOggOpus is OggOpusSeconds' answer for what is no Ogg Opus.
var errNotOggOpus = errors.New("not Ogg Opus audio")

// oggTail is how much of the end of an Ogg file OggOpusSeconds reads for
// its last page, which is far shorter.
const oggTail = 64 << 10

// OggOpusSeconds is how long the Ogg Opus audio in r, size bytes, plays,
// rounded up: the granule position of its last page, less the encoder's
// pre-skip, at Opus's 48 kHz.
func OggOpusSeconds(r io.ReaderAt, size int64) (uint32, error) {
	head := make([]byte, min(size, SniffLen))
	if _, err := r.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	i := bytes.Index(head, []byte("OpusHead"))
	if !bytes.HasPrefix(head, []byte("OggS")) || i < 0 || i+12 > len(head) {
		return 0, errNotOggOpus
	}
	preSkip := uint64(binary.LittleEndian.Uint16(head[i+10:]))
	from := max(0, size-oggTail)
	tail := make([]byte, size-from)
	if _, err := r.ReadAt(tail, from); err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	// The last page whose granule position is set: a page that ends no
	// packet has none (all ones).
	for end := len(tail); ; {
		j := bytes.LastIndex(tail[:end], []byte("OggS"))
		if j < 0 {
			return 0, errNotOggOpus
		}
		end = j
		if j+14 > len(tail) {
			continue
		}
		granule := binary.LittleEndian.Uint64(tail[j+6:])
		if granule == ^uint64(0) {
			continue
		}
		if granule <= preSkip {
			return 0, nil
		}
		samples := granule - preSkip
		return uint32((samples + 47999) / 48000), nil
	}
}
