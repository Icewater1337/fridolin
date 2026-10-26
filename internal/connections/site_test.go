package connections

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

// siteCall sends a request to the site mux as module, the way a module's
// server code does through the bridge.
func siteCall(t *testing.T, h http.Handler, method, path, module, ctype string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, bytes.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if module != "" {
		r.Header.Set(ModuleHeader, module)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// audioBody is a transcription's form with fields and, unless file is
// nil, the recording.
func audioBody(t *testing.T, file []byte, fields map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	if file != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="file"; filename="clip.webm"`)
		h.Set("Content-Type", "audio/webm;codecs=opus")
		p, _ := w.CreatePart(h)
		_, _ = p.Write(file)
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %d %s", w.Code, w.Body.String())
	}
	return out
}

func TestSiteMux(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	ts.setSettings(Settings{ElevenLabs: Policy{Voice: "v1"}})
	live := map[string]map[string]Declaration{
		"dictation": {OpenAI: {Use: []string{OpTranscribe, OpResponses}, Models: []string{"gpt-4o-mini-transcribe", "gpt-4o-mini"}, MonthlyUSD: 20}},
		"reader":    {ElevenLabs: {Use: []string{OpTTS, OpTranscribe, OpVoices}}},
		"nothing":   {},
	}
	h := ts.SiteMux(func(id string) (map[string]Declaration, error) {
		if d, ok := live[id]; ok {
			return d, nil
		}
		return nil, errors.New(`module "` + id + `" is not live`)
	})
	audio, ctype := audioBody(t, []byte("webm-bytes"), map[string]string{"language": "de", "prompt": "Termine"})
	noFile, noFileType := audioBody(t, nil, map[string]string{"model": "scribe_v1"})
	longField, longFieldType := audioBody(t, []byte("a"), map[string]string{"model": strings.Repeat("m", maxPrompt+1)})
	broken := []byte("--X\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nabc")

	w := siteCall(t, h, "POST", "/site-api/connections/openai/transcribe", "dictation", ctype, audio)
	if w.Code != 200 || w.Body.String() != "{\"text\":\"hallo welt\"}\n" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("transcribe: %d %s", w.Code, w.Body.String())
	}
	if got := ts.fake.last(t); string(got.File) != "webm-bytes" || got.FileType != "audio/webm" || got.Filename != "clip.webm" || got.Form["language"] != "de" || got.Form["prompt"] != "Termine" {
		t.Errorf("forwarded %+v", got.Form)
	}
	if u := ts.usageOf(OpenAI, "module:dictation"); u.Requests != 1 {
		t.Errorf("usage %+v", u)
	}

	for _, c := range []struct {
		name, method, path, module, ctype string
		body                              []byte
		want                              int
		msg                               string
	}{
		{"no module", "POST", "/site-api/connections/openai/transcribe", "", ctype, audio, 401, "X-Fridolin-Module"},
		{"a strange module", "POST", "/site-api/connections/openai/transcribe", "../x", ctype, audio, 404, "is live"},
		{"no such module", "POST", "/site-api/connections/openai/transcribe", "ghost", ctype, audio, 404, `module "ghost" is not live`},
		{"not declared", "POST", "/site-api/connections/openai/transcribe", "nothing", ctype, audio, 403, "does not declare transcribe in capabilities.openai.use"},
		{"another connection", "POST", "/site-api/connections/openai/transcribe", "reader", ctype, audio, 403, "capabilities.openai.use"},
		{"another op", "POST", "/site-api/connections/openai/responses", "reader", "application/json", []byte(`{}`), 403, ""},
		{"a model not listed", "POST", "/site-api/connections/openai/responses", "dictation", "application/json", []byte(`{"model": "gpt-5"}`), 403, "may not use model gpt-5"},
		{"stream", "POST", "/site-api/connections/openai/responses", "dictation", "application/json", []byte(`{"model": "gpt-4o-mini", "stream": true}`), 400, "stream"},
		{"no form", "POST", "/site-api/connections/openai/transcribe", "dictation", "application/json", []byte(`{}`), 400, "multipart"},
		{"no file", "POST", "/site-api/connections/elevenlabs/transcribe", "reader", noFileType, noFile, 400, "no audio"},
		{"a broken form", "POST", "/site-api/connections/elevenlabs/transcribe", "reader", "multipart/form-data; boundary=X", broken, 400, "reading the form"},
		{"a long field", "POST", "/site-api/connections/elevenlabs/transcribe", "reader", longFieldType, longField, 400, "model is more than"},
		{"tts without JSON", "POST", "/site-api/connections/elevenlabs/tts", "reader", "application/json", []byte(`text`), 400, "as JSON"},
		{"tts too long", "POST", "/site-api/connections/elevenlabs/tts", "reader", "application/json", []byte(`{"text": "` + strings.Repeat("a", MaxSpeechText+1) + `"}`), 400, "more than 10000"},
	} {
		w := siteCall(t, h, c.method, c.path, c.module, c.ctype, c.body)
		if w.Code != c.want || !strings.Contains(decode(t, w)["error"].(string), c.msg) {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body.String())
		}
	}

	w = siteCall(t, h, "POST", "/site-api/connections/openai/responses", "dictation", "application/json", []byte(`{"model": "gpt-4o-mini", "input": "hi"}`))
	if w.Code != 200 || decode(t, w)["output_text"] != "hi" {
		t.Errorf("responses: %d %s", w.Code, w.Body.String())
	}
	w = siteCall(t, h, "POST", "/site-api/connections/elevenlabs/tts", "reader", "application/json", []byte(`{"text": "Hallo", "format": "mp3_22050_32", "voice_settings": {"speed": 1.1}}`))
	if w.Code != 200 || w.Body.String() != "ID3-fake-mp3" || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("tts: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if got := ts.fake.last(t); got.Path != "/v1/text-to-speech/v1" || got.Query != "output_format=mp3_22050_32" {
		t.Errorf("tts request %s?%s", got.Path, got.Query)
	}
	stt, sttType := audioBody(t, []byte("ogg"), map[string]string{"model": "scribe_v1", "language": "de"})
	w = siteCall(t, h, "POST", "/site-api/connections/elevenlabs/transcribe", "reader", sttType, stt)
	if out := decode(t, w); w.Code != 200 || out["text"] != "guten tag" || out["language"] != "deu" {
		t.Errorf("stt: %d %s", w.Code, w.Body.String())
	}
	w = siteCall(t, h, "GET", "/site-api/connections/elevenlabs/voices", "reader", "", nil)
	if w.Code != 200 || w.Body.String() != `{"voices":[{"id":"v1","name":"Rachel"},{"id":"v2","name":"Adam"}]}`+"\n" {
		t.Errorf("voices: %d %s", w.Code, w.Body.String())
	}

	// A provider's error: 502 with its status and code.
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error": {"message": "The model does not exist", "code": "model_not_found"}}`))
		return true
	})
	w = siteCall(t, h, "POST", "/site-api/connections/openai/responses", "dictation", "application/json", []byte(`{"model": "gpt-4o-mini"}`))
	if out := decode(t, w); w.Code != 502 || out["error"] != "OpenAI: The model does not exist" || out["upstream_status"] != 404.0 || out["upstream_code"] != "model_not_found" {
		t.Errorf("provider error: %d %s", w.Code, w.Body.String())
	}
	ts.fake.setAnswer(func(w http.ResponseWriter, s seen) bool { w.WriteHeader(500); return true })
	for _, p := range []struct{ path, ctype string }{{"/site-api/connections/elevenlabs/tts", "application/json"}, {"/site-api/connections/elevenlabs/transcribe", sttType}, {"/site-api/connections/elevenlabs/voices", ""}, {"/site-api/connections/openai/transcribe", ctype}} {
		body := []byte(`{"text": "x"}`)
		switch p.ctype {
		case sttType:
			body = stt
		case ctype:
			body = audio
		}
		method := "POST"
		if p.ctype == "" {
			method, body = "GET", nil
		}
		module := "reader"
		if strings.Contains(p.path, "openai") {
			module = "dictation"
		}
		if w := siteCall(t, h, method, p.path, module, p.ctype, body); w.Code != 502 {
			t.Errorf("%s: %d %s", p.path, w.Code, w.Body.String())
		} else if _, ok := decode(t, w)["upstream_code"]; ok {
			t.Errorf("%s: a code that was not given: %s", p.path, w.Body.String())
		}
	}

	// Every call, refused or not, is audited by module, never its content.
	ev := ts.audited()
	var outcomes []string
	for _, e := range ev {
		if !strings.HasPrefix(e.Caller.ID, "module:") {
			t.Errorf("caller %q", e.Caller.ID)
		}
		outcomes = append(outcomes, e.Outcome)
		for _, secret := range []string{"Termine", "webm-bytes", "Hallo"} {
			if strings.Contains(e.Outcome, secret) || strings.Contains(e.Model, secret) {
				t.Errorf("the audit holds content: %+v", e)
			}
		}
	}
	if !strings.Contains(strings.Join(outcomes, "|"), `module "ghost" is not live`) || !strings.Contains(strings.Join(outcomes, "|"), "does not declare") {
		t.Errorf("refusals not audited: %v", outcomes)
	}
	// Errors that are not the service's own are 500s.
	rec := httptest.NewRecorder()
	WriteError(rec, errors.New("boom"))
	if rec.Code != 500 || decode(t, rec)["error"] != "boom" {
		t.Errorf("plain error: %d %s", rec.Code, rec.Body.String())
	}
}

// The audio limit holds in the form: a recording over 25 MiB is refused
// before anything is sent.
func TestSiteAudioLimit(t *testing.T) {
	t.Parallel()
	ts := newTestService(t, false)
	h := ts.SiteMux(func(string) (map[string]Declaration, error) {
		return map[string]Declaration{OpenAI: {Use: []string{OpTranscribe}}}, nil
	})
	body, ctype := audioBody(t, make([]byte, MaxAudio+1), nil)
	w := siteCall(t, h, "POST", "/site-api/connections/openai/transcribe", "m", ctype, body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "more than 25 MiB") || ts.fake.count() != 0 {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if SiteAudioBody <= MaxAudio || SiteSpeechBody != 1<<20 || SiteResponsesBody != MaxResponsesBody {
		t.Error("body limits")
	}
}
