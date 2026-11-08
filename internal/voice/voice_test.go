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
	// Noise above the floor, but steady, longer than MaxUtterance: it is
	// learned as the line's.
	s := &Segmenter{}
	if got := feed(s, repeat(0.02, 600)...); len(got) != 0 {
		t.Errorf("steady noise made utterances %v", got)
	}
	s.Reset() // keeps the noise
	if got := feed(s, repeat(0.02, 600)...); len(got) != 0 {
		t.Errorf("steady noise after a Reset made utterances %v", got)
	}
}

// noiseOnset is at most one utterance, no longer than the noise window.
func noiseOnset(t *testing.T, got []int) {
	t.Helper()
	if len(got) > 1 || len(got) == 1 && got[0] > noiseWindow*60 {
		t.Errorf("noise made utterances %v ms, want at most one of %d", got, noiseWindow*60)
	}
}

func TestSegmenterLearnsNoiseThatStartsLater(t *testing.T) {
	t.Parallel()
	s := &Segmenter{}
	noiseOnset(t, feed(s, append(repeat(0.001, 50), repeat(0.02, 600)...)...))
}

func TestSegmenterLearnsANoiseJump(t *testing.T) {
	t.Parallel()
	s := &Segmenter{}
	noiseOnset(t, feed(s, append(repeat(0.02, 100), repeat(0.2, 600)...)...))
}

func TestSegmenterHearsSpeechOverSteadyNoise(t *testing.T) {
	t.Parallel()
	s := &Segmenter{}
	levels := repeat(0.02, 100)
	for range 3 {
		levels = append(levels, repeat(0.2, 20)...)  // 1.2 s of speech
		levels = append(levels, repeat(0.02, 25)...) // a 1.5 s pause
	}
	got := feed(s, levels...)
	if len(got) != 3 {
		t.Fatalf("utterances %v ms, want 3", got)
	}
	for _, ms := range got {
		// Speech and the Silence that ended it, to the frame.
		if ms < 2100 || ms > 2160 {
			t.Errorf("utterances %v ms, want each about 1200+900", got)
		}
	}
}

// talk is n frames of speech that dips between syllables, as speech does.
func talk(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 0.2
		if i%10 >= 8 {
			out[i] = 0.01
		}
	}
	return out
}

func TestSegmenterKeepsLongSpeech(t *testing.T) {
	t.Parallel()
	s := &Segmenter{}
	levels := append(repeat(0.001, 10), talk(200)...) // 12 s, past the window
	got := feed(s, append(levels, repeat(0.001, 20)...)...)
	if len(got) != 1 || got[0] < 12000 || got[0] > 13000 {
		t.Errorf("utterances %v ms, want one of about 12000+900", got)
	}
}

func TestSegmenterKeepsSpeechThatRunsIntoNoise(t *testing.T) {
	t.Parallel()
	// The noise starts as the speaker stops: the window is all noise, but
	// the buffer holds speech from before it, which must not go with it.
	s := &Segmenter{}
	levels := append(repeat(0.001, 10), talk(80)...) // 4.8 s
	got := feed(s, append(levels, repeat(0.05, 600)...)...)
	if want := 4800 + noiseWindow*60 + 900; len(got) != 1 || got[0] < 4800 || got[0] > want {
		t.Errorf("utterances %v ms, want one of at most %d", got, want)
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
