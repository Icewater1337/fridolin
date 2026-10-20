package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
	"github.com/Icewater1337/fridolin/internal/vault"
)

// vaultConfig turns the vault on with a small budget.
const vaultConfig = `{"vault": {"enabled": true, "hot_budget_gb": 3}}`

// startVault starts the harness's vault, as App.Start does, with the cold
// tier, the restic repositories and Litestream in local folders under
// local (FRIDOLIN_VAULT_LOCAL), and waits until it runs.
func (h *harness) startVault() {
	h.t.Helper()
	h.b.vault.Start(context.Background())
	h.vaultWait("the vault runs", func(st vaultStatus) bool {
		if st.State == vaultError {
			h.t.Fatalf("the vault failed: %s", st.Error)
		}
		return st.State == vaultRunning
	})
}

// vaultStatus is GET /api/vault/status.
func (h *harness) vaultStatus() vaultStatus {
	h.t.Helper()
	w := h.Do("GET", "/api/vault/status", nil)
	if w.Code != http.StatusOK {
		h.t.Fatalf("GET /api/vault/status: %d %s", w.Code, w.Body.String())
	}
	var st vaultStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) vaultWait(what string, cond func(vaultStatus) bool) vaultStatus {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		st := h.vaultStatus()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			data, _ := json.MarshalIndent(st, "", "  ")
			h.t.Fatalf("waiting for %s: status\n%s", what, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// vaultJob starts a vault job through the API and waits until it is over.
func (h *harness) vaultJob(path string, body any) vaultStatus {
	h.t.Helper()
	w := h.Do("POST", path, body)
	if w.Code != http.StatusAccepted {
		h.t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
	}
	return h.vaultWait("POST "+path+" to end", func(st vaultStatus) bool { return st.Job == "" })
}

func writeSized(t *testing.T, path string, size int64) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i * 7)
	}
	for written := int64(0); written < size; written += int64(len(chunk)) {
		chunk[0]++
		if _, err := io.MultiWriter(f, h).Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestVaultGate is M3's gate (docs/PLAN.md): a large recording goes cold
// once its transcript exists, while the transcript stays hot; vault_get
// brings it back for the brain's run; a fresh data dir rebuilds its hot
// tier from the cold tier within the budget, and answers. The recording
// is 64 MB here; the 2 GB case is a manual step (docs/VAULT.md),
// FRIDOLIN_GATE_RECORDING_MB=2048.
func TestVaultGate(t *testing.T) {
	local := t.TempDir()
	t.Setenv(vaultLocalEnv, local)
	t.Setenv("FRIDOLIN_FAKE_MCP", `[{"tool":"vault_get","args":{"path":"media/main/recording.m4a"}}]`)
	h := newServedHarness(t, vaultConfig)
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	data := h.DataDir()
	inbox := filepath.Join(data, "media", "main")
	rec := filepath.Join(inbox, "recording.m4a")
	size := int64(64)
	if mb, err := strconv.Atoi(os.Getenv("FRIDOLIN_GATE_RECORDING_MB")); err == nil && mb > 0 {
		size = int64(mb) // the 2 GB run by hand: FRIDOLIN_GATE_RECORDING_MB=2048
	}
	sum := writeSized(t, rec, size<<20)
	writeFile(t, filepath.Join(data, "secrets", "calendar.age"), "age-encrypted")
	month := time.Now().Format("2006-01")
	writeFile(t, filepath.Join(data, "audit", month+".jsonl"), `{"kind":"test"}`+"\n")
	writeFile(t, filepath.Join(data, "vault", "documents", "main", "lease.pdf"), "%PDF-1.7 a lease")
	// Not vault files: the brains' own state and the dashboard's token.
	writeFile(t, filepath.Join(data, "brains", "main", "tmp", "scratch.bin"), "scratch")

	h.startVault()
	st := h.vaultWait("the first upload", func(st vaultStatus) bool { return st.Files.Both >= 4 && st.LastSync != nil })
	if st.Files.Hot != 0 {
		t.Errorf("files still waiting for their upload: %+v", st.Files)
	}
	cold, err := os.ReadDir(filepath.Join(local, "cold"))
	if err != nil {
		t.Fatal(err)
	}
	var top []string
	for _, e := range cold {
		if !strings.HasPrefix(e.Name(), ".cloud") { // the fake's own staging
			top = append(top, e.Name())
		}
	}
	if strings.Join(top, ",") != ".fridolin-vault,audit,media,secrets,vault" {
		t.Errorf("the cold tier's top folders are %v", top)
	}

	// Without a transcript the recording stays hot: markdown-first.
	h.vaultJob("/api/vault/evict", nil)
	if _, err := os.Stat(rec); err != nil {
		t.Fatalf("the recording went cold before its transcript existed: %v", err)
	}

	// The transcript exists: the recording goes cold, the transcript stays.
	transcript := filepath.Join(inbox, "recording.transcript.md")
	writeFile(t, transcript, "# Recording\n\nWhat was said.\n")
	h.vaultJob("/api/vault/sync", nil)
	st = h.vaultJob("/api/vault/evict", nil)
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Fatalf("the recording is still hot after its transcript came: %v", err)
	}
	if _, err := os.Stat(transcript); err != nil {
		t.Fatalf("the transcript went cold: %v", err)
	}
	if len(st.Evictions) != 1 || st.Evictions[0].Path != "media/main/recording.m4a" || st.Evictions[0].Reason != vault.ReasonTranscribed {
		t.Errorf("eviction log: %+v", st.Evictions)
	}
	listing, err := os.ReadFile(filepath.Join(inbox, ".cold.json"))
	if err != nil || !strings.Contains(string(listing), `"recording.m4a"`) {
		t.Fatalf(".cold.json does not list the recording: %v %s", err, listing)
	}

	// The brain's run brings it back with vault_get, and reads it where it
	// is: in its own inbox.
	reply := h.Answer(inbound{JID: harnessDM, Text: "Play the recording #mcp"})
	if !strings.Contains(reply, "[main:vault_get] {") {
		t.Fatalf("vault_get in the run:\n%s", reply)
	}
	if got := fileSHA(t, rec); got != sum {
		t.Fatalf("the recording came back with sha256 %s, want %s", got, sum)
	}
	h.Shutdown()

	// A fresh core: an empty data dir, the same cold tier. Rebuild brings
	// back the pinned files within the budget; the rest stays cold until
	// asked for, and it answers.
	h2 := newServedHarness(t, vaultConfig)
	h2.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	h2.startVault()
	st = h2.vaultJob("/api/vault/rebuild", nil)
	data2 := h2.DataDir()
	for _, p := range []string{filepath.Join("secrets", "calendar.age"), filepath.Join("audit", month+".jsonl")} {
		if _, err := os.Stat(filepath.Join(data2, p)); err != nil {
			t.Errorf("pinned %s did not come back: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(data2, "media", "main", "recording.m4a")); !os.IsNotExist(err) {
		t.Errorf("the recording came back without being asked for: %v", err)
	}
	if st.HotBytes > st.TargetBytes || st.Files.Cold < 3 {
		t.Errorf("after the rebuild: hot %d of target %d, files %+v", st.HotBytes, st.TargetBytes, st.Files)
	}
	listing, err = os.ReadFile(filepath.Join(data2, "media", "main", ".cold.json"))
	if err != nil || !strings.Contains(string(listing), `"recording.m4a"`) {
		t.Errorf("the fresh core's .cold.json does not list the recording: %v %s", err, listing)
	}
	// fridolin vault get, as the person asks for it.
	w := h2.Do("POST", "/api/vault/get", map[string]any{"path": "media/main/recording.m4a"})
	if w.Code != http.StatusOK || fileSHA(t, filepath.Join(data2, "media", "main", "recording.m4a")) != sum {
		t.Fatalf("POST /api/vault/get: %d %s", w.Code, w.Body.String())
	}
	if reply := h2.Answer(inbound{JID: harnessDM, Text: "hello"}); !strings.Contains(reply, "hello") {
		t.Errorf("the fresh core's answer: %q", reply)
	}
}

// TestVaultToolsScope: a brain's vault tools reach its own paths; the
// core brain reaches a sub-brain's only with a files grant, and gets a
// copy it can read in its temp folder; nobody gets the data dir's other
// files.
func TestVaultToolsScope(t *testing.T) {
	t.Setenv(vaultLocalEnv, t.TempDir())
	h := coreHarness(t, []testutil.FakeMCPStep{
		{Tool: ""},
		{Brain: "core", Tool: "vault_get", Args: json.RawMessage(`{"path":"vault/documents/family/school.pdf"}`)},
		{Brain: "core", Tool: "vault_get", Args: json.RawMessage(`{"path":"secrets/calendar.age"}`)},
		{Brain: "core", Tool: "vault_put", Args: json.RawMessage(`{"path":"vault/documents/core/note.md","text":"remember this"}`)},
		{Brain: "core", Tool: "vault_put", Args: json.RawMessage(`{"path":"vault/documents/core/stolen","from":"/etc/hosts"}`)},
		{Brain: "core", Tool: "vault_search", Args: json.RawMessage(`{"query":"school"}`)},
	})
	h.b.cfgs.Update("", func(f *config.File) error { f.Vault.Enabled = true; return nil })
	data := h.DataDir()
	writeFile(t, filepath.Join(data, "vault", "documents", "family", "school.pdf"), "%PDF school")
	writeFile(t, filepath.Join(data, "secrets", "calendar.age"), "secret")
	h.startVault()
	h.vaultWait("the upload", func(st vaultStatus) bool { return st.Files.Both >= 2 })

	reply := h.Answer(inbound{JID: harnessDM, Text: "the school letter? #mcp"})
	if !strings.Contains(reply, "[core:list] read_memory,vault_ls,vault_get,vault_put,vault_search,ask_user,request_approval") {
		t.Errorf("the core's tools:\n%s", reply)
	}
	for _, want := range []string{
		"[core:vault_get failed] vault: path outside this brain's vault paths: vault/documents/family/school.pdf",
		"[core:vault_get failed] vault: path outside this brain's vault paths: secrets/calendar.age",
		`"path": "vault/documents/core/note.md"`,
		"[core:vault_put failed] from must be a file in your folder",
		`[core:vault_search] {`,
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("without a files grant, the reply lacks %q:\n%s", want, reply)
		}
	}
	if strings.Contains(reply, "school.pdf\"") && strings.Contains(reply, "[core:vault_search] {\n  \"files\": [\n    {") {
		t.Errorf("vault_search found another brain's file without a grant:\n%s", reply)
	}

	h.grant(map[string][]string{"family": {"files"}})
	reply = h.Answer(inbound{JID: harnessDM, Text: "the school letter? #mcp"})
	copyPath := filepath.Join(data, "brains", "core", "tmp", "vault", "vault", "documents", "family", "school.pdf")
	if !strings.Contains(reply, copyPath) {
		t.Fatalf("with the grant, vault_get did not hand the core a readable copy:\n%s", reply)
	}
	if got, err := os.ReadFile(copyPath); err != nil || string(got) != "%PDF school" {
		t.Errorf("the copy: %q %v", got, err)
	}
	if !strings.Contains(reply, "[core:vault_get failed] vault: path outside this brain's vault paths: secrets/calendar.age") {
		t.Errorf("a files grant opened the secrets:\n%s", reply)
	}

	// vault_put writes (M5): a lockdown refuses it, reading goes on.
	if _, err := h.b.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	reply = h.Answer(inbound{JID: harnessDM, Text: "the school letter? #mcp"})
	if !strings.Contains(reply, "[core:vault_put failed] the bridge is in lockdown") || !strings.Contains(reply, copyPath) {
		t.Errorf("in a lockdown:\n%s", reply)
	}
}

// TestVaultSnapshotsDrillAndDrive runs the snapshots with the real restic
// against local repositories: a nightly and an hourly snapshot on the
// schedule, the checks, the weekly copy to the Drive repository (which
// retires the old Drive backup's daily run), the mass-deletion hold and a
// restore drill that finds the brain folder and sound database copies.
func TestVaultSnapshotsDrillAndDrive(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil && findVaultTool("restic") == "" {
		t.Skip("restic is not installed")
	}
	if testing.Short() {
		t.Skip("runs restic")
	}
	local := t.TempDir()
	t.Setenv(vaultLocalEnv, local)
	t.Setenv("RESTIC_PASSWORD", "test-password")
	oldTick := vaultTickEvery
	vaultTickEvery = 100 * time.Millisecond
	t.Cleanup(func() { vaultTickEvery = oldTick })
	h := newHarness(t, vaultConfig)
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	brainDir := filepath.Join(h.Home(), "main")
	for i := range 40 {
		writeFile(t, filepath.Join(brainDir, "memories", "notes", "n"+string(rune('a'+i%26))+string(rune('a'+i/26))+".md"), "note")
	}
	h.startVault()
	st := h.vaultWait("the scheduled snapshots, checks and Drive copy", func(st vaultStatus) bool {
		sn := st.Snapshots
		return sn.LastNightly != nil && sn.LastHourly != nil && sn.LastReadData != nil && sn.LastCheck != nil && sn.Drive.LastCopy != nil
	})
	sn := st.Snapshots
	for _, r := range []*snapshotRecord{sn.LastNightly, sn.LastHourly} {
		if r.ID == "" || r.Error != "" || r.Files == 0 {
			t.Errorf("snapshot %+v", r)
		}
	}
	if !sn.LastReadData.OK || sn.LastReadData.ReadDataSubset != "5%" || sn.LastPrune == nil || sn.LastPrune.Error != "" {
		t.Errorf("checks and prune: %+v %+v %+v", sn.LastCheck, sn.LastReadData, sn.LastPrune)
	}
	if sn.Drive.LastCopy.Error != "" || !sn.Drive.CompletedOnce {
		t.Fatalf("the copy to Drive: %+v", sn.Drive)
	}
	if !h.b.backup.Summary().Superseded {
		t.Error("the old Drive backup's daily run goes on after the restic copy completed")
	}

	// Most of the brain's notes go: the next nightly snapshot holds
	// pruning until it is allowed.
	if err := os.RemoveAll(filepath.Join(brainDir, "memories")); err != nil {
		t.Fatal(err)
	}
	st = h.vaultJob("/api/vault/snapshot", map[string]string{"kind": "nightly"})
	if st.Snapshots.DeletionHold == nil {
		t.Fatalf("no deletion hold after the notes went: %+v", st.Snapshots.LastNightly)
	}
	if w := h.Do("POST", "/api/vault/prune/allow", map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("allowing the prune: %d %s", w.Code, w.Body.String())
	}
	if h.vaultStatus().Snapshots.DeletionHold != nil {
		t.Error("the hold stayed after it was allowed")
	}

	st = h.vaultJob("/api/vault/restore-drill", nil)
	d := st.LastDrill
	if d == nil || !d.OK {
		data, _ := json.MarshalIndent(st.Litestream, "", "  ")
		t.Logf("litestream: %s", data)
		_ = filepath.Walk(filepath.Join(local, "litestream"), func(p string, fi os.FileInfo, err error) error { t.Log(p); return nil })
		data, _ = json.MarshalIndent(d, "", "  ")
		t.Fatalf("restore drill:\n%s", data)
	}
	names := map[string]bool{}
	for _, c := range d.Checks {
		names[c.Name] = true
	}
	for _, want := range []string{"brain folder main", "database copy bridge.db", "database copy vault.db"} {
		if !names[want] {
			t.Errorf("the drill did not check %s: %+v", want, d.Checks)
		}
	}
}

func TestVaultLayout(t *testing.T) {
	cfg := &config.Settings{}
	cfg.DataDir = "/data"
	cfg.MediaDir = "/data/media"
	l := vaultLayoutOf(cfg, map[string]string{"main": "/data/brains-folders/main", "work": "/Users/you/brains/work", "bad": "/data/brains/bad"})
	for rel, ignored := range map[string]bool{
		"config.json": false, "secrets": false, "secrets/a.age": false, "audit/2026-10.jsonl": false,
		"vault/documents/main/x.pdf": false, "media/main/rec.m4a": false,
		"brains-folders": false, "brains-folders/main/memories/a.md": false, "brains-folders/other/x": true,
		"brains-folders/main/node_modules/x.js": true, "brains-folders/main/.git/HEAD": true,
		"brains-folders/main/claude-stream.log": true, "brains/bad/brain.json": true,
		"dashboard.token": true, "scripts.token": true, "rclone/rclone.conf": true, "run-tokens/main/x": true,
		"bridge.lock": true, "core/vault-status.json": true, "tsnet/x": true, "media/main/x.lock": true,
	} {
		if got := l.ignore(rel); got != ignored {
			t.Errorf("ignore(%q) = %v, want %v", rel, got, ignored)
		}
	}
	if got := strings.Join(l.brainPrefixes("main"), ","); got != "vault/documents/main,media/main,brains-folders/main" {
		t.Errorf("main's vault paths: %s", got)
	}
	if got := strings.Join(l.brainPrefixes("work"), ","); got != "vault/documents/work,media/work" {
		t.Errorf("work's vault paths: %s", got)
	}
}
