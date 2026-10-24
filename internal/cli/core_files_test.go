package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// core files export and import, as a core host's agent runs them in the
// core's container in a move (#187): the folder is config's
// sandbox.credentials_dir, which goes along under ~.
func TestCoreFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"google_calendar_token_home.json": `{"t":1}`, "restore.sh": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(t.TempDir(), "config.json")
	// Written by hand as ~/creds; loaded and saved by the bridge, expanded.
	if err := os.WriteFile(cfg, []byte(`{"sandbox": {"credentials_dir": "`+dir+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runCore([]string{"files", "export"}, coreEnv{environ: []string{"HOME=" + home, "FRIDOLIN_CONFIG=" + cfg}, stdout: &out, stderr: io.Discard}); code != 0 {
		t.Fatalf("export: exit %d", code)
	}
	var got secretFiles
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Dir != "~/creds" || len(got.Files) != 1 || string(got.Files["google_calendar_token_home.json"]) != `{"t":1}` {
		t.Fatalf("export: %s, %v", out.String(), err)
	}

	// Import into another home: what is there stays, the rest is written,
	// into the folder named under ~.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "creds"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "creds", "dashboard-users.json"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	got.Files["dashboard-users.json"] = []byte("theirs")
	in, _ := json.Marshal(got)
	out.Reset()
	if code := runCore([]string{"files", "import"}, coreEnv{environ: []string{"HOME=" + other}, stdin: bytes.NewReader(in), stdout: &out, stderr: io.Discard}); code != 0 {
		t.Fatalf("import: exit %d", code)
	}
	var res secretFilesResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || !slices.Equal(res.Stored, []string{"google_calendar_token_home.json"}) || !slices.Equal(res.Kept, []string{"dashboard-users.json"}) {
		t.Fatalf("import: %s, %v", out.String(), err)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "creds", "dashboard-users.json")); string(b) != "mine" {
		t.Fatalf("a file there was replaced: %s", b)
	}

	// No credentials_dir, or one in the data dir: no files (the export
	// carries those).
	if err := os.WriteFile(cfg, []byte(`{"sandbox": {"credentials_dir": "/data/creds"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runCore([]string{"files", "export"}, coreEnv{environ: []string{"HOME=" + home, "FRIDOLIN_CONFIG=" + cfg}, stdout: &out, stderr: io.Discard}); code != 0 || !strings.Contains(out.String(), `"dir":""`) {
		t.Errorf("credentials in the data dir: %d %s", code, out.String())
	}
	// A folder outside ~ is refused on import; a bad call says how.
	bad, _ := json.Marshal(secretFiles{Dir: "~/../etc", Files: map[string][]byte{"dashboard-users.json": []byte("x")}})
	if code := runCore([]string{"files", "import"}, coreEnv{environ: []string{"HOME=" + other}, stdin: bytes.NewReader(bad), stdout: io.Discard, stderr: io.Discard}); code != 2 {
		t.Errorf("a folder outside ~: exit %d", code)
	}
	var errb bytes.Buffer
	if code := runCore([]string{"files"}, coreEnv{stdout: io.Discard, stderr: &errb}); code != 2 || !strings.Contains(errb.String(), "usage") {
		t.Errorf("no subcommand: %d %s", code, errb.String())
	}
	if code := runCore([]string{"files", "import"}, coreEnv{environ: []string{"HOME=" + other}, stdin: strings.NewReader("not json"), stdout: io.Discard, stderr: io.Discard}); code != 2 {
		t.Errorf("bad input: %d", code)
	}
}
