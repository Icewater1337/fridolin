package claudecli

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestAntigravityArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		s          Options
		a          AntigravityOptions
		want, not  []string
		deny       []string
		agentMode  bool
		termSandbx bool
	}{
		{name: "tools off", s: Options{}, a: AntigravityOptions{ExternalSandbox: true},
			not:  []string{"--dangerously-skip-permissions", "--sandbox", "--model", "--effort"},
			deny: []string{"command(*)", "write_file(*)", "read_url(*)", "execute_url(*)", "download_url(*)"}, agentMode: true},
		{name: "tools on, outer sandbox", s: Options{Dangerously: true, Model: "gemini-x", Effort: "high"}, a: AntigravityOptions{ExternalSandbox: true},
			want: []string{"--dangerously-skip-permissions", "--mode", "accept-edits", "--model", "gemini-x", "--effort", "high"},
			not:  []string{"--sandbox"}},
		{name: "tools on, no outer sandbox", s: Options{Dangerously: true}, a: AntigravityOptions{},
			want: []string{"--dangerously-skip-permissions", "--sandbox"}, termSandbx: true},
		{name: "WebFetch blocked", s: Options{Dangerously: true, DisallowedTools: []string{"WebFetch"}, MediaInbox: "/in"}, a: AntigravityOptions{ExternalSandbox: true},
			want: []string{"--add-dir", "/in"}, deny: []string{"read_url(*)", "execute_url(*)", "download_url(*)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := AntigravityArgs(tc.s, tc.a)
			if args[len(args)-1] != "-p=" {
				t.Errorf("args end in %q, want -p=", args[len(args)-1])
			}
			for _, w := range append(tc.want, "--output-format", "stream-json", "--input-format", "--disable-slash-commands", "--settings") {
				if !slices.Contains(args, w) {
					t.Errorf("args %q lack %q", args, w)
				}
			}
			for _, n := range tc.not {
				if slices.Contains(args, n) {
					t.Errorf("args %q have %q", args, n)
				}
			}
			var settings struct {
				AgentMode   *string `json:"agentMode"`
				Sandbox     bool    `json:"enableTerminalSandbox"`
				Permissions struct{ Deny, Allow []string }
			}
			i := slices.Index(args, "--settings")
			if err := json.Unmarshal([]byte(args[i+1]), &settings); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(settings.Permissions.Deny, tc.deny) && (len(tc.deny) > 0 || len(settings.Permissions.Deny) > 0) {
				t.Errorf("deny = %q, want %q", settings.Permissions.Deny, tc.deny)
			}
			if (settings.AgentMode != nil) != tc.agentMode || settings.Sandbox != tc.termSandbx {
				t.Errorf("settings = %s", args[i+1])
			}
		})
	}
}

func TestAntigravitySettingsAllowTheBridgesTools(t *testing.T) {
	t.Parallel()
	got := AntigravitySettings(Options{}, AntigravityOptions{MCPServer: "fridolin", APIKey: true})
	for _, want := range []string{`"allow":["mcp(fridolin/*)"]`, `"modelProvider":"gemini"`} {
		if !strings.Contains(got, want) {
			t.Errorf("settings %s lack %s", got, want)
		}
	}
}

func TestValidateAntigravityTools(t *testing.T) {
	t.Parallel()
	if err := ValidateAntigravityTools([]string{"WebFetch"}); err != nil {
		t.Errorf("WebFetch: %v", err)
	}
	for _, tool := range []string{"WebSearch", "Bash", "mcp__x"} {
		if err := ValidateAntigravityTools([]string{"WebFetch", tool}); err == nil || !strings.Contains(err.Error(), tool) {
			t.Errorf("%s: err = %v, want a refusal naming it", tool, err)
		}
	}
}

func TestAntigravityPrompt(t *testing.T) {
	t.Parallel()
	line := AntigravityPrompt("hi \"you\"\nsecond line")
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("not one line: %q", line)
	}
	var m struct {
		Event   string
		Message struct{ Content string }
	}
	if err := json.Unmarshal([]byte(line), &m); err != nil || m.Event != "user" || m.Message.Content != "hi \"you\"\nsecond line" {
		t.Errorf("prompt = %q (%v)", line, err)
	}
}

// The real result of agy 1.5.14 run without a login.
func TestAntigravityUnauthenticatedSample(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/agy-unauthenticated.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var sink recordingSink
	res := ParseProvider(strings.NewReader(string(data)), &sink, ProviderAntigravity)
	if res.Err == nil || !strings.Contains(res.Err.Error(), AntigravityNotSignedIn) || !strings.HasPrefix(res.Err.Error(), "antigravity error: ") {
		t.Fatalf("err = %v, want the sign-in error", res.Err)
	}
	if len(sink.results) != 1 {
		t.Errorf("sink got %d results", len(sink.results))
	}
}
