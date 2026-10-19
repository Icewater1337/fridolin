package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// check says for each allowed group whose messages there reach its brain,
// as groupSenderAllowed decides: the numbers its group_members entry
// lists, every member for ["*"], and without an entry whom allowed_numbers
// admits.
func TestCheckShowsWhoIsHeardInGroups(t *testing.T) {
	const group = "120363000000000000@g.us"
	cases := []struct {
		name   string
		access string // config fields that decide who is heard
		heard  string // the group's line
	}{
		{"listed", `"group_members": {"` + group + `": ["+1 555 987 6543", "15551234567"]}`, "heard in " + group + ": [15559876543 15551234567] (group_members)"},
		{"everyone", `"group_members": {"` + group + `": ["*"]}`, "heard in " + group + `: every member (group_members is ["*"])`},
		{"an entry of no one", `"group_members": {"` + group + `": []}`, "heard in " + group + ": nobody (its group_members entry lists no one)"},
		{"no entry", "", "heard in " + group + ": [15551234567] (no group_members entry: allowed_numbers)"},
		{"no entry, nobody may DM", `"allowed_numbers": []`, "heard in " + group + ": nobody (no group_members entry, and allowed_numbers is empty)"},
		{"no entry, anyone may DM", `"allowed_numbers": [], "allow_any_dm": true`, "heard in " + group + ": every member (no group_members entry, allow_any_dm is true and allowed_numbers is empty)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			access := `"allowed_numbers": ["15551234567"]`
			if strings.Contains(c.access, "allowed_numbers") {
				access = c.access
			} else if c.access != "" {
				access += ", " + c.access
			}
			path := writeConfig(t, `{
				"claude_path": "`+fakeClaudeEnv(t, 0)+`",
				"data_dir": "`+home+`/data",
				"allowed_groups": ["`+group+`"],
				`+access+`
			}`)
			var out bytes.Buffer
			if err := WriteCheck(&out, CheckOptions{Config: path}); err != nil {
				t.Fatal(err)
			}
			if got := lineWith(out.String(), "heard in"); strings.TrimSpace(got) != c.heard {
				t.Errorf("got %q, want %q in:\n%s", got, c.heard, out.String())
			}
		})
	}
}

