package voice

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// frame is 60 ms of a tone at level, or of silence for 0.
func frame(level float64) []float32 {
	f := make([]float32, SampleRate*60/1000)
	for i := range f {
		f[i] = float32(level * math.Sqrt2 * math.Sin(2*math.Pi*220*float64(i)/SampleRate))
	}
	return f
}

// feed runs frames of the given levels through s and returns the
// utterances it found, by their length in ms.
func feed(s *Segmenter, levels ...float64) []int {
	var got []int
	for _, l := range levels {
		if u, ok := s.Feed(frame(l)); ok {
			got = append(got, Duration(u))
		}
	}
	return got
}

func repeat(level float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = level
	}
	return out
}

func TestSegmenterFindsUtterances(t *testing.T) {
	t.Parallel()
	s := &Segmenter{}
	var levels []float64
	levels = append(levels, repeat(0.001, 10)...) // line noise
	levels = append(levels, repeat(0.2, 20)...)   // 1.2 s of speech
	levels = append(levels, repeat(0.001, 20)...) // a pause
	levels = append(levels, repeat(0.2, 2)...)    // a click: too short
	levels = append(levels, repeat(0.001, 20)...)
	levels = append(levels, repeat(0.2, 10)...) // 0.6 s
	levels = append(levels, repeat(0.001, 20)...)
	got := feed(s, levels...)
	if len(got) != 2 || got[0] < 1200 || got[0] > 2200 || got[1] < 600 || got[1] > 1600 {
		t.Errorf("utterances %v ms, want about 1200+pause and 600+pause", got)
	}
}

func TestSegmenterCutsALongUtterance(t *testing.T) {
	t.Parallel()
	s := &Segmenter{MaxUtterance: 3000}
	got := feed(s, repeat(0.3, 100)...)
	if len(got) < 1 || got[0] != 3000 {
		t.Errorf("utterances %v ms, want 3000 first", got)
	}
}

func TestSegmenterIgnoresSteadyNoise(t *testing.T) {
	t.Parallel()
	// Noise above the floor, but steady: it is learned as the line's.
	s := &Segmenter{}
	if got := feed(s, repeat(0.02, 200)...); len(got) > 1 {
		t.Errorf("steady noise made utterances %v", got)
	}
}

func TestWAV(t *testing.T) {
	t.Parallel()
	pcm := []float32{0, 0.5, -1, 2}
	w := WAV(pcm)
	if string(w[:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || string(w[36:40]) != "data" {
		t.Fatalf("header %q", w[:44])
	}
	if rate := binary.LittleEndian.Uint32(w[24:]); rate != SampleRate {
		t.Errorf("rate %d", rate)
	}
	if n := binary.LittleEndian.Uint32(w[40:]); n != 8 || len(w) != 52 {
		t.Errorf("data %d bytes, file %d", n, len(w))
	}
	if v := int16(binary.LittleEndian.Uint16(w[46:])); v != 16384 {
		t.Errorf("0.5 is %d", v)
	}
	if v := int16(binary.LittleEndian.Uint16(w[50:])); v != math.MaxInt16 {
		t.Errorf("2 clips to %d", v)
	}
}

func TestSpeakable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"*Done!* I booked it for `Tuesday`.", 0, "Done! I booked it for Tuesday."},
		{"Here:\n- one\n- two\n\n[POLL]\nQuestion: Which?\n- A\n- B\n[/POLL]\nThat's all.", 0, "Here: one two That's all."},
		{"First sentence. Second sentence goes on and on.", 30, "First sentence."},
		{"no end in sight at all here", 10, "no end in …"},
	} {
		if got := Speakable(tc.in, tc.max); got != tc.want {
			t.Errorf("Speakable(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
	if got := Speakable(strings.Repeat("ä", 50), 10); len([]rune(got)) != 11 {
		t.Errorf("runes cut: %q", got)
	}
}
