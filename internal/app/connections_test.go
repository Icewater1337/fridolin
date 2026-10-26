package app

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// Fake keys, never real ones.
const (
	testOpenAIKey     = "sk-test-0000000000000000000000000000app"
	testElevenLabsKey = "sk_test_0000000000000000000000000000app"
	// goldenOpenAIKey is the OpenAI key of goldenBridge.
	goldenOpenAIKey = "sk-test-golden-00000000000000000000a1b2"
)

// fakeAI is OpenAI and ElevenLabs on one test server: every call works,
// but GET /v1/models answers models.
type fakeAI struct {
	*httptest.Server
	mu     sync.Mutex
	calls  []string // "METHOD path"
	auth   []string // the key each call carried
	files  [][]byte // the recordings sent
	models int
}

// useFakeAI points the connections of the bridges t makes at a fakeAI.
func useFakeAI(t *testing.T) *fakeAI {
	t.Helper()
	f := &fakeAI{models: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	optionsOf(t).hooks.connections = &connections.Endpoints{OpenAI: f.URL, ElevenLabs: f.URL}
	return f
}

func (f *fakeAI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization")+r.Header.Get("xi-api-key"))
	if file, _, err := r.FormFile("file"); err == nil {
		data, _ := io.ReadAll(file)
		f.files = append(f.files, data)
	}
	models := f.models
	f.mu.Unlock()
	reply := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch {
	case r.URL.Path == "/v1/models":
		w.WriteHeader(models)
		reply(map[string]any{"data": []any{}})
	case r.URL.Path == "/v1/audio/transcriptions":
		reply(map[string]any{"text": "transcribed by openai", "usage": map[string]any{"type": "duration", "seconds": 60}})
	case r.URL.Path == "/v1/responses":
		reply(map[string]any{"model": "gpt-4o-mini", "output_text": "hi", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
	case r.URL.Path == "/v1/speech-to-text":
		reply(map[string]any{"text": "transcribed by elevenlabs", "language_code": "deu"})
	case r.URL.Path == "/v1/voices":
		reply(map[string]any{"voices": []map[string]any{{"voice_id": "v1", "name": "Rachel"}}})
	case strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/"):
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("ID3fake"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAI) seen() ([]string, []string, [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]string(nil), f.auth...), append([][]byte(nil), f.files...)
}

func (f *fakeAI) setModels(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = status
}

// setConnectionKeys saves both keys through the dashboard.
func (h *harness) setConnectionKeys() {
	h.t.Helper()
	for id, key := range map[string]string{"openai": testOpenAIKey, "elevenlabs": testElevenLabsKey} {
		if w := h.Do("PUT", "/api/connections/"+id, map[string]string{"api_key": key}); w.Code != http.StatusOK {
			h.t.Fatalf("setting the %s key: %d %s", id, w.Code, w.Body.String())
		}
	}
}

// connectionsView is GET /api/connections.
func (h *harness) connectionsView() connections.View {
	h.t.Helper()
	w := h.Do("GET", "/api/connections", nil)
	var v connections.View
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		h.t.Fatalf("GET /api/connections: %d %s", w.Code, w.Body.String())
	}
	return v
}

// noKeys fails when a key shows in data.
func noKeys(t *testing.T, what string, data []byte) {
	t.Helper()
	for _, k := range []string{testOpenAIKey, testElevenLabsKey, goldenOpenAIKey} {
		if bytes.Contains(data, []byte(k)) {
			t.Errorf("%s shows a key: %s", what, data)
		}
	}
}

// siteConnectionCall calls the site bridge at url as a module's server
// code does: with token, module in X-Fridolin-Module and body of type ct.
func siteConnectionCall(t *testing.T, method, url, token, module, path, ct string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if module != "" {
		req.Header.Set(connections.ModuleHeader, module)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

// TestConnectionsDashboard: the Integrations page sets the keys, which
// never come back, the brains that may use each connection, which go into
// config.json, and checks a key with the provider.
func TestConnectionsDashboard(t *testing.T) {
	t.Parallel()
	fake := useFakeAI(t)
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "personal", Name: "Personal", Routing: manifest.Routing{HandlesDMs: true}})

	v := h.connectionsView()
	if len(v.Connections) != 2 || v.Connections[0].Configured || v.Connections[1].Configured || len(v.Modules) != 0 || len(v.Usage) != 0 {
		t.Fatalf("nothing set: %+v", v)
	}
	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"PUT", "/api/connections/openai", map[string]any{}, 400},
		{"PUT", "/api/connections/openai", map[string]any{"api_key": "short"}, 400},
		{"PUT", "/api/connections/openai", map[string]any{"api_key": testOpenAIKey, "secret": "x"}, 400},
		{"PUT", "/api/connections/nope", map[string]any{"api_key": testOpenAIKey}, 404},
		{"PUT", "/api/connections/nope/brains", map[string]any{"brains": []string{}}, 404},
		{"PUT", "/api/connections/openai/brains", map[string]any{"brains": []string{"ghost"}}, 400},
		{"PUT", "/api/connections/openai/brains", map[string]any{"voice": "v1"}, 400},
		{"PUT", "/api/connections/openai/brains", map[string]any{"brain_monthly_usd": -1}, 400},
		{"PUT", "/api/connections/elevenlabs/brains", map[string]any{"voice": "../x"}, 400},
		{"DELETE", "/api/connections/nope", nil, 404},
		{"POST", "/api/connections/nope/check", nil, 404},
	} {
		if w := h.Do(c.method, c.path, c.body); w.Code != c.want {
			t.Errorf("%s %s %v: %d %s, want %d", c.method, c.path, c.body, w.Code, w.Body.String(), c.want)
		}
	}

	w := h.Do("PUT", "/api/connections/openai", map[string]any{"api_key": testOpenAIKey, "organization": "org-test"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"configured":true`) || !strings.Contains(w.Body.String(), `"key_hint":"…0app"`) || !strings.Contains(w.Body.String(), `"organization":"org-test"`) {
		t.Fatalf("saving the key: %d %s", w.Code, w.Body.String())
	}
	noKeys(t, "the answer", w.Body.Bytes())
	keyFile := filepath.Join(h.DataDir(), "connections", "openai.json")
	if st, err := os.Stat(keyFile); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v %v", st, err)
	}
	// A blank key keeps it.
	if w := h.Do("PUT", "/api/connections/openai", map[string]any{"api_key": "", "project": "proj_1"}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"key_hint":"…0app"`) {
		t.Errorf("a blank key: %d %s", w.Code, w.Body.String())
	}

	w = h.Do("PUT", "/api/connections/openai/brains", map[string]any{"brains": []string{"personal", "personal"}, "brain_monthly_usd": 10})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"brains":["personal"],"brain_monthly_usd":10`) {
		t.Fatalf("the brains: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("PUT", "/api/connections/elevenlabs/brains", map[string]any{"voice": "v1"}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"voice":"v1"`) {
		t.Errorf("the voice: %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(h.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Connections map[string]map[string]any `json:"connections"`
	}
	if err := json.Unmarshal(data, &file); err != nil || file.Connections["openai"]["brain_monthly_usd"] != 10.0 || file.Connections["elevenlabs"]["voice"] != "v1" {
		t.Errorf("config.json: %s", data)
	}
	noKeys(t, "config.json", data)

	if w := h.Do("POST", "/api/connections/openai/check", nil); w.Code != http.StatusOK || w.Body.String() != "{\"ok\":true,\"detail\":\"the key works\"}\n" {
		t.Errorf("check: %d %s", w.Code, w.Body.String())
	}
	fake.setModels(http.StatusUnauthorized)
	if w := h.Do("POST", "/api/connections/openai/check", nil); w.Code != http.StatusOK || w.Body.String() != "{\"ok\":false,\"error\":\"OpenAI says the key is wrong (401)\"}\n" {
		t.Errorf("a wrong key: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("POST", "/api/connections/elevenlabs/check", nil); !strings.Contains(w.Body.String(), "no ElevenLabs key: set one in the dashboard") {
		t.Errorf("no key: %d %s", w.Code, w.Body.String())
	}
	_, auth, _ := fake.seen()
	if len(auth) != 2 || auth[0] != "Bearer "+testOpenAIKey {
		t.Errorf("the checks went out with %v", auth)
	}

	if w := h.Do("DELETE", "/api/connections/openai", nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(keyFile); !os.IsNotExist(err) {
		t.Errorf("the key file stays: %v", err)
	}
	v = h.connectionsView()
	if v.Connections[0].Configured || len(v.Connections[0].Brains) != 1 {
		t.Errorf("after removing: %+v", v.Connections[0])
	}
	var lines []string
	for _, l := range h.auditOf("connection_key") {
		lines = append(lines, l["detail"].(string))
		raw, _ := json.Marshal(l)
		noKeys(t, "the audit", raw)
	}
	if strings.Join(lines, "|") != "openai: key set; organization saved|openai: project saved|openai: key removed" {
		t.Errorf("audit %v", lines)
	}
	noKeys(t, "GET /api/connections", h.Do("GET", "/api/connections", nil).Body.Bytes())
}

// liveSite deploys modules, by id their capabilities, as the live commit
// of an enabled site, and restarts the bridge, which reads it.
func (h *harness) liveSite(modules map[string]string) {
	h.t.Helper()
	commit := strings.Repeat("a", 40)
	site := filepath.Join(h.DataDir(), "site")
	for id, caps := range modules {
		dir := filepath.Join(site, "deploys", commit[:12], "modules", id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			h.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "module.json"), []byte(`{"version": 1, "id": "`+id+`", "capabilities": `+caps+`}`), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(site, "settings.json"), []byte(`{"enabled": true, "approved": "`+commit+`"}`), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.Restart()
}

// TestConnectionsFromSiteModules: a module's server code calls OpenAI and
// ElevenLabs through the site bridge, as its live manifest declares; the
// dashboard lists what the modules declare and what they used.
func TestConnectionsFromSiteModules(t *testing.T) {
	t.Parallel()
	fake := useFakeAI(t)
	h := newHarness(t, "")
	h.setConnectionKeys()
	h.liveSite(map[string]string{
		"dictation": `{"openai": {"use": ["transcribe", "responses"], "monthly_usd": 20}}`,
		"reader":    `{"elevenlabs": {"use": ["tts", "voices"], "models": ["eleven_multilingual_v2"]}}`,
		"plain":     `{}`,
	})
	url, token, err := h.b.filesBridge()
	if err != nil {
		t.Fatal(err)
	}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, _ := mw.CreateFormFile("file", "note.webm")
	_, _ = part.Write([]byte("webm-audio"))
	_ = mw.WriteField("language", "de")
	_ = mw.Close()

	code, body := siteConnectionCall(t, "POST", url, token, "dictation", "/site-api/connections/openai/transcribe", mw.FormDataContentType(), form.Bytes())
	if code != http.StatusOK || body != "{\"text\":\"transcribed by openai\"}\n" {
		t.Fatalf("transcribe: %d %s", code, body)
	}
	if code, body := siteConnectionCall(t, "POST", url, token, "dictation", "/site-api/connections/openai/responses", "application/json", []byte(`{"model": "gpt-4o-mini", "input": "hi"}`)); code != http.StatusOK || !strings.Contains(body, `"output_text":"hi"`) {
		t.Errorf("responses: %d %s", code, body)
	}
	if code, body := siteConnectionCall(t, "POST", url, token, "reader", "/site-api/connections/elevenlabs/tts", "application/json", []byte(`{"text": "Hallo", "voice": "v1"}`)); code != http.StatusOK || body != "ID3fake" {
		t.Errorf("tts: %d %s", code, body)
	}
	for _, c := range []struct {
		module, path, ct string
		body             []byte
		want             int
	}{
		{"plain", "/site-api/connections/openai/transcribe", mw.FormDataContentType(), form.Bytes(), http.StatusForbidden},
		{"reader", "/site-api/connections/elevenlabs/transcribe", mw.FormDataContentType(), form.Bytes(), http.StatusForbidden},
		{"reader", "/site-api/connections/elevenlabs/tts", "application/json", []byte(`{"text": "Hallo", "voice": "v1", "model": "eleven_v3"}`), http.StatusForbidden},
		{"ghost", "/site-api/connections/openai/responses", "application/json", []byte(`{"model": "gpt-4o"}`), http.StatusNotFound},
		{"dictation", "/site-api/connections/openai/responses", "application/json", []byte(`{"model": "gpt-4o", "stream": true}`), http.StatusBadRequest},
		{"dictation", "/site-api/connections/openai/nope", "application/json", []byte(`{}`), http.StatusMethodNotAllowed},
	} {
		if code, body := siteConnectionCall(t, "POST", url, token, c.module, c.path, c.ct, c.body); code != c.want {
			t.Errorf("%s %s: %d %s, want %d", c.module, c.path, code, body, c.want)
		}
	}
	calls, auth, files := fake.seen()
	if strings.Join(calls, ",") != "POST /v1/audio/transcriptions,POST /v1/responses,POST /v1/text-to-speech/v1" || auth[2] != testElevenLabsKey || string(files[0]) != "webm-audio" {
		t.Errorf("the providers got %v %v", calls, auth)
	}

	v := h.connectionsView()
	var mods, usage []string
	for _, m := range v.Modules {
		mods = append(mods, m.Module+"/"+m.Connection+"/"+strings.Join(m.Use, "+"))
	}
	for _, u := range v.Usage {
		usage = append(usage, u.Connection+" "+u.Caller)
		if u.Caller == "module:dictation" && (u.Requests != 2 || u.LimitUSD != 20 || u.AudioSeconds != 60) {
			t.Errorf("dictation's usage %+v", u)
		}
	}
	if strings.Join(mods, ",") != "dictation/openai/transcribe+responses,reader/elevenlabs/tts+voices" {
		t.Errorf("modules %v", mods)
	}
	if strings.Join(usage, ",") != "openai module:dictation,elevenlabs module:reader" {
		t.Errorf("usage %v", usage)
	}
	var calls2 []string
	for _, l := range h.auditOf("connection_call") {
		calls2 = append(calls2, l["by"].(string)+" "+l["detail"].(string)+" "+l["outcome"].(string))
	}
	want := []string{
		"module:dictation openai transcribe gpt-4o-mini-transcribe ok",
		"module:dictation openai responses gpt-4o-mini ok",
		"module:reader elevenlabs tts eleven_multilingual_v2 ok",
	}
	if len(calls2) < 3 || strings.Join(calls2[:3], "|") != strings.Join(want, "|") {
		t.Errorf("audit %v", calls2)
	}
	if !strings.Contains(strings.Join(calls2, "|"), "module:plain openai transcribe module plain does not declare transcribe in capabilities.openai.use") {
		t.Errorf("the refusal is not audited: %v", calls2)
	}
}

// TestConnectionsBrainTools: a brain's runs have the connections' tools
// only while the connection has a key and connections.<id>.brains lists
// the brain; the bridge reads the recording from the run's folder and
// writes speech into its temp folder.
func TestConnectionsBrainTools(t *testing.T) {
	t.Parallel()
	fake := useFakeAI(t)
	h := newHarness(t, `{"connections": {"openai": {"brains": ["personal"], "brain_monthly_usd": 0.001}, "elevenlabs": {"brains": ["personal"], "voice": "v1"}}}`)
	dir := h.AddBrain(manifest.Manifest{ID: "personal", Name: "Personal", Routing: manifest.Routing{HandlesDMs: true}})
	h.AddBrain(manifest.Manifest{ID: "work", Name: "Work"})
	tok, revoke := h.b.runTokens.mintRun(runInfo{Brain: "personal", ID: "r1", Chat: harnessDM})
	defer revoke()
	workTok, revokeWork := h.b.runTokens.mintRun(runInfo{Brain: "work", ID: "r2", Chat: harnessDM})
	defer revokeWork()
	call := func(token, run, method, path, body string) string {
		w := h.Script(method, path, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Fridolin-Run", run)
		})
		return w.Body.String()
	}
	tools := func(token, run string) string { return call(token, run, "GET", "/brain/mcp/tools", "") }
	if got := tools(tok, "r1"); strings.Contains(got, "openai_transcribe") || strings.Contains(got, "elevenlabs_speak") {
		t.Errorf("tools without keys: %s", got)
	}
	h.setConnectionKeys()
	got := tools(tok, "r1")
	for _, name := range []string{"openai_transcribe", "elevenlabs_transcribe", "elevenlabs_speak", "elevenlabs_voices"} {
		if !strings.Contains(got, `"name":"`+name+`"`) {
			t.Errorf("personal has no %s: %s", name, got)
		}
	}
	if got := tools(workTok, "r2"); strings.Contains(got, "openai_") || strings.Contains(got, "elevenlabs_") {
		t.Errorf("work's tools: %s", got)
	}

	note := filepath.Join(dir, "voice.ogg")
	if err := os.WriteFile(note, []byte("ogg-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	mcpCall := func(token, run, name string, args any) string {
		data, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
		return call(token, run, "POST", "/brain/mcp/call", string(data))
	}
	if got := mcpCall(tok, "r1", "openai_transcribe", map[string]any{"path": note, "language": "de"}); !strings.Contains(got, `transcribed by openai`) || strings.Contains(got, `"isError":true`) {
		t.Fatalf("openai_transcribe: %s", got)
	}
	if _, _, files := fake.seen(); len(files) != 1 || string(files[0]) != "ogg-audio" {
		t.Errorf("the recording sent: %q", files)
	}
	// A minute at $0.003 is over the limit of $0.001.
	if got := mcpCall(tok, "r1", "openai_transcribe", map[string]any{"path": note}); !strings.Contains(got, "brain:personal is over its monthly budget of $0.001 for OpenAI") {
		t.Errorf("over budget: %s", got)
	}
	if got := mcpCall(tok, "r1", "elevenlabs_transcribe", map[string]any{"path": "/etc/hosts"}); !strings.Contains(got, "path must be a file in your folder") {
		t.Errorf("a file outside: %s", got)
	}
	if got := mcpCall(tok, "r1", "elevenlabs_transcribe", map[string]any{"path": note}); !strings.Contains(got, `transcribed by elevenlabs`) || !strings.Contains(got, `deu`) {
		t.Errorf("elevenlabs_transcribe: %s", got)
	}
	got = mcpCall(tok, "r1", "elevenlabs_speak", map[string]any{"text": "Hallo"})
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	var spoken struct {
		Path        string `json:"path"`
		ContentType string `json:"content_type"`
		Characters  int    `json:"characters"`
	}
	if json.Unmarshal([]byte(got), &res) != nil || len(res.Content) == 0 || json.Unmarshal([]byte(res.Content[0].Text), &spoken) != nil {
		t.Fatalf("elevenlabs_speak: %s", got)
	}
	tmp := brainStateOf(AbsPath(h.DataDir()), "personal").tmp
	if data, err := os.ReadFile(spoken.Path); err != nil || string(data) != "ID3fake" || filepath.Dir(spoken.Path) != filepath.Join(tmp, "speech") ||
		filepath.Ext(spoken.Path) != ".mp3" || spoken.ContentType != "audio/mpeg" || spoken.Characters != 5 {
		t.Errorf("the speech: %+v %v", spoken, err)
	}
	if got := mcpCall(tok, "r1", "elevenlabs_voices", nil); !strings.Contains(got, `\"id\": \"v1\"`) {
		t.Errorf("voices: %s", got)
	}
	if got := mcpCall(workTok, "r2", "elevenlabs_voices", nil); !strings.Contains(got, "this run has no tool elevenlabs_voices") {
		t.Errorf("work calls a tool it has not: %s", got)
	}
	var by []string
	for _, l := range h.auditOf("connection_call") {
		if l["run"] != "r1" || l["brain"] != "personal" || l["by"] != "brain:personal" {
			t.Errorf("audit line %v", l)
		}
		by = append(by, l["detail"].(string))
	}
	if strings.Join(by, "|") != "openai transcribe gpt-4o-mini-transcribe|openai transcribe gpt-4o-mini-transcribe|elevenlabs transcribe scribe_v1|elevenlabs tts eleven_multilingual_v2|elevenlabs voices" {
		t.Errorf("audit %v", by)
	}
}

func TestWriteNewFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "speech")
	a, err := writeNewFile(dir, "x", ".mp3", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := writeNewFile(dir, "x", ".mp3", []byte("b"))
	if err != nil || filepath.Base(a) != "x.mp3" || filepath.Base(b) != "x-2.mp3" {
		t.Errorf("%s %s %v", a, b, err)
	}
	if _, err := writeNewFile(filepath.Join(a, "sub"), "x", ".mp3", nil); err == nil {
		t.Error("a folder below a file")
	}
}