func TestCheckRedact(t *testing.T) {
	// check's output goes into public bug reports. With --redact it shows
	// no configured number, JID or home folder, in the report, the log
	// lines of loading the config and the sandbox profile alike; without
	// it, it shows them as they are. The home folder has a dot in its
	// name, which a sandbox profile's regular expressions write escaped.
	home := filepath.Join(fsx.Canonical(t.TempDir()), "first.last")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	testutil.ClearEnv(t, "FRIDOLIN_CONFIG", "CONFIG_FILE")
	// otherWritten has a note after the number, which makes it no number
	// to the bridge, and oddGroup has no @g.us, so loading the config
	// logs both as they are written.
	const (
		owner, other  = "15551234567", "15559876543"
		otherWritten  = "+1 555 987 6543 (mobile)"
		oddGroup      = "120363000000000004"
		allowedGroup  = "120363000000000001@g.us"
		brainGroup    = "120363000000000002@g.us"
		ownerJID      = owner + "@s.whatsapp.net"
		strangerGroup = "120363000000000003@g.us"
		// memberOnly is in group_members only, which check prints as
		// digits, with no + or JID that would give it away as a number.
		memberOnly = "15550001234" // personal-data:ignore (a fictional 555 number)
	)
	lid := strings.Repeat("7", 15) + "@lid"
	path := filepath.Join(home, ".fridolin", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{
		"claude_path": "` + fakeClaudeEnv(t, 0) + `",
		"data_dir": "~/.fridolin",
		"allowed_numbers": ["` + owner + `", "` + otherWritten + `"],
		"allowed_groups": ["` + allowedGroup + `", "` + oddGroup + `"],
		"group_members": {"` + allowedGroup + `": ["` + memberOnly + `"]},
		"not_a_setting": true` + brainDirsJSON(t,
		brainFolder(t, home+"/brains/main", manifest.Manifest{ID: "main", Routing: manifest.Routing{HandlesDMs: true, Groups: []string{brainGroup}}}),
	) + `}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	o := CheckOptions{Routes: []string{ownerJID, allowedGroup, strangerGroup, lid}, SandboxFor: "main"}
	if runtime.GOOS != "darwin" {
		o.SandboxFor = "" // the profile is sandbox-exec's
	}

	report := func(t *testing.T, redact bool) (string, []string) {
		t.Helper()
		logs := captureLog(t)
		o.Redact = redact
		var out bytes.Buffer
		if err := WriteCheck(&out, o); err != nil {
			t.Fatal(err)
		}
		return out.String(), logs()
	}
	secrets := []string{home, fsx.Resolve(home), regexp.QuoteMeta(home), owner, other, otherWritten, oddGroup, memberOnly,
		"120363000000000001", "120363000000000002", "120363000000000003", strings.Repeat("7", 15)}

	t.Run("redacted", func(t *testing.T) {
		out, logs := report(t, true)
		all := out + strings.Join(logs, "\n")
		for _, s := range secrets {
			if strings.Contains(all, s) {
				t.Errorf("%q is in the redacted output:\n%s", s, all)
			}
		}
		want := []string{
			"Config OK: ~/.fridolin/config.json\n",
			"  data dir:  ~/.fridolin\n",
			"  DM allow:  [1…67 +1…43]\n",
			"  groups:    […01@g.us …04]\n",
			"  heard in …01@g.us: [1…34] (group_members)\n",
			"  heard in …04: [1…67 +1…43] (no group_members entry: allowed_numbers)\n",
			"→ ~/brains/main (dms=true default=false groups=[…02@g.us])\n",
			"  route 1…67@s.whatsapp.net → main\n",
			"  route …01@g.us → ",
			"  route …03@g.us → main (not in allowed_groups: ignored)\n",
			"  route …77@lid → ",
		}
		if o.SandboxFor != "" {
			want = append(want, `"~/.fridolin"`, `"^~/`)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("no %q in:\n%s", w, out)
			}
		}
		for _, w := range []string{"config ~/.fridolin/config.json: ignoring unknown settings", `allowed_numbers "+1…43" (digits with the country code)`, `allowed_groups "…04"`} {
			if !logContains(func() []string { return logs }, w) {
				t.Errorf("the log of loading the config does not say %q: %q", w, logs)
			}
		}
	})

	t.Run("as it is", func(t *testing.T) {
		out, logs := report(t, false)
		for _, s := range append([]string{home}, owner, otherWritten, oddGroup, allowedGroup, brainGroup, strangerGroup, lid, memberOnly, "Config OK: "+path+"\n", "  DM allow:  ["+owner+" "+otherWritten+"]\n") {
			if !strings.Contains(out, s) {
				t.Errorf("no %q in:\n%s", s, out)
			}
		}
		if strings.Contains(out, "…") || strings.Contains(out, "~/") {
			t.Errorf("the output is redacted without --redact:\n%s", out)
		}
		if !logContains(func() []string { return logs }, "config "+path+": ignoring unknown settings") {
			t.Errorf("the log of loading the config changed: %q", logs)
		}
		// The same report, only masked.
		red, _ := report(t, true)
		if got, want := strings.Count(red, "\n"), strings.Count(out, "\n"); got != want {
			t.Errorf("the redacted report has %d lines, the plain one %d", got, want)
		}
	})

	// The chats of the config file, read before it loads (addConfig), are
	// masked as written: loading logs what it finds odd that way. No such
	// log names a group_members number today, so only this case needs
	// addConfig to add them; the report masks the loaded ones.
	t.Run("as written", func(t *testing.T) {
		r := newRedactor(home)
		r.addConfig(path)
		for _, s := range []string{owner, otherWritten, oddGroup, allowedGroup, memberOnly} {
			if got := r.text(s); strings.Contains(got, s) {
				t.Errorf("%q, from the config file, is redacted to %q", s, got)
			}
		}
	})

	t.Run("an error", func(t *testing.T) {
		broken := filepath.Join(home, "broken", "config.json")
		makeFiles(t, home, "broken/config.json")
		for _, redact := range []bool{true, false} {
			captureLog(t)
			err := WriteCheck(io.Discard, CheckOptions{Config: broken, Redact: redact})
			if err == nil {
				t.Fatal("a config that is no JSON loaded")
			}
			if got := strings.Contains(err.Error(), home); got == redact {
				t.Errorf("redact=%v: the error names the home folder: %v", redact, got)
			}
		}
	})
}

