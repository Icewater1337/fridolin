package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBoxCommands(t *testing.T) {
	src := BoxSourceFromEnv(func(name string) string {
		return map[string]string{"FRIDOLIN_BOX_SOURCE": " https://example.com/fridolin/abc/ ", "FRIDOLIN_BOX_REGISTRY": "ghcr.io/owner"}[name]
	}, "abc12345")
	sh, ps := BoxCommands("fj-code", "core.example.ts.net:8766", src)
	if sh != "curl -fsSL https://example.com/fridolin/abc/container/box.sh | sh -s -- --registry ghcr.io/owner --version abc12345 --core core.example.ts.net:8766 --code fj-code" {
		t.Errorf("sh: %s", sh)
	}
	if !strings.HasPrefix(ps, "& ([scriptblock]::Create((irm https://example.com/fridolin/abc/container/box.ps1))) -Registry ghcr.io/owner ") {
		t.Errorf("PowerShell: %s", ps)
	}
	if sh, ps := BoxCommands("fj-code", "core:8766", BoxSourceFromEnv(func(string) string { return "" }, "abc12345")); sh != "" || ps != "" {
		t.Errorf("a core that does not know its box source offers %q, %q", sh, ps)
	}
}

func TestNativeCommands(t *testing.T) {
	src := BoxSourceFromEnv(func(name string) string {
		return map[string]string{"FRIDOLIN_BOX_SOURCE": "https://example.com/fridolin/abc"}[name]
	}, "abc12345")
	sh, ps := NativeCommands("fj-code", "core.example.ts.net:8766", src)
	if sh != "curl -fsSL https://example.com/fridolin/abc/container/node.sh | sh -s -- --core core.example.ts.net:8766 --code fj-code" {
		t.Errorf("sh: %s", sh)
	}
	if ps != "& ([scriptblock]::Create((irm https://example.com/fridolin/abc/container/node.ps1))) -Core core.example.ts.net:8766 -Code fj-code" {
		t.Errorf("PowerShell: %s", ps)
	}
	if sh, ps := NativeCommands("fj-code", "core:8766", BoxSourceFromEnv(func(string) string { return "" }, "abc12345")); sh != "" || ps != "" {
		t.Errorf("a core that does not know where its scripts are offers %q, %q", sh, ps)
	}
}

// joinCodeNodes is a fleet that only makes join codes.
type joinCodeNodes struct{ NodesService }

func (joinCodeNodes) NewJoinCodeFrom(string) (string, time.Time, error) {
	return "fj-code", time.Date(2026, 10, 28, 9, 10, 0, 0, time.UTC), nil
}

// POST /api/nodes/join-code answers every line, the old fields as they
// were and the native ones beside them.
func TestJoinCodeAnswersEveryLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		src    BoxSource
		native bool
	}{
		{"published", BoxSource{Source: "https://example.com/fridolin/abc", Registry: "ghcr.io/owner", Version: "abc12345"}, true},
		{"a local build", BoxSource{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &NodesAPI{Nodes: func() NodesService { return joinCodeNodes{} }, Enabled: true,
				CoreAddress: func() string { return "core.example.ts.net:8766" }, Box: tc.src}
			w := httptest.NewRecorder()
			a.JoinCode(w, httptest.NewRequest(http.MethodPost, "/api/nodes/join-code", strings.NewReader("{}")))
			var got map[string]any
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			sh, ps := JoinCommands("fj-code", "core.example.ts.net:8766")
			if got["code"] != "fj-code" || got["core"] != "core.example.ts.net:8766" || got["command"] != sh || got["command_powershell"] != ps {
				t.Fatalf("the old fields: %v", got)
			}
			nsh, nps := NativeCommands("fj-code", "core.example.ts.net:8766", tc.src)
			if _, ok := got["native_command"]; ok != tc.native || (tc.native && (got["native_command"] != nsh || got["native_command_powershell"] != nps)) {
				t.Fatalf("the native lines: %v", got)
			}
			if _, ok := got["box_command"]; ok != tc.native {
				t.Fatalf("the box lines: %v", got)
			}
		})
	}
}
