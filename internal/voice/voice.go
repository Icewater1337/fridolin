// Package voice turns a call's audio into what a person said, one
// utterance at a time, and back (docs/CALLS.md). It is pure: the bridge
// (internal/app) feeds it the call's frames and hands each utterance to a
// transcription, and plays speech the connections read.
//
// Audio here is what the WhatsApp call stack speaks: mono float32 PCM at
// SampleRate, in frames of 60 ms.
package voice

import (
	"bytes"
	"encoding/binary"
	"math"
)

// SampleRate is the call's sample rate, in Hz.
const SampleRate = 16000

// The defaults of a Segmenter.
const (
	// DefaultSilence is how long a pause ends an utterance.
	DefaultSilence = 900 // ms
	// DefaultMinSpeech is the shortest speech that counts: a cough or a
	// click is no utterance.
	DefaultMinSpeech = 300 // ms
	// DefaultMaxUtterance cuts an utterance that goes on and on.
	DefaultMaxUtterance = 30000 // ms
	// DefaultMinLevel is the quietest RMS level that is speech, whatever
	// the line's noise.
	DefaultMinLevel = 0.01
)

// Segmenter finds the utterances in a stream of frames with a simple
// energy detector: a frame is speech when its level is well above the
// line's noise (which it keeps learning from the quiet frames) and above
// a floor; an utterance starts with speech and ends after a pause.
type Segmenter struct {
	// Silence, MinSpeech and MaxUtterance are in milliseconds; MinLevel
	// is an RMS level (0 to 1). Zero takes the default.
	Silence, MinSpeech, MaxUtterance int
	MinLevel                         float64

	noise   float64
	buf     []float32
	speech  int // samples of speech in buf
	quiet   int // samples of quiet since the last speech
	talking bool
}

func ms(n, def int) int {
	if n <= 0 {
		n = def
	}
	return n * SampleRate / 1000
}

// Level is a frame's RMS level, 0 to 1.
func Level(frame []float32) float64 {
	if len(frame) == 0 {
		return 0
	}
	var sum float64
	for _, s := range frame {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(frame)))
}

// Feed takes the next frame and returns an utterance when one ended with
// it: the speech with what pause was left in it.
func (s *Segmenter) Feed(frame []float32) ([]float32, bool) {
	floor := s.MinLevel
	if floor <= 0 {
		floor = DefaultMinLevel
	}
	level := Level(frame)
	speech := level > floor && level > 3*s.noise
	if !speech {
		// The line's noise, learned slowly from what is not speech.
		if s.noise == 0 {
			s.noise = level
		} else {
			s.noise = 0.95*s.noise + 0.05*level
		}
	}
	switch {
	case speech:
		s.talking = true
		s.buf = append(s.buf, frame...)
		s.speech += len(frame)
		s.quiet = 0
	case s.talking:
		s.buf = append(s.buf, frame...)
		s.quiet += len(frame)
	default:
		return nil, false
	}
	if s.quiet >= ms(s.Silence, DefaultSilence) || len(s.buf) >= ms(s.MaxUtterance, DefaultMaxUtterance) {
		out, enough := s.buf, s.speech >= ms(s.MinSpeech, DefaultMinSpeech)
		s.Reset()
		if enough {
			return out, true
		}
	}
	return nil, false
}

// Reset drops what the segmenter holds, as when Fridolin starts to speak
// and does not listen meanwhile. It keeps what it learned of the noise.
func (s *Segmenter) Reset() {
	s.buf, s.speech, s.quiet, s.talking = nil, 0, 0, false
}

// WAV is pcm as a 16-bit mono WAV file at SampleRate, which every
// transcription takes.
func WAV(pcm []float32) []byte {
	data := PCM16(pcm)
	var b bytes.Buffer
	b.Grow(44 + len(data))
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(SampleRate), uint32(SampleRate * 2), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// PCM16 is pcm as signed 16-bit little-endian samples.
func PCM16(pcm []float32) []byte {
	out := make([]byte, 2*len(pcm))
	for i, s := range pcm {
		v := math.Max(-1, math.Min(1, float64(s)))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(math.Round(v*math.MaxInt16))))
	}
	return out
}

// Duration is how long pcm plays, in milliseconds.
func Duration(pcm []float32) int { return len(pcm) * 1000 / SampleRate }

// Speakable is text as a voice reads it well: without WhatsApp's
// markup (*bold*, _italic_, ~strike~, `code`), list bullets, links in
// angle brackets or [POLL] blocks, and cut to max characters at a
// sentence's end where it can.
func Speakable(text string, max int) string {
	var b bytes.Buffer
	inPoll := false
	for _, line := range bytes.Split([]byte(text), []byte("\n")) {
		l := bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(l, []byte("[POLL")):
			inPoll = true
			continue
		case bytes.HasPrefix(l, []byte("[/POLL]")):
			inPoll = false
			continue
		case inPoll, len(l) == 0:
			continue
		}
		l = bytes.TrimLeft(l, "-*•# ")
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.Write(l)
	}
	out := bytes.Map(func(r rune) rune {
		switch r {
		case '*', '_', '~', '`', '<', '>', '|':
			return -1
		}
		return r
	}, b.Bytes())
	s := string(bytes.Join(bytes.Fields(out), []byte(" ")))
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	cut := string(runes[:max])
	if i := lastSentenceEnd(cut); i >= max/3 {
		return cut[:i+1]
	}
	return cut + "…"
}

func lastSentenceEnd(s string) int {
	end := -1
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' {
			end = i
		}
	}
	return end
}