func TestRedactorText(t *testing.T) {
	// Made-up numbers outside the placeholders' country carry
	// personal-data:ignore.
	r := newRedactor("/srv/first.last")
	r.addChats("15551234567", "+15559876543", "15559876543@s.whatsapp.net")
	r.addGroups("120363000000000001@g.us", "120363000000000009")
	cases := []struct{ in, want string }{
		{"15551234567@s.whatsapp.net", "1…67@s.whatsapp.net"},
		{"15551234567:12@s.whatsapp.net", "1…67@s.whatsapp.net"},           // a device's JID
		{"491701234567@s.whatsapp.net", "49…67@s.whatsapp.net"},            // a two-digit country code; personal-data:ignore
		{"3530000000099@s.whatsapp.net", "353…99@s.whatsapp.net"},          // a three-digit one; personal-data:ignore
		{"120363000000000001@g.us", "…01@g.us"},                            // a group
		{"15551234567-1600000000@g.us", "…00@g.us"},                        // a group named by its creator's number; personal-data:ignore
		{strings.Repeat("7", 15) + "@lid", "…77@lid"},                      // a LID
		{"[15551234567 15559876543]", "[1…67 1…43]"},                       // numbers configured or routed
		{`"120363000000000009" (digits@g.us)`, `"…09" (digits@g.us)`},      // a group written without @g.us
		{"+15559876543 and +4917012345678", "+1…43 and +49…78"},            // international format; personal-data:ignore
		{"you@example.com", "y…@example.com"},                              // as GoogleMaskEmail
		{"/srv/first.last/.fridolin", "~/.fridolin"},                       // a path
		{`(subpath "/srv/first.last")`, `(subpath "~")`},                   // the folder itself
		{`(regex "^/srv/first\\.last/x(/.*)?$")`, `(regex "^~/x(/.*)?$")`}, // in a sandbox profile's regular expression
		{"/srv/first.lastname/x", "/srv/first.lastname/x"},                 // another folder
		{"127.0.0.1:8765 and 2.1.400", "127.0.0.1:8765 and 2.1.400"},       // what is no number
		{"12345@g.us", "…@g.us"},                                           // too short to keep any digit
		{"allowed_groups (digits@g.us)", "allowed_groups (digits@g.us)"},   // no JID, and no address either
		{"brain-120363000000000005@g.us", "brain-…05@g.us"},                // a JID right after a name
		{"main.120363000000000005@g.us", "main.…05@g.us"},                  // or a word
		{"x15550001111@s.whatsapp.net", "x1…11@s.whatsapp.net"},            // a number's; personal-data:ignore
		{"x" + strings.Repeat("6", 15) + "@lid", "x…66@lid"},               // a LID's
	}
	for _, c := range cases {
		if got := r.text(c.in); got != c.want {
			t.Errorf("text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := newRedactor("/").text("/usr/bin"); got != "/usr/bin" {
		t.Errorf("a home of / redacted %q", got)
	}
}

func TestCheckDisabledDashboardAndUnknownSandboxBrain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeConfig(t, `{"http_listen":"off","claude_path":"`+fakeClaudeEnv(t, 0)+`","data_dir":"`+home+`/data"}`)
	var out bytes.Buffer
	err := WriteCheck(&out, CheckOptions{Config: path, SandboxFor: "missing"})
	if err == nil || !strings.Contains(err.Error(), "unknown or unavailable brain") {
		t.Fatalf("unknown brain: %v", err)
	}
	if !strings.Contains(out.String(), "dashboard: disabled") || strings.Contains(out.String(), "http://off") {
		t.Fatalf("report: %s", out.String())
	}
}

func TestCheckUnavailableBrainsDoesNotSuggestEmptySetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := resolvedSettings(t, &config.File{ClaudePath: fakeClaudeEnv(t, 0), DataDir: filepath.Join(home, "data"), BrainDirs: []config.BrainRef{{Path: filepath.Join(home, "missing")}}})
	var out bytes.Buffer
	_ = CheckReport(&out, "config.json", cfg, CheckOptions{})
	if !strings.Contains(out.String(), "none available") || strings.Contains(out.String(), "none yet") {
		t.Fatalf("report: %s", out.String())
	}
}
