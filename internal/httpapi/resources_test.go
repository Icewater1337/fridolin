package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/registry"
	sitesvc "github.com/Icewater1337/fridolin/internal/site"
)

type configReadFake struct {
	ConfigStore
	reads    int
	snapshot config.Snapshot
}

func (f *configReadFake) Snapshot() config.Snapshot { f.reads++; return f.snapshot }
func (f *configReadFake) Path() string              { return "config.json" }
func TestConfigGetUsesOneSnapshotWithoutMutatingIt(t *testing.T) {
	f := &configReadFake{snapshot: config.Snapshot{File: &config.Config{BrainDirs: []config.BrainRef{{Path: "brain"}}}, Effective: &config.Config{BrainDirs: []config.BrainRef{{Path: "brain"}}}, ETag: "revision"}}
	a := ConfigAPI{Store: func() ConfigStore { return f }}
	w := httptest.NewRecorder()
	a.Get(w, httptest.NewRequest("GET", "/config", nil))
	var response ConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if f.reads != 1 || response.ETag != "revision" || len(response.Config.BrainDirs) != 0 || len(response.Effective.BrainDirs) != 0 || len(f.snapshot.File.BrainDirs) != 1 || len(f.snapshot.Effective.BrainDirs) != 1 {
		t.Fatalf("reads=%d response=%+v", f.reads, response)
	}
}

func TestChatAllowDoesNotDismissBeforeConfigurationSucceeds(t *testing.T) {
	dismissed := false
	a := ChatsAPI{AllowChat: func(dm bool, entry, brain string) error {
		if !dm || entry != "15551234567" {
			t.Fatalf("%v %s", dm, entry)
		}
		return errors.New("save failed")
	}, DismissIgnored: func(string, bool) error { dismissed = true; return nil }}
	w := httptest.NewRecorder()
	a.Allow(w, httptest.NewRequest("POST", "/allow", strings.NewReader(`{"kind":"dm","number":"15551234567"}`)))
	if w.Code != 422 || dismissed {
		t.Fatalf("%d dismissed=%v", w.Code, dismissed)
	}
}

type inspectFake struct {
	BrainRegistry
	path string
}

func (f *inspectFake) Inspect(path string) (registry.Inspection, error) {
	f.path = path
	return registry.Inspection{AbsPath: path, Problems: []string{"protected"}, Warnings: []string{}}, nil
}
func TestBrainInspectionPreservesRefusalWithoutMetadata(t *testing.T) {
	f := &inspectFake{}
	a := BrainsAPI{Registry: func() BrainRegistry { return f }}
	w := httptest.NewRecorder()
	a.Inspect(w, httptest.NewRequest("POST", "/inspect", strings.NewReader(`{"path":"candidate"}`)))
	if w.Code != 200 || f.path != "candidate" || strings.Contains(w.Body.String(), `"exists"`) {
		t.Fatalf("%d %s path=%q", w.Code, w.Body, f.path)
	}
}

func TestBrainPollDoesNotRevealAnotherBrainsPoll(t *testing.T) {
	reads := 0
	a := BrainAPI{BrainForToken: func(token string) *brain.Brain {
		if token == "allowed" {
			return &brain.Brain{}
		}
		return nil
	}, ReadPoll: func(context.Context, string) (string, any, error) {
		reads++
		return "other-chat", struct {
			Secret string `json:"secret"`
		}{"hidden"}, nil
	}, OwnsChat: func(*brain.Brain, string) bool { return false }}
	for _, token := range []string{"denied", "allowed"} {
		r := httptest.NewRequest("GET", "/brain/polls?id=poll", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		want := 401
		if token == "allowed" {
			want = 404
		}
		if w.Code != want || strings.Contains(w.Body.String(), "hidden") {
			t.Fatalf("%s: %d %s", token, w.Code, w.Body)
		}
	}
	if reads != 1 {
		t.Fatalf("unauthorized read: %d", reads)
	}
}

func TestBuildChatMissingBrainDoesNotReadChat(t *testing.T) {
	a := BuildChatAPI{Brain: func(string) *brain.Brain { return nil }, View: func(*brain.Brain) BuildChatResponse { t.Fatal("read missing brain"); return BuildChatResponse{} }}
	w := httptest.NewRecorder()
	a.Chat(w, httptest.NewRequest("GET", "/build/missing", nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestStatusAdaptersUseRequestSnapshot(t *testing.T) {
	type marker struct{}
	ctx := context.WithValue(t.Context(), marker{}, "request")
	a := StatusAPI{Snapshot: func(got context.Context) StatusResponse {
		if got.Value(marker{}) != "request" {
			t.Error("request context lost")
		}
		return StatusResponse{Version: "test-version"}
	}}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil).WithContext(ctx))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"test-version"`) {
		t.Fatal(w.Body.String())
	}
	calls := 0
	s := SiteAPI{StatusView: func() sitesvc.StatusSnapshot {
		calls++
		return sitesvc.StatusSnapshot{PublicURL: "https://example.com/"}
	}}
	w = httptest.NewRecorder()
	s.Status(w, httptest.NewRequest("GET", "/site", nil))
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), `"public_url":"https://example.com/"`) {
		t.Fatal(w.Body.String())
	}
}
