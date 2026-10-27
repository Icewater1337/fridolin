package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func errStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 200
	}
	return StatusOf(err)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestConnectionsAndOps(t *testing.T) {
	t.Parallel()
	if got := IDs(); !slices.Equal(got, []string{OpenAI, ElevenLabs}) {
		t.Errorf("IDs = %v", got)
	}
	if !Known(OpenAI) || Known("anthropic") || Title(ElevenLabs) != "ElevenLabs" || Title("x") != "x" {
		t.Error("lookups")
	}
	if got := Ops(ElevenLabs); !slices.Equal(got, []string{OpTTS, OpTranscribe, OpVoices}) {
		t.Errorf("ElevenLabs ops = %v", got)
	}
	if (Settings{OpenAI: Policy{Voice: "a"}, ElevenLabs: Policy{Voice: "b"}}).Of("x").Voice != "" {
		t.Error("an unknown connection has a policy")
	}
	if StatusOf(errors.New("x")) != 500 || StatusOf(fail(429, "x")) != 429 || fail(418, "x").HTTPStatus() != 418 {
		t.Error("StatusOf")
	}
	if usd(20) != "20" || usd(7.5) != "7.5" {
		t.Error("usd")
	}
}

func TestKeys(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, true)
	if ts.Configured(OpenAI) {
		t.Fatal("a key before any was set")
	}
	for _, c := range []struct {
		id   string
		u    KeyUpdate
		want int
	}{
		{"anthropic", KeyUpdate{APIKey: testOpenAIKey}, 404},
		{OpenAI, KeyUpdate{}, 400},
		{OpenAI, KeyUpdate{APIKey: "short"}, 400},
		{OpenAI, KeyUpdate{APIKey: "sk-with space-0000"}, 400},
		{OpenAI, KeyUpdate{APIKey: "sk-ümlaut-00000000"}, 400},
		{OpenAI, KeyUpdate{APIKey: strings.Repeat("k", 513)}, 400},
		{ElevenLabs, KeyUpdate{APIKey: testElevenLabsKey, Organization: new("org-1")}, 400},
		{OpenAI, KeyUpdate{APIKey: testOpenAIKey, Project: new("proj 1")}, 400},
	} {
		if got := errStatus(t, ts.SetKey(c.id, c.u)); got != c.want {
			t.Errorf("SetKey(%s, %+v) = %d, want %d", c.id, c.u, got, c.want)
		}
	}
	if err := ts.SetKey(OpenAI, KeyUpdate{APIKey: "  " + testOpenAIKey + "\n", Organization: new("org-abc"), Project: new("proj_1")}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(ts.KeyPath(OpenAI))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Dir(ts.KeyPath(OpenAI))); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the folder: %v %v", st, err)
	}
	if f := readJSONFile(t, ts.KeyPath(OpenAI)); f["api_key"] != testOpenAIKey || f["organization"] != "org-abc" || f["project"] != "proj_1" {
		t.Errorf("openai.json = %v", f)
	}
	// A blank key keeps the stored one; only what is given changes.
	if err := ts.SetKey(OpenAI, KeyUpdate{Project: new("")}); err != nil {
		t.Fatal(err)
	}
	if f := readJSONFile(t, ts.KeyPath(OpenAI)); f["api_key"] != testOpenAIKey || f["organization"] != "org-abc" || f["project"] != "" {
		t.Errorf("after a blank save: %v", f)
	}
	if err := ts.SetKey(ElevenLabs, KeyUpdate{APIKey: testElevenLabsKey, Organization: new("")}); err != nil {
		t.Fatal(err)
	}
	if f := readJSONFile(t, ts.KeyPath(ElevenLabs)); len(f) != 1 || f["api_key"] != testElevenLabsKey {
		t.Errorf("elevenlabs.json = %v", f)
	}
	e, err := ts.Entry(OpenAI)
	if err != nil || !e.Configured || e.KeyHint != "…enai" || *e.Organization != "org-abc" || e.Voice != nil {
		t.Errorf("entry %+v %v", e, err)
	}
	data, _ := json.Marshal(e)
	assertNoKey(t, "the entry", data)
	if err := ts.SetKey(OpenAI, KeyUpdate{APIKey: "sk-12345678"}); err != nil {
		t.Fatal(err)
	}
	if e, _ := ts.Entry(OpenAI); e.KeyHint != "" {
		t.Errorf("a short key's hint: %q", e.KeyHint)
	}
	if _, err := ts.Entry("x"); errStatus(t, err) != 404 {
		t.Error("an unknown entry")
	}

	if err := ts.RemoveKey(OpenAI); err != nil || ts.Configured(OpenAI) {
		t.Fatalf("remove: %v", err)
	}
	if err := ts.RemoveKey(OpenAI); err != nil {
		t.Errorf("removing again: %v", err)
	}
	if errStatus(t, ts.RemoveKey("x")) != 404 {
		t.Error("removing an unknown connection")
	}
	// A file that does not parse, or has no key, is no key.
	if err := os.WriteFile(ts.KeyPath(OpenAI), []byte(`{"api_key": ""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ts.Configured(OpenAI) {
		t.Error("an empty key counts")
	}
}

func TestOpenAITranscribe(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	if err := ts.SetKey(OpenAI, KeyUpdate{Organization: new("org-abc"), Project: new("proj_1")}); err != nil {
		t.Fatal(err)
	}
	c := Caller{ID: "module:dictation"}
	audio := bytes.Repeat([]byte{1}, 1000)
	tr, err := ts.Transcribe(context.Background(), OpenAI, c, TranscribeRequest{Audio: Audio{Data: audio, Filename: "../x/Notiz (1).webm", ContentType: "audio/webm"}, Language: "de", Prompt: "Medizin"})
	if err != nil || tr.Text != "hallo welt" || tr.Language != "" {
		t.Fatalf("transcribe: %+v %v", tr, err)
	}
	got := ts.fake.last(t)
	if got.Path != "/v1/audio/transcriptions" || got.Header.Get("Authorization") != "Bearer "+testOpenAIKey ||
		got.Header.Get("OpenAI-Organization") != "org-abc" || got.Header.Get("OpenAI-Project") != "proj_1" || got.Header.Get("xi-api-key") != "" {
		t.Errorf("request: %s %v", got.Path, got.Header)
	}
	if got.Form["model"] != DefaultOpenAITranscribe || got.Form["language"] != "de" || got.Form["prompt"] != "Medizin" || got.Form["response_format"] != "json" {
		t.Errorf("form %v", got.Form)
	}
	if !bytes.Equal(got.File, audio) || got.Filename != "Notiz__1_.webm" || got.FileType != "audio/webm" {
		t.Errorf("file %q %q (%d bytes)", got.Filename, got.FileType, len(got.File))
	}
	// 90 s at the default model's $0.003 a minute.
	u := ts.usageOf(OpenAI, c.ID)
	if u.Requests != 1 || u.Errors != 0 || u.AudioSeconds != 90 || !near(u.USD, 1.5*0.003) {
		t.Errorf("usage %+v", u)
	}
	ev := ts.audited()
	if len(ev) != 1 || ev[0].Caller.ID != c.ID || ev[0].Connection != OpenAI || ev[0].Op != OpTranscribe || ev[0].Model != DefaultOpenAITranscribe || ev[0].Outcome != "ok" {
		t.Errorf("events %+v", ev)
	}

	// By tokens, when the answer gives no duration.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
		writeJSON(w, 200, map[string]any{"text": "x", "usage": map[string]any{"type": "tokens", "input_tokens": 1000, "output_tokens": 500,
			"input_token_details": map[string]any{"text_tokens": 100, "audio_tokens": 900}}})
		return true
	})
	c2 := Caller{ID: "brain:personal"}
	if _, err := ts.Transcribe(context.Background(), OpenAI, c2, TranscribeRequest{Audio: Audio{Data: audio}, Model: "gpt-4o-transcribe"}); err != nil {
		t.Fatal(err)
	}
	if got := ts.fake.last(t); got.Filename != "audio" || got.FileType != "application/octet-stream" {
		t.Errorf("a file without name or type: %q %q", got.Filename, got.FileType)
	}
	u = ts.usageOf(OpenAI, c2.ID)
	if u.InputTokens != 1000 || u.OutputTokens != 500 || !near(u.USD, (900*6+100*2.5+500*10)/1e6) {
		t.Errorf("token usage %+v", u)
	}
	// By the file's size when the answer says nothing: 2 MB, 2 minutes.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool { writeJSON(w, 200, map[string]any{"text": "x"}); return true })
	c3 := Caller{ID: "brain:size"}
	if _, err := ts.Transcribe(context.Background(), OpenAI, c3, TranscribeRequest{Audio: Audio{Data: make([]byte, 2_000_000), ContentType: "audio/ogg"}, Model: "whisper-1"}); err != nil {
		t.Fatal(err)
	}
	if got := ts.fake.last(t); got.Filename != "audio.ogg" {
		t.Errorf("an ogg's name: %q", got.Filename)
	}
	if u := ts.usageOf(OpenAI, c3.ID); u.AudioSeconds != 120 || !near(u.USD, 2*0.006) {
		t.Errorf("size usage %+v", u)
	}
	// verbose_json's duration counts too; an unknown model costs the most.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
		writeJSON(w, 200, map[string]any{"text": "x", "duration": 60})
		return true
	})
	c4 := Caller{ID: "brain:new"}
	if _, err := ts.Transcribe(context.Background(), OpenAI, c4, TranscribeRequest{Audio: Audio{Data: audio}, Model: "gpt-transcribe"}); err != nil {
		t.Fatal(err)
	}
	if u := ts.usageOf(OpenAI, c4.ID); !near(u.USD, 0.006) {
		t.Errorf("an unknown model %+v", u)
	}

	// What a caller sends wrong never reaches OpenAI.
	n := ts.fake.count()
	for _, req := range []TranscribeRequest{
		{},
		{Audio: Audio{Data: make([]byte, MaxAudio+1)}},
		{Audio: Audio{Data: audio}, Language: "deutsch!"},
		{Audio: Audio{Data: audio}, Prompt: strings.Repeat("p", maxPrompt+1)},
		{Audio: Audio{Data: audio}, Model: "no spaces please"},
	} {
		if _, err := ts.Transcribe(context.Background(), OpenAI, c, req); errStatus(t, err) != 400 {
			t.Errorf("%+v: %v", req.Model, err)
		}
	}
	if _, err := ts.Transcribe(context.Background(), ElevenLabs, c, TranscribeRequest{Audio: Audio{Data: audio}, Prompt: "x"}); errStatus(t, err) != 400 {
		t.Errorf("a prompt for ElevenLabs: %v", err)
	}
	if ts.fake.count() != n {
		t.Error("a refused request reached the provider")
	}
}

func TestElevenLabsTranscribeSpeakVoices(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	c := Caller{ID: "brain:personal"}
	tr, err := ts.Transcribe(context.Background(), ElevenLabs, c, TranscribeRequest{Audio: Audio{Data: []byte("ogg"), Filename: "voice.ogg", ContentType: "audio/ogg"}, Language: "de"})
	if err != nil || tr.Text != "guten tag" || tr.Language != "deu" {
		t.Fatalf("transcribe %+v %v", tr, err)
	}
	got := ts.fake.last(t)
	if got.Path != "/v1/speech-to-text" || got.Header.Get("xi-api-key") != testElevenLabsKey || got.Header.Get("Authorization") != "" ||
		got.Form["model_id"] != DefaultElevenLabsTranscribe || got.Form["language_code"] != "de" || got.Filename != "voice.ogg" {
		t.Errorf("request %s %v %v %q", got.Path, got.Header, got.Form, got.Filename)
	}
	// Two minutes by the last word's end.
	if u := ts.usageOf(ElevenLabs, c.ID); u.AudioSeconds != 120 || !near(u.USD, 2*0.40/60) {
		t.Errorf("usage %+v", u)
	}
	// Without words: by the size.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
		if s.Path == "/v1/speech-to-text" {
			writeJSON(w, 200, map[string]any{"text": "x"})
			return true
		}
		return false
	})
	c2 := Caller{ID: "brain:size"}
	if _, err := ts.Transcribe(context.Background(), ElevenLabs, c2, TranscribeRequest{Audio: Audio{Data: make([]byte, 1_000_000)}, Model: "scribe_v9"}); err != nil {
		t.Fatal(err)
	}
	if u := ts.usageOf(ElevenLabs, c2.ID); u.AudioSeconds != 60 || !near(u.USD, 0.40/60) {
		t.Errorf("size usage %+v", u)
	}

	// Speech: the default voice, unless one is named.
	if _, err := ts.Speak(context.Background(), c, SpeakRequest{Text: "Hallo"}); errStatus(t, err) != 400 || !strings.Contains(err.Error(), "name a voice") {
		t.Errorf("no voice: %v", err)
	}
	ts.setSettings(Settings{ElevenLabs: Policy{Voice: "defaultvoice"}})
	sp, err := ts.Speak(context.Background(), c, SpeakRequest{Text: "Hallo Welt", Language: "de", VoiceSettings: json.RawMessage(`{"stability": 0.5}`)})
	if err != nil || string(sp.Audio) != "ID3-fake-mp3" || sp.ContentType != "audio/mpeg" || sp.Format != DefaultSpeechFormat || sp.Characters != 42 {
		t.Fatalf("speak %+v %v", sp, err)
	}
	got = ts.fake.last(t)
	var body map[string]any
	_ = json.Unmarshal(got.Body, &body)
	if got.Path != "/v1/text-to-speech/defaultvoice" || got.Query != "output_format=mp3_44100_128" || body["text"] != "Hallo Welt" ||
		body["model_id"] != DefaultElevenLabsSpeech || body["language_code"] != "de" || body["voice_settings"].(map[string]any)["stability"] != 0.5 {
		t.Errorf("tts request %s?%s %v", got.Path, got.Query, body)
	}
	if _, err := ts.Speak(context.Background(), c, SpeakRequest{Text: "Hi", Voice: "other", Format: "pcm_16000", Model: "eleven_flash_v2_5"}); err != nil {
		t.Fatal(err)
	}
	if got := ts.fake.last(t); got.Path != "/v1/text-to-speech/other" || got.Query != "output_format=pcm_16000" {
		t.Errorf("named voice %s?%s", got.Path, got.Query)
	}
	// 84 characters billed at $0.30 a thousand.
	if u := ts.usageOf(ElevenLabs, c.ID); u.Characters != 84 || !near(u.USD, 2*0.40/60+84*0.30/1000) || u.Requests != 3 {
		t.Errorf("usage %+v", u)
	}
	// Without the header: the text's length; without a type: bytes.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
		w.Header()["Content-Type"] = nil
		_, _ = w.Write([]byte("raw"))
		return true
	})
	if sp, err := ts.Speak(context.Background(), c, SpeakRequest{Text: "Grüße"}); err != nil || sp.Characters != 5 || sp.ContentType != "application/octet-stream" {
		t.Errorf("without header: %+v %v", sp, err)
	}
	ts.fake.setAnswer(nil)
	for _, req := range []SpeakRequest{
		{Text: " "},
		{Text: strings.Repeat("ü", MaxSpeechText+1)},
		{Text: "x", Voice: "../voices"},
		{Text: "x", Format: "MP3"},
		{Text: "x", Language: "1"},
		{Text: "x", VoiceSettings: json.RawMessage(`[1]`)},
		{Text: "x", VoiceSettings: json.RawMessage(`{"a":"` + strings.Repeat("x", maxVoiceSettings) + `"}`)},
		{Text: "x", Model: "a b"},
	} {
		if _, err := ts.Speak(context.Background(), c, req); errStatus(t, err) != 400 {
			t.Errorf("%+v: %v", req.Voice, err)
		}
	}
	if _, err := ts.Speak(context.Background(), c, SpeakRequest{Text: "x", VoiceSettings: json.RawMessage(`null`)}); err != nil {
		t.Errorf("null voice settings: %v", err)
	}

	vs, err := ts.Voices(context.Background(), c)
	if err != nil || len(vs) != 2 || vs[0] != (Voice{ID: "v1", Name: "Rachel"}) {
		t.Fatalf("voices %+v %v", vs, err)
	}
	if got := ts.fake.last(t); got.Method != "GET" || got.Path != "/v1/voices" {
		t.Errorf("voices request %s %s", got.Method, got.Path)
	}
}

func TestResponses(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	c := Caller{ID: "module:chat"}
	body := []byte(`{"model": "gpt-4o-mini", "input": "Hallo", "metadata": {"a": "b"}}`)
	out, err := ts.Responses(context.Background(), c, body)
	if err != nil || !strings.Contains(string(out), `"output_text":"hi"`) {
		t.Fatalf("responses %s %v", out, err)
	}
	if got := ts.fake.last(t); !bytes.Equal(got.Body, body) || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("not passed through: %s %v", got.Body, got.Header)
	}
	// The snapshot gpt-4o-mini-2024-07-18 costs what gpt-4o-mini does.
	if u := ts.usageOf(OpenAI, c.ID); u.InputTokens != 1000 || u.OutputTokens != 2000 || !near(u.USD, (1000*0.15+2000*0.60)/1e6) {
		t.Errorf("usage %+v", u)
	}
	n := ts.fake.count()
	for _, b := range []string{`[1]`, `null`, `{"input": "x"}`, `{"model": ""}`, `{"model": "gpt-4o", "stream": true}`, `{"model": "gpt-4o", "background": true}`, `{"model": "a b"}`} {
		if _, err := ts.Responses(context.Background(), c, []byte(b)); errStatus(t, err) != 400 {
			t.Errorf("%s: %v", b, err)
		}
	}
	if _, err := ts.Responses(context.Background(), c, make([]byte, MaxResponsesBody+1)); errStatus(t, err) != 400 {
		t.Errorf("too large: %v", err)
	}
	if ts.fake.count() != n {
		t.Error("a refused request reached OpenAI")
	}
	if _, err := ts.Responses(context.Background(), c, []byte(`{"model": "gpt-4o", "stream": false}`)); err != nil {
		t.Errorf("stream false: %v", err)
	}
	// An answer that is no JSON is the provider's failure.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool { _, _ = w.Write([]byte("<html>")); return true })
	if _, err := ts.Responses(context.Background(), c, body); errStatus(t, err) != 502 {
		t.Errorf("html: %v", err)
	}
	if _, err := ts.Transcribe(context.Background(), OpenAI, c, TranscribeRequest{Audio: Audio{Data: []byte("a")}}); errStatus(t, err) != 502 {
		t.Errorf("html transcript: %v", err)
	}
	if _, err := ts.Transcribe(context.Background(), ElevenLabs, c, TranscribeRequest{Audio: Audio{Data: []byte("a")}}); errStatus(t, err) != 502 {
		t.Errorf("html stt: %v", err)
	}
	if _, err := ts.Voices(context.Background(), c); errStatus(t, err) != 502 {
		t.Errorf("html voices: %v", err)
	}
	if u := ts.usageOf(OpenAI, c.ID); u.Requests != 4 || u.Errors != 2 {
		t.Errorf("errors counted: %+v", u)
	}
}

func TestModelsAndBudget(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	audio := TranscribeRequest{Audio: Audio{Data: make([]byte, 1000)}}
	mod := ModuleCaller("dictation", Declaration{Use: []string{OpTranscribe}, Models: []string{"gpt-4o-transcribe"}, MonthlyUSD: 0.01})
	if mod.ID != "module:dictation" || mod.LimitUSD != 0.01 {
		t.Fatalf("caller %+v", mod)
	}
	// The default model is checked too.
	_, err := ts.Transcribe(context.Background(), OpenAI, mod, audio)
	if errStatus(t, err) != 403 || !strings.Contains(err.Error(), "may not use model gpt-4o-mini-transcribe with OpenAI") {
		t.Errorf("default model: %v", err)
	}
	audio.Model = "gpt-4o-transcribe"
	// 90 s at $0.006 a minute is $0.009: below the cent, so a second goes.
	for i := range 2 {
		if _, err := ts.Transcribe(context.Background(), OpenAI, mod, audio); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	_, err = ts.Transcribe(context.Background(), OpenAI, mod, audio)
	if errStatus(t, err) != 429 || err.Error() != "module:dictation is over its monthly budget of $0.01 for OpenAI" {
		t.Errorf("over budget: %v", err)
	}
	// Another connection has a budget of its own.
	if _, err := ts.Voices(context.Background(), mod); err != nil {
		t.Errorf("ElevenLabs: %v", err)
	}
	// The next month starts again.
	ts.clock.set(time.Date(2026, 11, 1, 0, 0, 1, 0, time.UTC))
	if _, err := ts.Transcribe(context.Background(), OpenAI, mod, audio); err != nil {
		t.Errorf("a new month: %v", err)
	}
	brain := BrainCaller("personal", "r1", 0)
	if brain.ID != "brain:personal" || brain.Run != "r1" || brain.Models != nil {
		t.Errorf("brain caller %+v", brain)
	}
	ev := ts.audited()
	if last := ev[len(ev)-1]; last.Outcome != "ok" || last.Model != "gpt-4o-transcribe" {
		t.Errorf("last event %+v", last)
	}
	if !slices.ContainsFunc(ev, func(e Event) bool { return strings.Contains(e.Outcome, "over its monthly budget") }) {
		t.Error("the refusal is not audited")
	}
}

func TestProviderErrorsAreScrubbed(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	c := Caller{ID: "brain:personal"}
	audio := TranscribeRequest{Audio: Audio{Data: []byte("a")}}
	cases := []struct {
		name        string
		status      int
		body        string
		conn        string
		wantMsg     string
		wantCode    string
		wantNoMatch string
	}{
		{"openai", 404, `{"error": {"message": "The model gpt-x does not exist", "type": "invalid_request_error", "code": "model_not_found"}}`, OpenAI, "OpenAI: The model gpt-x does not exist", "model_not_found", ""},
		{"openai type", 429, `{"error": {"message": "Slow down", "type": "rate_limit", "code": null}}`, OpenAI, "OpenAI: Slow down", "rate_limit", ""},
		{"openai echoes the key", 401, `{"error": {"message": "Incorrect API key provided: ` + testOpenAIKey + `.", "code": "invalid_api_key"}}`, OpenAI, "OpenAI: Incorrect API key provided: [redacted].", "invalid_api_key", testOpenAIKey},
		{"elevenlabs object", 401, `{"detail": {"status": "invalid_api_key", "message": "Invalid API key ` + testElevenLabsKey + `"}}`, ElevenLabs, "ElevenLabs: Invalid API key [redacted]", "invalid_api_key", testElevenLabsKey},
		{"elevenlabs code", 400, `{"detail": {"code": "voice_not_found", "message": "no voice"}}`, ElevenLabs, "ElevenLabs: no voice", "voice_not_found", ""},
		{"elevenlabs string", 404, `{"detail": "Not Found"}`, ElevenLabs, "ElevenLabs: Not Found", "", ""},
		{"elevenlabs list", 422, `{"detail": [{"loc": ["body"], "msg": "field required"}]}`, ElevenLabs, "ElevenLabs: field required", "", ""},
		{"no JSON", 500, `oops`, OpenAI, "OpenAI: it answered 500 Internal Server Error", "", ""},
		{"a strange code", 400, `{"error": {"message": "x", "code": "a b c"}}`, OpenAI, "OpenAI: x", "", ""},
		{"another key", 400, `{"error": {"message": "key sk-proj-abcdefghijklmnop and xi_abcdefghijkl and ` + strings.Repeat("Z", 40) + `"}}`, OpenAI, "OpenAI: key [redacted] and [redacted] and [redacted]", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
				return true
			})
			_, err := ts.Transcribe(context.Background(), tc.conn, c, audio)
			e, ok := errors.AsType[*Error](err)
			if !ok || e.Status != 502 || e.UpstreamStatus != tc.status || e.UpstreamCode != tc.wantCode || e.Message != tc.wantMsg {
				t.Errorf("got %+v", e)
			}
			assertNoKey(t, "the error", []byte(err.Error()))
		})
	}
	for _, e := range ts.audited() {
		assertNoKey(t, "the audit", []byte(e.Outcome))
	}
	if u := ts.usageOf(OpenAI, c.ID); u.Errors == 0 || u.Errors != u.Requests || u.USD != 0 {
		t.Errorf("failed calls: %+v", u)
	}
	long := scrub(strings.Repeat("ä ", 400), key{})
	if !strings.HasSuffix(long, "…") || len(long) > 504 {
		t.Errorf("long message: %d", len(long))
	}
}

func TestTimeoutsAndUnreachable(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	c := Caller{ID: "brain:personal"}
	release := make(chan struct{})
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool { <-release; return false })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := ts.Voices(ctx, c)
	cancel()
	close(release)
	if errStatus(t, err) != 504 || !strings.Contains(err.Error(), "did not answer within") {
		t.Errorf("timeout: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := ts.Voices(ctx, c); errStatus(t, err) != 502 || !strings.Contains(err.Error(), "was stopped") {
		t.Errorf("stopped: %v", err)
	}
	gone := New(Options{Dir: ts.o.Dir, Endpoints: Endpoints{OpenAI: "http://127.0.0.1:1", ElevenLabs: "http://127.0.0.1:1"}})
	if _, err := gone.Voices(context.Background(), c); errStatus(t, err) != 502 || !strings.Contains(err.Error(), "could not reach ElevenLabs") {
		t.Errorf("unreachable: %v", err)
	}
	if _, err := gone.Responses(context.Background(), c, []byte(`{"model": "gpt-4o"}`)); err == nil {
		t.Error("an unreachable OpenAI answered")
	}
	bad := New(Options{Dir: ts.o.Dir, Endpoints: Endpoints{OpenAI: "http://bad host"}})
	if _, err := bad.Responses(context.Background(), c, []byte(`{"model": "gpt-4o"}`)); errStatus(t, err) != 500 {
		t.Errorf("a bad address: %v", err)
	}
	// Too large an answer.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
		_, _ = w.Write(make([]byte, maxAnswer+1))
		return true
	})
	if _, err := ts.Voices(context.Background(), c); errStatus(t, err) != 502 || !strings.Contains(err.Error(), "more than") {
		t.Errorf("too large: %v", err)
	}
}

func TestNoKey(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, true)
	c := Caller{ID: "brain:personal"}
	_, err := ts.Transcribe(context.Background(), OpenAI, c, TranscribeRequest{Audio: Audio{Data: []byte("a")}})
	if errStatus(t, err) != 503 || err.Error() != "no OpenAI key: set one in the dashboard under Integrations" {
		t.Errorf("transcribe: %v", err)
	}
	if _, err := ts.Responses(context.Background(), c, []byte(`{}`)); errStatus(t, err) != 503 {
		t.Errorf("responses: %v", err)
	}
	if _, err := ts.Speak(context.Background(), c, SpeakRequest{Text: "x"}); errStatus(t, err) != 503 {
		t.Errorf("speak: %v", err)
	}
	if _, err := ts.Voices(context.Background(), c); errStatus(t, err) != 503 {
		t.Errorf("voices: %v", err)
	}
	if _, err := ts.Transcribe(context.Background(), "x", c, TranscribeRequest{}); errStatus(t, err) != 404 {
		t.Errorf("unknown connection: %v", err)
	}
	if _, err := ts.Check(context.Background(), OpenAI, time.Second); errStatus(t, err) != 503 {
		t.Errorf("check: %v", err)
	}
	if ts.fake.count() != 0 {
		t.Error("a call went out without a key")
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	if d, err := ts.Check(context.Background(), OpenAI, time.Second); err != nil || d != "the key works" {
		t.Errorf("works: %q %v", d, err)
	}
	if got := ts.fake.last(t); got.Path != "/v1/models" || got.Method != "GET" || got.Header.Get("Authorization") != "Bearer "+testOpenAIKey {
		t.Errorf("request %s %s", got.Method, got.Path)
	}
	status := 401
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool { w.WriteHeader(status); return true })
	if _, err := ts.Check(context.Background(), OpenAI, time.Second); err == nil || err.Error() != "OpenAI says the key is wrong (401)" {
		t.Errorf("401: %v", err)
	}
	status = 403
	if d, err := ts.Check(context.Background(), ElevenLabs, time.Second); err != nil || !strings.Contains(d, "403") {
		t.Errorf("ElevenLabs 403: %q %v", d, err)
	}
	if _, err := ts.Check(context.Background(), OpenAI, time.Second); errStatus(t, err) != 502 {
		t.Errorf("OpenAI 403: %v", err)
	}
	// The check counts as no one's usage.
	if _, rows := ts.MonthUsage(); len(rows) != 0 {
		t.Errorf("usage %+v", rows)
	}
}

func TestUsageLog(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	c := Caller{ID: "brain:personal"}
	for m := 1; m <= 15; m++ {
		ts.clock.set(time.Date(2025, time.Month(m), 3, 0, 0, 0, 0, time.UTC))
		if _, err := ts.Voices(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	st, err := os.Stat(ts.usagePath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("usage.json: %v %v", st, err)
	}
	months := readJSONFile(t, ts.usagePath())["months"].(map[string]any)
	if len(months) != keepMonths || months["2025-01"] != nil || months["2025-02"] != nil || months["2026-03"] == nil {
		t.Errorf("months kept: %v", months)
	}
	// Another service reads it back.
	again := New(Options{Dir: ts.o.Dir, Now: ts.clock.Now})
	if month, rows := again.MonthUsage(); month != "2026-03" || len(rows) != 1 || rows[0].Requests != 1 || rows[0].Caller != c.ID {
		t.Errorf("read back: %s %+v", month, rows)
	}
	// A log that does not parse starts over.
	if err := os.WriteFile(ts.usagePath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, rows := New(Options{Dir: ts.o.Dir}).MonthUsage(); len(rows) != 0 {
		t.Errorf("a broken log: %+v", rows)
	}
}

func TestView(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	ts.setSettings(Settings{OpenAI: Policy{Brains: []string{"personal"}, BrainMonthlyUSD: 10}, ElevenLabs: Policy{Voice: "v1"}})
	mods := []Module{
		{ID: "zeta", Connections: map[string]Declaration{ElevenLabs: {Use: []string{OpTTS}}}},
		{ID: "alpha", Connections: map[string]Declaration{ElevenLabs: {Use: []string{OpVoices}, MonthlyUSD: 5}, OpenAI: {Use: []string{OpTranscribe}, Models: []string{"whisper-1"}, MonthlyUSD: 20}}},
		{ID: "plain"},
	}
	if _, err := ts.Voices(context.Background(), ModuleCaller("alpha", mods[1].Connections[ElevenLabs])); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Transcribe(context.Background(), OpenAI, BrainCaller("personal", "r", 10), TranscribeRequest{Audio: Audio{Data: []byte("a")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Transcribe(context.Background(), OpenAI, ModuleCaller("alpha", mods[1].Connections[OpenAI]), TranscribeRequest{Audio: Audio{Data: []byte("a")}, Model: "whisper-1"}); err != nil {
		t.Fatal(err)
	}
	v := ts.View(mods)
	data, _ := json.Marshal(v)
	assertNoKey(t, "the view", data)
	if v.Month != "2026-10" || len(v.Connections) != 2 || v.Connections[0].ID != OpenAI || !slices.Equal(v.Connections[0].Brains, []string{"personal"}) || *v.Connections[1].Voice != "v1" {
		t.Errorf("view %s", data)
	}
	var mods2 []string
	for _, m := range v.Modules {
		mods2 = append(mods2, m.Module+"/"+m.Connection)
	}
	if strings.Join(mods2, ",") != "alpha/openai,alpha/elevenlabs,zeta/elevenlabs" {
		t.Errorf("modules %v", mods2)
	}
	if !strings.Contains(string(data), `"module":"zeta","connection":"elevenlabs","use":["tts"],"models":[],"monthly_usd":0`) {
		t.Errorf("an empty list is null: %s", data)
	}
	var rows []string
	for _, r := range v.Usage {
		rows = append(rows, r.Connection+" "+r.Caller+" "+usd(r.LimitUSD))
	}
	if strings.Join(rows, ",") != "openai brain:personal 10,openai module:alpha 20,elevenlabs module:alpha 5" {
		t.Errorf("usage rows %v", rows)
	}
	empty := newTestService(t, true).View(nil)
	data, _ = json.Marshal(empty)
	if !strings.Contains(string(data), `"modules":[],"usage":[]`) || !strings.Contains(string(data), `"brains":[]`) || !strings.Contains(string(data), `"organization":"","project":""`) {
		t.Errorf("empty view %s", data)
	}
}

func TestPrices(t *testing.T) {
	t.Parallel()
	for model, want := range map[string]tokenRate{
		"gpt-4o":                 {2.5, 10},
		"gpt-4o-mini-2024-07-18": {0.15, 0.60},
		"gpt-4o-2024-08-06":      {2.5, 10},
		"o3-2025":                {2, 8},
		"gpt-5-pro":              {2.5, 10},
		"gpt-4o-minimal":         {2.5, 10},
		"something":              {2.5, 10},
	} {
		if got := rateOf(openAIResponses, model, highestTokens); got != want {
			t.Errorf("%s: %+v, want %+v", model, got, want)
		}
	}
	if r := rateOf(openAITranscribe, "unknown", highestTranscribe); r != (transcribeRate{0.006, 6, 2.5, 10}) {
		t.Errorf("transcribe unknown %+v", r)
	}
	if u := speechUsage(1000); !near(u.USD, 0.30) || u.Characters != 1000 {
		t.Errorf("speech %+v", u)
	}
	// Token details that do not add up count everything as audio.
	u := transcriptionUsage("gpt-4o-mini-transcribe", 0, openAITranscript{Usage: &transcriptUsage{Type: "tokens", InputTokens: 100, OutputTokens: 10}})
	if !near(u.USD, (100*3+10*5)/1e6) {
		t.Errorf("tokens without details %+v", u)
	}
}

func TestAudioName(t *testing.T) {
	t.Parallel()
	for in, want := range map[[2]string]string{
		{"rec.wav", "audio/wav"}:                 "rec.wav",
		{`C:\x\y.m4a`, ""}:                       "y.m4a",
		{"..hidden.mp3", ""}:                     "hidden.mp3",
		{"", "audio/mpeg"}:                       "audio.mp3",
		{"noext", "audio/flac"}:                  "audio.flac",
		{"a\"b;c.ogg", ""}:                       "a_b_c.ogg",
		{strings.Repeat("x", 200) + ".webm", ""}: strings.Repeat("x", 95) + ".webm",
		{"Notiz ü.ogg", ""}:                      "Notiz__.ogg",
		{"", "application/x-something-unknown"}:  "audio",
		{"voice.opus", "audio/ogg"}:              "voice.ogg",
	} {
		if got := audioName(in[0], in[1]); got != want {
			t.Errorf("audioName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestAudioTypeAndSpeechExt(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{"/m/3EB0.ogg": "audio/ogg", "a.OPUS": "audio/ogg", "x.M4A": "audio/mp4", "noext": "", "a.txt": ""} {
		if got := AudioType(name); got != want {
			t.Errorf("AudioType(%q) = %q, want %q", name, got, want)
		}
	}
	for in, want := range map[[2]string]string{
		{"mp3_44100_128", "audio/mpeg"}: ".mp3", {"pcm_16000", ""}: ".pcm", {"opus_48000_64", ""}: ".opus",
		{"", "audio/mpeg"}: ".mp3", {"weird", "application/x-unknown"}: ".bin",
	} {
		if got := SpeechExt(in[0], in[1]); got != want {
			t.Errorf("SpeechExt(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// The default client refuses a redirect, so the ElevenLabs key never
// follows one to another host.
func TestDefaultClientRefusesRedirects(t *testing.T) {
	t.Parallel()
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
	}))
	defer other.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer provider.Close()
	s := New(Options{
		Dir:       filepath.Join(t.TempDir(), "connections"),
		Settings:  func() Settings { return Settings{ElevenLabs: Policy{Voice: "v"}} },
		Endpoints: Endpoints{OpenAI: provider.URL, ElevenLabs: provider.URL},
	})
	if err := s.SetKey(ElevenLabs, KeyUpdate{APIKey: testElevenLabsKey}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Speak(context.Background(), Caller{ID: "brain:personal"}, SpeakRequest{Text: "Hallo"})
	if errStatus(t, err) != http.StatusBadGateway || !strings.Contains(err.Error(), "redirect refused") || strings.Contains(err.Error(), testElevenLabsKey) {
		t.Errorf("speak through a redirect: %v", err)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect's host got %d requests", n)
	}
}
