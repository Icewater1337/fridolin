package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/connections"
	"github.com/Icewater1337/fridolin/internal/google"

	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// fakeRclone writes a stand-in for rclone into dir and returns its path.
// It answers `version`, lists nothing (a first backup, no versions yet),
// accepts every other command, and logs each call to calls.log next to it.
func fakeRclone(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "rclone")
	script := `#!/bin/sh
printf '%s\n' "$*" >>"$(dirname "$0")/calls.log"
[ "$1" = --config ] && shift 2
case "$1" in
version) echo "rclone v1.70.3" ;;
lsjson) echo "[]" ;;
esac
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// goldenManifest is the brain of goldenBridge: the DM brain and the
// default, serving harnessGroup, backed up, with a calendar.
var goldenManifest = manifest.Manifest{
	ID: "main", Name: "Main",
	Routing: manifest.Routing{HandlesDMs: true, Default: true, Groups: []string{harnessGroup}},
	Backup:  manifest.Backup{Enabled: true, Exclude: []string{"node_modules/", "*.log", "- private.txt"}},
	Calendar: &manifest.Calendar{
		Account: "you@example.com", Calendars: []string{"you@example.com", "team@example.com"}, Default: "you@example.com",
	},
}

// goldenClientJSON is a Google OAuth client file of the Desktop app kind.
const goldenClientJSON = `{"installed":{"client_id":"123-abc.apps.googleusercontent.com","client_secret":"TEST-CLIENT-SECRET","redirect_uris":["http://localhost"]}}`

// goldenBridge is a dev-mode bridge with something on every page: a brain
// that answered a DM, a chat that was ignored, a finished backup to a
// connected (fake) Drive, a calendar sign-in and Plaud on the fake CLI.
// Everything is set up through the API, except what a sign-in in the
// browser would write: the Drive remote and the calendar token. fakes is
// the folder with the fake rclone and its calls.log.
func goldenBridge(t *testing.T) (h *harness, s *scrubber, fakes string) {
	t.Helper()
	fakes = fsx.Canonical(t.TempDir())
	rclone := fakeRclone(t, fakes)
	newFakePlaud(t)
	// No call of the connections may leave the test.
	optionsOf(t).hooks.connections = &connections.Endpoints{OpenAI: "http://127.0.0.1:9", ElevenLabs: "http://127.0.0.1:9"}
	h = newHarness(t, `{
		"ingest": {"enabled": true},
		"backup": {"enabled": true, "rclone_path": "`+rclone+`"},
		"plaud": {"enabled": true, "cli_path": "`+os.Args[0]+`"}
	}`)
	s = h.scrubber().maskKeys("rclone.install_hint")
	s.addPath(fakes, "<FAKES>")
	// The HOME the Plaud CLI runs with, a folder named home in the data
	// dir's plaud folder, which the personal-data check would take for a
	// user's home folder.
	s.addPath(filepath.Join(h.DataDir(), "plaud", "home"), "<PLAUD_HOME>")

	conf := filepath.Join(h.DataDir(), "rclone", "rclone.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("[brain-gdrive]\ntype = alias\nremote = "+filepath.Join(fakes, "drive")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.AddBrain(goldenManifest)

	if w := h.Do("PUT", "/api/google/client", map[string]string{"json": goldenClientJSON}); w.Code != http.StatusOK {
		t.Fatalf("google client: %d %s", w.Code, w.Body.String())
	}
	tok, err := json.Marshal(google.Token{Token: "golden-access-token", RefreshToken: "golden-refresh-token", ClientID: "123-abc.apps.googleusercontent.com", Scopes: calendarScopes, Account: "you@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.DataDir(), "google_calendar_token_main.json"), tok, 0o600); err != nil {
		t.Fatal(err)
	}

	// An OpenAI key, which main may use.
	if w := h.Do("PUT", "/api/connections/openai", map[string]string{"api_key": goldenOpenAIKey}); w.Code != http.StatusOK {
		t.Fatalf("the OpenAI key: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("PUT", "/api/connections/openai/brains", map[string]any{"brains": []string{"main"}, "brain_monthly_usd": 10}); w.Code != http.StatusOK {
		t.Fatalf("the OpenAI brains: %d %s", w.Code, w.Body.String())
	}
	// A GitLab connection, kept on this machine, and GitHub's commit name.
	if w := h.Do("PUT", "/api/gitlab", map[string]string{"base_url": "https://gitlab.example.com", "token": goldenGitLabToken, "name": "Jane Doe", "email": "jane@example.com"}); w.Code != http.StatusOK {
		t.Fatalf("the GitLab connection: %d %s", w.Code, w.Body.String())
	}
	if w := h.Do("PUT", "/api/github/identity", map[string]string{"name": "Jane Doe", "email": "you@example.com"}); w.Code != http.StatusOK {
		t.Fatalf("the GitHub identity: %d %s", w.Code, w.Body.String())
	}

	h.Inject(inbound{JID: harnessDM, Text: "hello"})
	h.WaitSent(harnessDM, "hello")
	h.WaitFor("both history lines", func() bool { return strings.Count(h.Do("GET", "/api/activity", nil).Body.String(), `"brain"`) == 2 })

	if w := h.Do("POST", "/api/dev/inbound", inbound{JID: "120363000000000001@g.us", Sender: "15559876543", Text: "hi"}); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "ignored") {
		t.Fatalf("a group that is not allowed: %d %s", w.Code, w.Body.String())
	}

	if w := h.Do("POST", "/api/backup/run", map[string]any{"brains": []string{"main"}}); w.Code != http.StatusAccepted {
		t.Fatalf("backup run: %d %s", w.Code, w.Body.String())
	}
	h.WaitBackup()

	if w := h.Do("POST", "/api/plaud/connect", nil); w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("plaud connect: %d %s", w.Code, w.Body.String())
	}
	h.WaitFor("the Plaud sign-in", func() bool {
		var st struct {
			Connected bool `json:"connected"`
			Connect   *struct {
				State string `json:"state"`
			} `json:"connect"`
		}
		_ = json.Unmarshal(h.Do("GET", "/api/plaud/status", nil).Body.Bytes(), &st)
		return st.Connected && (st.Connect == nil || st.Connect.State != "running")
	})
	return h, s, fakes
}

// WaitBackup waits until no backup runs, as the Backup page sees it.
func (h *harness) WaitBackup() {
	h.t.Helper()
	h.WaitFor("the backup to finish", func() bool {
		var st struct {
			Job any `json:"job"`
		}
		w := h.Do("GET", "/api/backup/status", nil)
		return json.Unmarshal(w.Body.Bytes(), &st) == nil && st.Job == nil
	})
}

// TestGoldenGETEndpoints pins every GET the dashboard and scripts use, on a
// bridge with a brain, an ignored chat, a backup, a calendar and Plaud.
func TestGoldenGETEndpoints(t *testing.T) {
	t.Parallel()
	h, s, _ := goldenBridge(t)
	tests := []struct {
		name, path string
		// mask lists what this endpoint reports about the machine or the
		// moment rather than the bridge.
		mask []string
	}{
		{"health", "/health", nil},
		{"ready", "/ready", nil},
		{"status", "/api/status", nil},
		{"whatsapp-qr", "/api/whatsapp/qr", nil},
		{"whatsapp-groups", "/api/whatsapp/groups", nil},
		{"activity", "/api/activity", nil},
		{"activity-other-brain", "/api/activity?brain=other", nil},
		// Every line the bridge logged so far, in the order goroutines ran.
		{"logs", "/api/logs", []string{"lines"}},
		{"chats-ignored", "/api/chats/ignored", nil},
		{"config", "/api/config", nil},
		{"brains", "/api/brains", nil},
		{"brain", "/api/brains/main", nil},
		{"brain-unknown", "/api/brains/nope", nil},
		// osascript on macOS, zenity or kdialog where installed.
		{"fs-capabilities", "/api/fs/capabilities", []string{"picker"}},
		{"fs-list", "/api/fs/list", nil},
		{"fs-list-outside-home", "/api/fs/list?path=/", nil},
		{"backup-status", "/api/backup/status", nil},
		{"backup-versions", "/api/brains/main/backup/versions", nil},
		{"backup-versions-unknown-brain", "/api/brains/nope/backup/versions", nil},
		{"vault-status", "/api/vault/status", nil},
		{"google-client", "/api/google/client", nil},
		{"calendar-health", "/api/calendar/health", nil},
		{"calendar", "/api/brains/main/calendar?list=0", nil},
		{"calendar-unknown-brain", "/api/brains/nope/calendar", nil},
		{"plaud-status", "/api/plaud/status", nil},
		{"github", "/api/github", nil},
		// Which of node, cloudflared and gh this machine has, and whether
		// it has sandbox-exec for the live site (macOS only).
		{"site", "/api/site", []string{"runtime", "site.sandbox.on", "site.sandbox.note"}},
		{"site-users", "/api/site/users", nil},
		{"site-builders", "/api/site/builders", nil},
		{"site-proposals", "/api/site/proposals", nil},
		{"site-secrets", "/api/site/secrets", nil},
		{"site-build", "/api/site/build/main", nil},
		{"dev-outbox", "/api/dev/outbox", nil},
		{"nodes", "/api/nodes", nil},
		{"policy", "/api/policy", nil},
		{"approvals", "/api/approvals", nil},
		{"housekeeping", "/api/housekeeping", nil},
		{"schedules", "/api/schedules", nil},
		{"schedule-runs", "/api/schedules/main/brief/runs", nil},
		{"schedule-log-unknown", "/api/schedules/main/brief/runs/20261023T060000-abcdef/log", nil},
		// Run ids are random, and how long a run took is the machine's.
		{"threads", "/api/threads", []string{"run", "duration_ms"}},
		{"audit", "/api/audit", []string{"run", "duration_ms"}},
		{"fride", "/api/fride", nil},
		// This month.
		{"connections", "/api/connections", []string{"month"}},
		{"gitlab", "/api/gitlab", nil},
		{"git-identities", "/api/git-identities", nil},
		{"brain-skills", "/api/brains/main/skills", nil},
		{"brain-skills-unknown-brain", "/api/brains/nope/skills", nil},
		{"unknown-endpoint", "/api/nope", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := h.Do("GET", tc.path, nil)
			s := s.with(tc.mask...)
			assertGolden(t, "api/get-"+tc.name+".golden", append([]byte("GET "+tc.path+"\n"), s.response(t, w)...))
		})
	}
}

// TestGoldenConfigWrites pins what PUT /api/config answers, and the errors
// of the endpoints that change settings, with their status codes.
func TestGoldenConfigWrites(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "")
	s := h.scrubber()
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	cfg := h.Config()
	var file map[string]any
	if err := json.Unmarshal(cfg.Config, &file); err != nil {
		t.Fatal(err)
	}
	// with is the config GET returned with changes; a key changed to nil
	// is left out.
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range file {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
			if v == nil {
				delete(out, k)
			}
		}
		return out
	}

	tests := []struct {
		name, method, path string
		body               func() any
	}{
		// A live setting and one that waits for a restart.
		{"put-config", "PUT", "/api/config", func() any {
			return map[string]any{"etag": h.Config().ETag, "config": with(map[string]any{"triggers": []string{"@bot"}, "http_listen": "127.0.0.1:8766"})}
		}},
		// Stale on its own: saved once, then sent with the ETag from before.
		{"put-config-stale-etag", "PUT", "/api/config", func() any {
			stale := h.Config().ETag
			if w := h.Do("PUT", "/api/config", map[string]any{"etag": stale, "config": with(map[string]any{"log_level": "warn"})}); w.Code != http.StatusOK {
				t.Fatalf("the save before: %d %s", w.Code, w.Body.String())
			}
			return map[string]any{"etag": stale, "config": with(nil)}
		}},
		{"put-config-invalid", "PUT", "/api/config", func() any {
			return map[string]any{"etag": h.Config().ETag, "config": with(map[string]any{"http_listen": "0.0.0.0:80"})}
		}},
		{"put-config-not-json", "PUT", "/api/config", func() any { return map[string]any{"etag": h.Config().ETag, "config": "nope"} }},
		{"put-config-no-etag", "PUT", "/api/config", func() any { return map[string]any{"config": with(nil)} }},
		// From a tab opened before the upgrade, so without a version.
		{"put-config-inline-brains", "PUT", "/api/config", func() any {
			return map[string]any{"etag": h.Config().ETag, "config": with(map[string]any{"version": nil, "brains": []map[string]string{{"id": "x"}}})}
		}},
		// From a newer bridge's dashboard, which this one would save without
		// the settings it does not know.
		{"put-config-newer-version", "PUT", "/api/config", func() any {
			return map[string]any{"etag": h.Config().ETag, "config": with(map[string]any{"version": config.Version + 1})}
		}},
		{"put-brain-unknown", "PUT", "/api/brains/nope", func() any { return map[string]any{"manifest": map[string]string{"id": "nope", "name": "Nope"}} }},
		{"put-brain-no-name", "PUT", "/api/brains/main", func() any { return map[string]any{"manifest": map[string]string{"id": "main"}} }},
		{"delete-brain-unknown", "DELETE", "/api/brains/nope", func() any { return nil }},
		{"add-brain-invalid-id", "POST", "/api/brains", func() any {
			return map[string]any{"path": "~/bad", "create_dir": true, "manifest": map[string]any{"id": "Bad Id", "name": "Bad"}}
		}},
		{"add-brain-duplicate", "POST", "/api/brains", func() any {
			return map[string]any{"path": "~/main2", "create_dir": true, "manifest": map[string]any{"id": "main", "name": "Main"}}
		}},
		{"stop-run-unknown-brain", "POST", "/api/brains/nope/stop", func() any { return nil }},
		{"allow-chat-invalid", "POST", "/api/chats/allow", func() any { return map[string]string{"jid": "nope"} }},
		{"backup-settings-invalid", "PUT", "/api/backup/settings", func() any { return map[string]any{"daily_at": "25:00"} }},
		{"backup-run-not-connected", "POST", "/api/backup/run", func() any { return map[string]any{} }},
		{"plaud-settings-unknown-field", "PUT", "/api/plaud/settings", func() any { return map[string]any{"interval_seconds": 5} }},
		{"plaud-settings-unknown-brain", "PUT", "/api/plaud/settings", func() any { return map[string]any{"brain": "nope"} }},
		{"google-client-invalid", "PUT", "/api/google/client", func() any { return map[string]string{"json": "{}"} }},
		{"dev-inbound-no-jid", "POST", "/api/dev/inbound", func() any { return map[string]string{"text": "x"} }},
		{"dev-inbound-unknown-brain", "POST", "/api/dev/inbound", func() any { return map[string]string{"jid": harnessDM, "text": "x", "brain": "nope"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body()
			w := h.Do(tc.method, tc.path, body)
			head := fmt.Sprintf("%s %s\n", tc.method, tc.path)
			// Not for the config, which goes whole, with an ETag that
			// changes each run.
			if body != nil && tc.path != "/api/config" {
				sent, _ := json.Marshal(body)
				head += s.text(string(sent)) + "\n"
			}
			assertGolden(t, "api/"+tc.name+".golden", append([]byte(head), s.response(t, w)...))
		})
	}
}
