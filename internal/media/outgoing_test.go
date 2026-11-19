package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"strings"
	"testing"
)

// oggPage is an Ogg page with granule position granule and one packet.
func oggPage(granule uint64, packet []byte) []byte {
	h := make([]byte, 27)
	copy(h, "OggS")
	binary.LittleEndian.PutUint64(h[6:], granule)
	h[26] = 1 // one segment
	return append(append(h, byte(len(packet))), packet...)
}

// opusHead is an OpusHead packet with pre-skip preSkip.
func opusHead(preSkip uint16) []byte {
	p := make([]byte, 19)
	copy(p, "OpusHead")
	p[8], p[9] = 1, 1 // version, channels
	binary.LittleEndian.PutUint16(p[10:], preSkip)
	binary.LittleEndian.PutUint32(p[12:], 48000)
	return p
}

// fakeOgg is Ogg Opus audio of samples samples after a pre-skip of 312,
// with a last page that ends no packet.
func fakeOgg(samples uint64) []byte {
	var b bytes.Buffer
	b.Write(oggPage(0, opusHead(312)))
	b.Write(oggPage(0, []byte("OpusTags")))
	b.Write(oggPage(312+samples/2, bytes.Repeat([]byte{1}, 100)))
	b.Write(oggPage(312+samples, bytes.Repeat([]byte{2}, 100)))
	b.Write(oggPage(^uint64(0), bytes.Repeat([]byte{3}, 10)))
	return b.Bytes()
}

func TestSniffType(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpeg := []byte("\xff\xd8\xff\xe0\x00\x10JFIF")
	for _, c := range []struct {
		name string
		head []byte
		want string
	}{
		{"chart.png", png, "image/png"},
		{"chart.pdf", png, "image/png"}, // the content wins over the name
		{"photo.jpg", jpeg, "image/jpeg"},
		{"report.pdf", []byte("%PDF-1.7\n"), "application/pdf"},
		{"data.json", []byte(`{"a": 1}`), "application/json"},
		{"notes.txt", []byte("hello"), "text/plain"},
		{"notes", []byte("hello"), "text/plain"},
		{"blob.unknown-ext", []byte{0, 1, 2, 3}, "application/octet-stream"},
		{"voice.ogg", fakeOgg(48000), "audio/ogg"},
		{"tune.mp3", []byte("ID3\x03\x00\x00\x00"), "audio/mpeg"},
	} {
		if got := SniffType(c.name, c.head); got != c.want {
			t.Errorf("SniffType(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// The length of Ogg Opus audio is its last granule position less the
// pre-skip, rounded up to the second; what is no Ogg Opus has none.
func TestOggOpusSeconds(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		samples uint64
		want    uint32
	}{{0, 0}, {48000, 1}, {48001, 2}, {120000, 3}} {
		ogg := fakeOgg(c.samples)
		got, err := OggOpusSeconds(bytes.NewReader(ogg), int64(len(ogg)))
		if err != nil || got != c.want {
			t.Errorf("%d samples: %d s, %v; want %d s", c.samples, got, err, c.want)
		}
	}
	for _, data := range [][]byte{[]byte("ID3 not ogg"), oggPage(0, []byte("Vorbis..."))} {
		if _, err := OggOpusSeconds(bytes.NewReader(data), int64(len(data))); err == nil {
			t.Errorf("%q: no error", data)
		}
	}
	// A file longer than the tail it reads.
	long := append(fakeOgg(0)[:0:0], oggPage(0, opusHead(312))...)
	long = append(long, bytes.Repeat([]byte{0}, oggTail*2)...)
	long = append(long, oggPage(312+96000, []byte{1})...)
	if got, err := OggOpusSeconds(bytes.NewReader(long), int64(len(long))); err != nil || got != 2 {
		t.Errorf("a long file: %d s, %v; want 2 s", got, err)
	}
	if !IsOggOpus(fakeOgg(1)) || IsOggOpus(oggPage(0, []byte("\x01vorbis"))) {
		t.Error("IsOggOpus is wrong")
	}
}

// wav is silence of seconds seconds as a 16 kHz mono WAV file.
func wav(seconds int) []byte {
	data := make([]byte, 16000*2*seconds)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// ffmpeg makes a voice note of audio, and reads nothing a playlist names.
func TestToVoiceNote(t *testing.T) {
	t.Parallel()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	var out bytes.Buffer
	if err := ToVoiceNote(context.Background(), ffmpeg, bytes.NewReader(wav(2)), &out); err != nil {
		t.Fatal(err)
	}
	if !IsOggOpus(out.Bytes()) {
		t.Fatalf("not Ogg Opus: %q", out.Bytes()[:min(out.Len(), 64)])
	}
	if s, err := OggOpusSeconds(bytes.NewReader(out.Bytes()), int64(out.Len())); err != nil || s != 2 {
		t.Errorf("the voice note plays %d s, %v; want 2 s", s, err)
	}
	playlist := "#EXTM3U\n#EXTINF:1,\nfile:///etc/hosts\n"
	out.Reset()
	if err := ToVoiceNote(context.Background(), ffmpeg, strings.NewReader(playlist), &out); err == nil || out.Len() > 0 {
		t.Errorf("a playlist became %d bytes, %v", out.Len(), err)
	}
}

// A failing ffmpeg is an error that says what it said.
func TestToVoiceNoteFails(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	// sh runs voiceNoteArgs[0] as its script: no such file.
	err = ToVoiceNote(context.Background(), sh, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.HasPrefix(err.Error(), "ffmpeg: ") {
		t.Errorf("err = %v", err)
	}
}
