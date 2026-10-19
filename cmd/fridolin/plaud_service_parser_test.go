package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

func loadServicePlaudGolden(t *testing.T) map[string]struct {
	Stdout string `json:"stdout"`
} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pkgDir, "testdata", "plaud", "cli-0.3.15.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}
func writeServicePlaudRecordings(t *testing.T, dir string, list []string, recs []testutil.PlaudRecording) {
	t.Helper()
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(dir, "list.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		data, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(dir, "rec_"+r.ID+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Only the id column of `plaud files` holds ids: a recording named like
// one does not add a recording.
func TestPlaudListedIDsComeFromTheIDColumn(t *testing.T) {
	golden := loadServicePlaudGolden(t)["files"].Stdout
	if got := plaudListedIDs(golden); !slices.Equal(got, []string{servicePlaudNew, servicePlaudOld2, servicePlaudOld1}) {
		t.Errorf("ids from the recorded listing = %v", got)
	}
	dir := t.TempDir()
	r := testutil.PlaudRecording{ID: servicePlaudNew, Name: "of_00000000000000000000ff copy", CreatedAt: "2026-10-16T18:00:00", Duration: 60000}
	writeServicePlaudRecordings(t, dir, []string{servicePlaudNew}, []testutil.PlaudRecording{r})
	home := t.TempDir()
	_ = os.MkdirAll(filepath.Join(home, ".plaud"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".plaud", "tokens.json"), []byte("{}"), 0o600)
	cmd := exec.Command(os.Args[0], "files")
	cmd.Env = []string{"FRIDOLIN_FAKE_PLAUD=1", "FRIDOLIN_FAKE_PLAUD_DIR=" + dir, "HOME=" + home, "GORACE=atexit_sleep_ms=0"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := plaudListedIDs(string(out)); !slices.Equal(got, []string{servicePlaudNew}) {
		t.Errorf("ids = %v from:\n%s", got, out)
	}
}

// parseKV leaves out a value the CLI has not got: "-" for a missing
// start_at or serial_number, as in the recorded details, and "undefined"
// or "null" for a field Plaud did not send. The transcript's header then
// falls back ("?" for the duration) rather than print them.
func TestPlaudParseKV(t *testing.T) {
	d := parseKV(loadServicePlaudGolden(t)["file"].Stdout)
	for k, want := range map[string]string{"id": servicePlaudNew, "name": "Team sync", "duration": "5m00s", "transcript": "available", "start_at": "", "serial_number": ""} {
		if d[k] != want {
			t.Errorf("recorded details: %s = %q, want %q", k, d[k], want)
		}
	}
	for _, c := range []struct {
		line, want string
	}{
		{"  duration: 5m00s", "5m00s"},
		{"  duration: -", ""},
		{"  duration: undefined", ""},
		{"  duration: null", ""},
		{"  duration:", ""},
		{"  duration:    ", ""},
	} {
		got, ok := parseKV(c.line)["duration"]
		if got != c.want || ok != (c.want != "") {
			t.Errorf("parseKV(%q) duration = %q (%v), want %q", c.line, got, ok, c.want)
		}
	}
}
