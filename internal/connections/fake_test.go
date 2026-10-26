package connections

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Fake keys. Never real ones.
const (
	testOpenAIKey     = "sk-test-0000000000000000000000000000openai"
	testElevenLabsKey = "sk_test_00000000000000000000000000000000eleven"
)

// seen is one request the fake providers got.
type seen struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
	// Form holds a multipart request's fields; File its file.
	Form     map[string]string
	File     []byte
	Filename string
	FileType string
}

// fakeProviders is OpenAI and ElevenLabs on one test server. answer
// decides each answer; by default every call succeeds.
type fakeProviders struct {
	*httptest.Server
	mu     sync.Mutex
	got    []seen
	answer func(w http.ResponseWriter, s seen) bool
}

func newFakeProviders(t *testing.T) *fakeProviders {
	t.Helper()
	f := &fakeProviders{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeProviders) serve(w http.ResponseWriter, r *http.Request) {
	s := seen{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Form: map[string]string{}}
	if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "multipart/form-data" {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			if p.FormName() == "file" {
				s.File, s.Filename, s.FileType = data, p.FileName(), p.Header.Get("Content-Type")
			} else {
				s.Form[p.FormName()] = string(data)
			}
		}
	} else {
		s.Body, _ = io.ReadAll(r.Body)
	}
	f.mu.Lock()
	f.got = append(f.got, s)
	answer := f.answer
	f.mu.Unlock()
	if answer != nil && answer(w, s) {
		return
	}
	switch s.Path {
	case "/v1/audio/transcriptions":
		writeJSON(w, 200, map[string]any{"text": "hallo welt", "usage": map[string]any{"type": "duration", "seconds": 90}})
	case "/v1/responses":
		writeJSON(w, 200, map[string]any{"id": "resp_1", "model": "gpt-4o-mini-2024-07-18", "output_text": "hi", "usage": map[string]any{"input_tokens": 1000, "output_tokens": 2000}})
	case "/v1/speech-to-text":
		writeJSON(w, 200, map[string]any{"text": "guten tag", "language_code": "deu", "words": []map[string]any{{"text": "guten", "end": 0.5}, {"text": "tag", "end": 120}}})
	case "/v1/voices":
		writeJSON(w, 200, map[string]any{"voices": []map[string]any{{"voice_id": "v1", "name": "Rachel", "category": "premade"}, {"voice_id": "v2", "name": "Adam"}}})
	case "/v1/models":
		writeJSON(w, 200, map[string]any{"data": []any{}})
	default:
		if strings.HasPrefix(s.Path, "/v1/text-to-speech/") {
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Header().Set("character-cost", "42")
			_, _ = w.Write([]byte("ID3-fake-mp3"))
			return
		}
		http.NotFound(w, r)
	}
}

// last is the last request the fakes got.
func (f *fakeProviders) last(t *testing.T) seen {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) == 0 {
		t.Fatal("the providers got no request")
	}
	return f.got[len(f.got)-1]
}

func (f *fakeProviders) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *fakeProviders) setAnswer(fn func(w http.ResponseWriter, s seen) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = fn
}

// testClock is a clock tests move.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// testService is a service in a temp folder against fake providers, with
// both keys set unless noKeys, and the events it audits.
type testService struct {
	*Service
	fake     *fakeProviders
	clock    *testClock
	settings *Settings
	mu       sync.Mutex
	events   []Event
}

func newTestService(t *testing.T, noKeys bool) *testService {
	t.Helper()
	ts := &testService{fake: newFakeProviders(t), clock: &testClock{now: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}, settings: &Settings{}}
	ts.Service = New(Options{
		Dir:       filepath.Join(t.TempDir(), "connections"),
		Settings:  func() Settings { ts.mu.Lock(); defer ts.mu.Unlock(); return *ts.settings },
		HTTP:      ts.fake.Client(),
		Endpoints: Endpoints{OpenAI: ts.fake.URL, ElevenLabs: ts.fake.URL},
		Now:       ts.clock.Now,
		Audit: func(e Event) {
			ts.mu.Lock()
			defer ts.mu.Unlock()
			ts.events = append(ts.events, e)
		},
	})
	if !noKeys {
		if err := ts.SetKey(OpenAI, KeyUpdate{APIKey: testOpenAIKey}); err != nil {
			t.Fatal(err)
		}
		if err := ts.SetKey(ElevenLabs, KeyUpdate{APIKey: testElevenLabsKey}); err != nil {
			t.Fatal(err)
		}
	}
	return ts
}

func (ts *testService) setSettings(s Settings) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	*ts.settings = s
}

func (ts *testService) audited() []Event {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]Event(nil), ts.events...)
}

// usageOf is caller's usage of conn this month.
func (ts *testService) usageOf(conn, caller string) Usage {
	_, rows := ts.MonthUsage()
	for _, r := range rows {
		if r.Connection == conn && r.Caller == caller {
			return r.Usage
		}
	}
	return Usage{}
}

// assertNoKey fails when a key shows in data.
func assertNoKey(t *testing.T, what string, data []byte) {
	t.Helper()
	for _, k := range []string{testOpenAIKey, testElevenLabsKey} {
		if strings.Contains(string(data), k) {
			t.Errorf("%s shows a key: %s", what, data)
		}
	}
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
