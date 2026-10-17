package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout returns what fn prints to standard output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	out := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		out <- string(data)
	}()
	fn()
	w.Close()
	return <-out
}

func TestCheckShowsWhoMayDM(t *testing.T) {
	const (
		owner    = "15551234567@s.whatsapp.net"
		stranger = "15559876543@s.whatsapp.net"
		ignored  = "(not in allowed_numbers: ignored)"
	)
	tests := []struct {
		name   string
		access string // config fields that decide DM access
		allow  string // the DM allow line
		// whether each route line says the chat is ignored
		ownerIgnored, strangerIgnored bool
	}{
		{"unset flag and empty list", `"allowed_numbers": []`, "DM allow:  nobody", true, true},
		{"explicit true and empty list", `"allowed_numbers": [], "allow_any_dm": true`, "DM allow:  anyone", false, false},
		{"numbers listed", `"allowed_numbers": ["15551234567"], "allow_any_dm": true`, "DM allow:  [15551234567]", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := writeConfig(t, `{
				"brain_mode": true,
				"data_dir": "`+home+`/data",
				"brains": [{"name": "main", "work_dir": "`+home+`/main", "handles_dms": true}],
				`+tc.access+`
			}`)
			out := captureStdout(t, func() {
				checkCmd([]string{"--config", path, "--route", owner, "--route", stranger})
			})
			if !strings.Contains(out, tc.allow) {
				t.Errorf("want %q in:\n%s", tc.allow, out)
			}
			for jid, wantIgnored := range map[string]bool{owner: tc.ownerIgnored, stranger: tc.strangerIgnored} {
				line := lineWith(out, "route "+jid)
				if line == "" {
					t.Fatalf("no route line for %s in:\n%s", jid, out)
				}
				if got := strings.Contains(line, ignored); got != wantIgnored {
					t.Errorf("%q: ignored = %v, want %v", line, got, wantIgnored)
				}
			}
		})
	}
}

// lineWith returns the first line of out that contains s.
func lineWith(out, s string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}
