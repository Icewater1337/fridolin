package connections

import (
	"maps"
	"regexp"
	"strings"
)

// The prices the usage log estimates costs with, in US dollars, as the
// providers listed them in October 2026. They are estimates: a provider's
// own bill, and OpenAI's project budget, are what count. A model not in a
// table is priced at the table's highest rate, so that a budget errs on
// the safe side.

// transcribeRate is a transcription model's price: per minute of audio,
// and per million tokens of audio in, text in (the prompt) and text out.
type transcribeRate struct {
	PerMinute, AudioIn, TextIn, Out float64
}

var openAITranscribe = map[string]transcribeRate{
	"whisper-1":                 {PerMinute: 0.006},
	"gpt-4o-transcribe":         {PerMinute: 0.006, AudioIn: 6, TextIn: 2.5, Out: 10},
	"gpt-4o-transcribe-diarize": {PerMinute: 0.006, AudioIn: 6, TextIn: 2.5, Out: 10},
	"gpt-4o-mini-transcribe":    {PerMinute: 0.003, AudioIn: 3, TextIn: 1.25, Out: 5},
}

// tokenRate is a text model's price per million tokens in and out.
type tokenRate struct{ In, Out float64 }

var openAIResponses = map[string]tokenRate{
	"gpt-5":        {In: 1.25, Out: 10},
	"gpt-5-mini":   {In: 0.25, Out: 2},
	"gpt-5-nano":   {In: 0.05, Out: 0.40},
	"gpt-4.1":      {In: 2, Out: 8},
	"gpt-4.1-mini": {In: 0.40, Out: 1.60},
	"gpt-4.1-nano": {In: 0.10, Out: 0.40},
	"gpt-4o":       {In: 2.50, Out: 10},
	"gpt-4o-mini":  {In: 0.15, Out: 0.60},
	"o3":           {In: 2, Out: 8},
	"o4-mini":      {In: 1.10, Out: 4.40},
}

// elevenLabsPer1KChars is ElevenLabs' text-to-speech price per thousand
// characters it bills (its character-cost header): what credits beyond a
// Creator plan's cost. Flash and Turbo models bill fewer.
const elevenLabsPer1KChars = 0.30

// elevenLabsSTTPerMinute is ElevenLabs' speech-to-text per minute.
var elevenLabsSTTPerMinute = map[string]float64{
	"scribe_v1":              0.40 / 60,
	"scribe_v1_experimental": 0.40 / 60,
}

// snapshotRE is the date a model's snapshot carries after its name, as
// in gpt-4o-2024-08-06: such a model costs what its name does. Anything
// else after a name known is another model, and unknown.
var snapshotRE = regexp.MustCompile(`^-(\d{4}-\d{2}-\d{2}|\d{4})$`)

// rateOf is model's entry in table: its own, its snapshot's name's, or
// the highest of the table's rates, field by field (highest).
func rateOf[T any](table map[string]T, model string, highest func([]T) T) T {
	if r, ok := table[model]; ok {
		return r
	}
	best := ""
	for name := range table {
		if rest, ok := strings.CutPrefix(model, name); ok && snapshotRE.MatchString(rest) && len(name) > len(best) {
			best = name
		}
	}
	if best != "" {
		return table[best]
	}
	return highest(mapValues(table))
}

func highestTranscribe(rs []transcribeRate) transcribeRate {
	var h transcribeRate
	for _, r := range rs {
		h = transcribeRate{max(h.PerMinute, r.PerMinute), max(h.AudioIn, r.AudioIn), max(h.TextIn, r.TextIn), max(h.Out, r.Out)}
	}
	return h
}

func highestTokens(rs []tokenRate) tokenRate {
	var h tokenRate
	for _, r := range rs {
		h = tokenRate{max(h.In, r.In), max(h.Out, r.Out)}
	}
	return h
}

func highestFloat(rs []float64) float64 {
	var h float64
	for _, r := range rs {
		h = max(h, r)
	}
	return h
}

// sizeSeconds estimates a recording's length from its size: about a
// minute per megabyte, as compressed speech goes.
func sizeSeconds(n int) float64 { return float64(n) / 1e6 * 60 }

// transcriptionUsage is what an OpenAI transcription cost: by the
// duration the answer gives, else by its tokens, else by the file's size.
func transcriptionUsage(model string, size int, a openAITranscript) Usage {
	r := rateOf(openAITranscribe, model, highestTranscribe)
	u := Usage{}
	seconds := a.Duration
	if a.Usage != nil && a.Usage.Type == "duration" {
		seconds = a.Usage.Seconds
	}
	switch {
	case seconds > 0:
		u.AudioSeconds = seconds
		u.USD = seconds / 60 * r.PerMinute
	case a.Usage != nil && (a.Usage.InputTokens > 0 || a.Usage.OutputTokens > 0):
		u.InputTokens, u.OutputTokens = a.Usage.InputTokens, a.Usage.OutputTokens
		audio, text := a.Usage.InputTokens, int64(0)
		if d := a.Usage.InputTokenDetails; d != nil && d.AudioTokens+d.TextTokens == a.Usage.InputTokens {
			audio, text = d.AudioTokens, d.TextTokens
		}
		u.USD = (float64(audio)*r.AudioIn + float64(text)*r.TextIn + float64(a.Usage.OutputTokens)*r.Out) / 1e6
		u.AudioSeconds = sizeSeconds(size)
	default:
		u.AudioSeconds = sizeSeconds(size)
		u.USD = u.AudioSeconds / 60 * r.PerMinute
	}
	return u
}

// responsesUsage is what an OpenAI response cost, by its tokens.
func responsesUsage(model string, in, out int64) Usage {
	r := rateOf(openAIResponses, model, highestTokens)
	return Usage{InputTokens: in, OutputTokens: out, USD: (float64(in)*r.In + float64(out)*r.Out) / 1e6}
}

// speechUsage is what ElevenLabs' text-to-speech cost.
func speechUsage(chars int64) Usage {
	return Usage{Characters: chars, USD: float64(chars) / 1000 * elevenLabsPer1KChars}
}

// sttUsage is what ElevenLabs' speech-to-text cost, for seconds of audio.
func sttUsage(model string, seconds float64) Usage {
	return Usage{AudioSeconds: seconds, USD: seconds / 60 * rateOf(elevenLabsSTTPerMinute, model, highestFloat)}
}

func mapValues[T any](m map[string]T) []T {
	var out []T
	for v := range maps.Values(m) {
		out = append(out, v)
	}
	return out
}
