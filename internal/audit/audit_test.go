package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName)
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	l := &Log{Dir: dir, Now: func() time.Time { return now }}
	l.Append(Entry{Kind: RunStart, Thread: "a", Brain: "core", Run: "r1"})
	now = now.Add(2 * time.Hour) // October
	l.Append(Entry{Kind: ApprovalRequested, Thread: "a", Brain: "core", Run: "r1", ID: "x", Detail: "Submit the return with password: hunter2 and key " + strings.Repeat("a", 40)})
	l.Append(Entry{Kind: RunStart, Thread: "b", Brain: "family", Run: "r2"})

	all, err := Read(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Run != "r2" || all[2].TS != "2026-09-30T23:00:00Z" {
		t.Fatalf("newest first across months: %+v", all)
	}
	if d := all[1].Detail; strings.Contains(d, "hunter2") || strings.Contains(d, strings.Repeat("a", 40)) {
		t.Errorf("a secret reached the log: %q", d)
	}
	got, _ := Read(dir, Filter{Thread: "a", Kind: RunStart})
	if len(got) != 1 || got[0].Run != "r1" {
		t.Errorf("filter: %+v", got)
	}
	got, _ = Read(dir, Filter{Months: 1})
	if len(got) != 2 {
		t.Errorf("one month: %+v", got)
	}
	got, _ = Read(dir, Filter{Limit: 1})
	if len(got) != 1 {
		t.Errorf("limit: %+v", got)
	}
	got, _ = Read(dir, Filter{Query: "SUBMIT"})
	if len(got) != 1 {
		t.Errorf("query: %+v", got)
	}
	// A broken line and a huge one are skipped.
	f, _ := os.OpenFile(filepath.Join(dir, "2026-10.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{broken\n" + `{"kind":"x","detail":"` + strings.Repeat("y", 200<<10) + "\"}\n" + `{"kind":"lockdown"}` + "\n")
	f.Close()
	got, _ = Read(dir, Filter{})
	if len(got) != 4 || got[0].Kind != Lockdown {
		t.Errorf("after junk: %+v", got[0])
	}
	if got, err := Read(filepath.Join(dir, "none"), Filter{}); err != nil || len(got) != 0 {
		t.Errorf("a missing folder: %v %v", got, err)
	}
}

func TestScrubCommandLines(t *testing.T) {
	for in, leak := range map[string]string{
		"curl -H 'Authorization: Bearer abcdef123456' https://x": "abcdef123456",
		"export OPENAI_KEY=sk-proj-abcdefgh1234":                 "sk-proj-abcdefgh1234",
		"gh auth login --with-token ghp_abcdefgh1234":            "ghp_abcdefgh1234",
		"mysql --password=hunter2hunter -u root":                 "hunter2hunter",
		"TOKEN=abc123 ./deploy.sh":                               "abc123",
		"ElevenLabs: invalid key sk_0123abcd4567":                "sk_0123abcd4567",
	} {
		if out := Scrub(in); strings.Contains(out, leak) {
			t.Errorf("Scrub(%q) = %q, still has %q", in, out, leak)
		}
	}
	if got := Scrub("git status && ls -la"); got != "git status && ls -la" {
		t.Errorf("an ordinary command changed: %q", got)
	}
}

// made-up random parts of keys; the keys are put together in the test, so
// that no line holds one whole.
const (
	rnd36 = "A1b2C3d4E5f6G7h8J9k0L1m2N3p4Q5r6S7t8"
	hex48 = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a6978"
)

func TestSecretIn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// Keys by their prefix, as their vendors make them.
		{"use sk-proj-" + rnd36 + " for it", SecretKey},
		{"the ElevenLabs key is sk_" + hex48, SecretKey},
		{"token " + "gh" + "p_" + rnd36, SecretKey},
		{"github_" + "pat_11ABCDEFG0" + rnd36, SecretKey},
		{"xo" + "xb-1234567890-0987654321-" + rnd36, SecretKey},
		{"aws: " + "AK" + "IAIOSFODNN7EXAMPLE", SecretKey},
		{"gl" + "pat-xY9zA8bC7dE6fG5hI4jK", SecretKey},
		{"SK-PROJ-" + rnd36, SecretKey},
		// Credentials in a header or on a command line.
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.Zm9vYmFy", SecretAuth},
		{"Authorization: Basic dXNlcjpwYXNz", SecretAuth},
		{"mysql --password=hunter2hunter -u root", SecretFlag},
		{"deploy --token 7f3a9c1e5b2d", SecretFlag},
		// A secret named with its value.
		{"the wifi password: hunter2", SecretKeyValue},
		{"Her PIN: 4821", SecretKeyValue},
		{`api_key="Zx81Qw72Er63"`, SecretKeyValue},
		{"TOKEN=abc123 ./deploy.sh", SecretKeyValue},
		{"secret = 'Tr0ub4dor&3'", SecretKeyValue},

		// Near-misses: none of these is a secret.
		{"", ""},
		{"See you at 8, the table is booked for 4 people.", ""},
		{"commit 3f2a9c1e5b7d4f6a8c0e2b4d6f8a0c2e4b6d8f0a fixed it", ""},
		{"run 123e4567-e89b-12d3-a456-426614174000 ended", ""},
		{"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", ""},
		{"Donaudampfschifffahrtsgesellschaftskapitän and internationalization", ""},
		{"sha256:" + hex48 + hex48[:16], ""},
		{"Add a field token: string to the config, and a --token flag.", ""},
		{"a basic setup, by the bearer of good news", ""},
		{"Basic Auth is enough", ""},
		{"set password: <your password> and token=$GITHUB_TOKEN", ""},
		{"api_key: [redacted], secret: ****, pin: 12", ""},
		{"api_key: {{API_KEY}}, token: %TOKEN%", ""},
		{"the task-runner and desk_reservation_2024 and Ask-me-anything-2024", ""},
		{"scikit-learn and sk-learn", ""},
	} {
		if got := SecretIn(tc.in); got != tc.want {
			t.Errorf("SecretIn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestScrubMasksWhatSecretInFinds holds the two to the same patterns:
// what SecretIn calls a secret, Scrub masks.
func TestScrubMasksWhatSecretInFinds(t *testing.T) {
	for _, s := range []string{
		"use sk-proj-" + rnd36,
		"Authorization: Basic dXNlcjpwYXNz",
		"mysql --password=hunter2hunter",
		"the wifi password: hunter2",
		"xo" + "xb-1234567890-0987654321",
	} {
		if SecretIn(s) == "" {
			t.Errorf("SecretIn(%q) found nothing", s)
		}
		if out := Scrub(s); SecretIn(out) != "" || !strings.Contains(out, "[redacted]") {
			t.Errorf("Scrub(%q) = %q", s, out)
		}
	}
}
