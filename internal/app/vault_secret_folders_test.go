package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// secretTree is a folder's state: each entry's mode, bytes and time, by
// its path below the folder.
type secretTree map[string]string

func readSecretTree(t *testing.T, dir string) secretTree {
	t.Helper()
	out := secretTree{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		v := fi.Mode().String() + " " + fi.ModTime().UTC().Format(time.RFC3339Nano)
		if fi.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += " " + string(data)
		} else if rel == "." {
			v = fi.Mode().String() // the folder's own time moves with what is in it
		}
		out[filepath.ToSlash(rel)] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (a secretTree) diff(b secretTree) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "missing "+k)
		} else if v != w {
			out = append(out, k+": "+v+" ≠ "+w)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "extra "+k)
		}
	}
	slices.Sort(out)
	return out
}

// secretValue is in every secret file of the tests: it must reach no log,
// no status and no byte of the cold tier in the clear.
const secretValue = "test-value-9f3c"

// writeSecretFolders makes the credentials folder and a legacy folder
// with files of several modes, a subfolder and a symbolic link.
func writeSecretFolders(t *testing.T, home string) (creds, legacy string) {
	t.Helper()
	creds, legacy = filepath.Join(home, ".fridolin-secrets"), filepath.Join(home, ".personal-calendar")
	when := time.Date(2026, 10, 1, 8, 30, 15, 123456789, time.UTC)
	for _, f := range []struct {
		path string
		mode fs.FileMode
	}{
		{filepath.Join(creds, "google_oauth_web_credentials.json"), 0o600},
		{filepath.Join(creds, "dashboard-users.json"), 0o640},
		{filepath.Join(creds, "google_calendar_token_main.json"), 0o400},
		{filepath.Join(legacy, "tokens", "token.json"), 0o600},
		{filepath.Join(legacy, "refresh.sh"), 0o755},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(`{"secret":"`+secretValue+`","file":"`+filepath.Base(f.path)+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f.path, f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f.path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(legacy, "tokens"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(legacy, "link")); err != nil {
		t.Fatal(err)
	}
	return creds, legacy
}

func secretRestore(h *harness, req map[string]any) (secretRestoreRecord, int) {
	h.t.Helper()
	w := h.Do("POST", "/api/vault/secret-folders/restore", req)
	var rec secretRestoreRecord
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &rec); err != nil {
			h.t.Fatal(err)
		}
	}
	return rec, w.Code
}

// TestVaultSecretFoldersRoundTrip backs up sandbox.credentials_dir and a
// vault.secret_dirs folder through the stand-in for B2 (a folder, sealed
// as the cold tier), restores them into a new folder with the exact bytes,
// modes and times, and over the live folders with --force after copying
// them aside; the live folders are never written otherwise, and no secret
// reaches the log, the status or the stand-in in the clear.
func TestVaultSecretFoldersRoundTrip(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	setenv(t, vaultLocalEnv, local)
	logs := &lockedBuffer{}
	optionsOf(t).Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := newHarness(t, `{"vault": {"enabled": true, "hot_budget_gb": 3, "secret_dirs": ["~/.personal-calendar"]},
		"sandbox": {"credentials_dir": "~/.fridolin-secrets"}}`)
	creds, legacy := writeSecretFolders(t, h.Home())
	before := map[string]secretTree{creds: readSecretTree(t, creds), legacy: readSecretTree(t, legacy)}
	h.startVault()

	st := h.vaultJob("/api/vault/secret-folders/backup", nil)
	sf := st.SecretFolders
	if sf == nil || !sf.Enabled || sf.LastOK == nil || sf.LastOK.Files != 5 || len(sf.Versions) != 1 || sf.Last.Error != "" {
		data, _ := json.MarshalIndent(sf, "", "  ")
		t.Fatalf("after the backup:\n%s", data)
	}
	if got := []string{sf.Folders[0].Label, sf.Folders[1].Label}; !slices.Equal(got, []string{".fridolin-secrets", ".personal-calendar"}) || sf.Folders[1].Skipped != 1 {
		t.Errorf("folders %+v", sf.Folders)
	}
	version := sf.LastOK.Version
	raw, err := os.ReadFile(filepath.Join(local, secretFoldersPath, version))
	if err != nil || !strings.HasPrefix(string(raw), "FRDSEAL1") || bytes.Contains(raw, []byte(secretValue)) || bytes.Contains(raw, []byte("google_oauth")) {
		t.Fatalf("the version is not sealed: %v %.20q", err, raw)
	}
	// Not in the cold tier, which evicts, nor anywhere else of the stand-in.
	_ = filepath.WalkDir(local, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); bytes.Contains(data, []byte(secretValue)) {
				t.Errorf("%s holds a secret in the clear", p)
			}
		}
		return nil
	})
	enc := slices.IndexFunc(st.Encryption, func(l storeLayers) bool { return l.Store == "secret-folders" })
	if enc < 0 || len(st.Encryption[enc].Layers) != 2 {
		t.Errorf("the encryption status: %+v", st.Encryption)
	}

	// Into a new folder, the default: exact bytes, modes and times.
	rec, code := secretRestore(h, map[string]any{})
	if code != http.StatusOK || rec.Version != version || rec.Files != 5 || !strings.HasPrefix(rec.To, filepath.Join(h.DataDir(), "core", "restored-secrets-")) {
		t.Fatalf("restore: %d %+v", code, rec)
	}
	for _, f := range []struct{ live, label string }{{creds, ".fridolin-secrets"}, {legacy, ".personal-calendar"}} {
		want := maps.Clone(before[f.live])
		delete(want, "link") // links stay out of a version
		if d := want.diff(readSecretTree(t, filepath.Join(rec.To, f.label))); len(d) > 0 {
			t.Errorf("restored %s differs: %q", f.label, d)
		}
	}
	// Never into the live folders or what holds them, nor a full folder.
	if _, code := secretRestore(h, map[string]any{"to": filepath.Join(creds, "restore")}); code != http.StatusUnprocessableEntity {
		t.Errorf("a restore into the live folder: %d", code)
	}
	if _, code := secretRestore(h, map[string]any{"to": h.Home()}); code != http.StatusUnprocessableEntity {
		t.Errorf("a restore into the folder holding the live ones: %d", code)
	}
	if _, code := secretRestore(h, map[string]any{"to": rec.To}); code != http.StatusConflict {
		t.Errorf("a restore into a folder with files in it: %d", code)
	}
	if _, code := secretRestore(h, map[string]any{"at": "2020-01-01"}); code != http.StatusNotFound {
		t.Errorf("a restore of a day before the first version: %d", code)
	}
	if _, code := secretRestore(h, map[string]any{"to": filepath.Join(t.TempDir(), "outside")}); code != http.StatusUnprocessableEntity {
		t.Errorf("a restore outside the home folder and the data dir: %d", code)
	}
	to := filepath.Join(h.Home(), "restored", "new")
	if rec, code := secretRestore(h, map[string]any{"to": to, "at": time.Now().Add(time.Minute).Format(time.RFC3339)}); code != http.StatusOK || rec.To != to || rec.Files != 5 {
		t.Errorf("a restore --to --at: %d %+v", code, rec)
	}
	// The vault read the live folders, and wrote nothing there.
	for dir, tree := range before {
		if d := tree.diff(readSecretTree(t, dir)); len(d) > 0 {
			t.Errorf("the vault changed the live folder %s: %q", dir, d)
		}
	}

	// --force: what it replaces goes aside first, then the version's
	// files go over the live ones; what the version lacks (the new file,
	// the link) stays.
	tokenFile := filepath.Join(creds, "google_calendar_token_main.json")
	if err := os.Chmod(tokenFile, 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, tokenFile, "a broken token")
	writeFile(t, filepath.Join(creds, "new-since.json"), "newer")
	rec, code = secretRestore(h, map[string]any{"force": true})
	if code != http.StatusOK || !rec.Force || rec.Kept != 2 || rec.Replaced == "" {
		t.Fatalf("restore --force: %d %+v", code, rec)
	}
	if got := readSecretTree(t, creds); got["google_calendar_token_main.json"] != before[creds]["google_calendar_token_main.json"] || got["new-since.json"] == "" {
		t.Errorf("after --force: %q", got)
	}
	if data, err := os.ReadFile(filepath.Join(rec.Replaced, ".fridolin-secrets", "google_calendar_token_main.json")); err != nil || string(data) != "a broken token" {
		t.Errorf("the copy of what --force replaced: %q %v", data, err)
	}
	if _, code := secretRestore(h, map[string]any{"force": true, "to": to}); code != http.StatusUnprocessableEntity {
		t.Errorf("--force with --to: %d", code)
	}

	if strings.Contains(logs.String(), secretValue) {
		t.Errorf("the log holds a secret:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "backed up the secret folders") {
		t.Errorf("the log does not say the backup ran:\n%s", logs.String())
	}
	if data, _ := os.ReadFile(filepath.Join(h.DataDir(), "core", vaultStatusFile)); bytes.Contains(data, []byte(secretValue)) || !bytes.Contains(data, []byte(`"secret_folders"`)) {
		t.Errorf("the status file:\n%s", data)
	}
	text, err := VaultStatusText(h.Do("GET", "/api/vault/status", nil).Body.Bytes(), time.Now())
	if err != nil || !strings.Contains(text, "secret folders: to vault bucket, secret-folders-sealed/") || !strings.Contains(text, "last restore: ok") {
		t.Errorf("fridolin vault status:\n%s %v", text, err)
	}
}

// TestVaultSecretFoldersOnChange takes the first version on the vault's
// tick, and another once a change settled.
func TestVaultSecretFoldersOnChange(t *testing.T) {
	t.Parallel()
	local := t.TempDir()
	setenv(t, vaultLocalEnv, local)
	optionsOf(t).waits.vaultTick = 50 * time.Millisecond
	optionsOf(t).waits.secretFoldersChange = time.Millisecond
	h := newHarness(t, `{"vault": {"enabled": true, "hot_budget_gb": 3}, "sandbox": {"credentials_dir": "~/.fridolin-secrets"}}`)
	creds, _ := writeSecretFolders(t, h.Home())
	h.startVault()
	first := h.vaultWait("the first version", func(st vaultStatus) bool {
		return st.SecretFolders != nil && st.SecretFolders.LastOK != nil
	}).SecretFolders.LastOK
	if first.Reason != "first" || first.Files != 3 {
		t.Fatalf("the first version: %+v", first)
	}
	time.Sleep(1100 * time.Millisecond) // versions are named by the second
	writeFile(t, filepath.Join(creds, "google_calendar_token_work.json"), `{"t":1}`)
	st := h.vaultWait("a version after the change", func(st vaultStatus) bool {
		return st.SecretFolders.LastOK.Version != first.Version
	}).SecretFolders
	if st.LastOK.Reason != "changed" || st.LastOK.Files != 4 || len(st.Versions) != 2 {
		t.Errorf("after the change: %+v %+v", st.LastOK, st.Versions)
	}
}

func TestSecretVersionsToDrop(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	var versions []secretVersion
	at := func(d time.Duration) secretVersion {
		t := now.Add(-d)
		return secretVersion{Version: t.Format(secretVersionLayout) + ".tar", At: t}
	}
	for _, d := range []time.Duration{
		time.Hour, 3 * time.Hour, 23 * time.Hour, // the last day: all kept
		40 * time.Hour, 41 * time.Hour, // one day: the newer kept
		10 * 24 * time.Hour,
		40 * 24 * time.Hour, 41 * 24 * time.Hour, // older than 30 days: dropped
	} {
		versions = append(versions, at(d))
	}
	var dropped []time.Time
	for _, v := range secretVersionsToDrop(versions, now, 30) {
		dropped = append(dropped, v.At)
	}
	want := []time.Time{now.Add(-41 * time.Hour), now.Add(-40 * 24 * time.Hour), now.Add(-41 * 24 * time.Hour)}
	if !slices.EqualFunc(dropped, want, time.Time.Equal) {
		t.Errorf("dropped %v, want %v", dropped, want)
	}
	// The newest is kept however old.
	if d := secretVersionsToDrop([]secretVersion{at(90 * 24 * time.Hour), at(100 * 24 * time.Hour)}, now, 30); len(d) != 1 || !d[0].At.Equal(now.Add(-100*24*time.Hour)) {
		t.Errorf("dropped %v", d)
	}
}

func TestParseSecretAt(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("CET", 3600)
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"", time.Time{}},
		{"2026-10-20", time.Date(2026, 10, 21, 0, 0, 0, 0, loc).Add(-time.Nanosecond)},
		{"2026-10-20T14:30", time.Date(2026, 10, 20, 14, 30, 0, 0, loc)},
		{"2026-10-20T14:30:00Z", time.Date(2026, 10, 20, 14, 30, 0, 0, time.UTC)},
	} {
		got, err := parseSecretAt(tc.in, loc)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("%q: %v %v, want %v", tc.in, got, err, tc.want)
		}
	}
	if _, err := parseSecretAt("yesterday", loc); err == nil {
		t.Error("yesterday was taken")
	}
}

// The secret folders are neither vault files nor in a snapshot: the cold
// tier evicts, and the snapshots go to Drive and node targets too.
func TestSecretFoldersStayOutOfTheColdTierAndSnapshots(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cfg := &config.Settings{}
	cfg.DataDir = filepath.Join(home, ".fridolin")
	cfg.MediaDir = filepath.Join(cfg.DataDir, "media")
	cfg.Sandbox.CredentialsDir = filepath.Join(cfg.DataDir, "secrets", "google")
	cfg.Vault.SecretDirs = []string{filepath.Join(home, ".personal-calendar")}
	l := vaultLayoutOf(cfg, nil, nil)
	if !l.ignore("secrets/google/token.json") || !l.ignore("secrets/google") || l.ignore("secrets/calendar.age") {
		t.Errorf("the layout: secrets/google %v, secrets/calendar.age %v", l.ignore("secrets/google/token.json"), l.ignore("secrets/calendar.age"))
	}
	ex := l.secretExcludes()
	if !slices.Contains(ex, cfg.Sandbox.CredentialsDir) || !slices.Contains(ex, cfg.Vault.SecretDirs[0]) {
		t.Errorf("the snapshots' excludes: %q", ex)
	}
	if ex := dataDirExcludes(cfg.DataDir, nil); !slices.Contains(ex, filepath.Join(cfg.DataDir, "core", "restored-secrets-*")) {
		t.Errorf("the data dir's excludes: %q", ex)
	}
}

func TestSecretFoldersOf(t *testing.T) {
	t.Parallel()
	cfg := &config.Settings{}
	cfg.Sandbox.CredentialsDir = "/home/you/a/secrets"
	cfg.Vault.SecretDirs = []string{"/home/you/b/secrets", "/home/you/a/secrets"}
	got, err := secretFoldersOf(cfg)
	if err != nil || len(got) != 2 || got[0].Label != "secrets" || got[1].Label != "secrets-2" || got[1].Source != "vault.secret_dirs" {
		t.Errorf("%+v %v", got, err)
	}
	cfg.Vault.SecretDirs = []string{"/home/you/a/secrets/inner"}
	if _, err := secretFoldersOf(cfg); err == nil || !strings.Contains(err.Error(), "one inside the other") {
		t.Errorf("nested folders: %v", err)
	}
	// The data dir is no secret folder: leaving it out of the cold tier
	// and the snapshots would leave out everything.
	cfg.DataDir, cfg.Sandbox.CredentialsDir, cfg.Vault.SecretDirs = "/home/you/.fridolin", "/home/you/.fridolin", []string{"/home/you"}
	if _, err := secretFoldersOf(cfg); err == nil || !strings.Contains(err.Error(), "holds the data dir") {
		t.Errorf("the data dir as a secret folder: %v", err)
	}
	if p := secretFolderPaths(cfg); len(p) != 0 {
		t.Errorf("the data dir is left out: %q", p)
	}
	if l := vaultLayoutOf(cfg, nil, nil); l.ignore("secrets/calendar.age") {
		t.Error("the cold tier lost its files to a credentials_dir set to the data dir")
	}
}

func TestDoctorVaultSecretFolders(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ok := &secretBackupRecord{At: now.Add(-3 * time.Hour), Version: "20261020T090000Z.tar", Reason: "daily", Files: 4}
	for _, tc := range []struct {
		name   string
		sf     *secretFoldersStatus
		status string
		detail string
	}{
		{"fresh", &secretFoldersStatus{Enabled: true, LastOK: ok, Last: ok, Folders: make([]secretFolderView, 2)}, DoctorOK, "2 folders, 4 files"},
		{"never", &secretFoldersStatus{Enabled: true, Last: &secretBackupRecord{At: now, Error: "stat: no such file or directory"}}, DoctorWarn, "no backup of the secret folders yet: stat"},
		{"old", &secretFoldersStatus{Enabled: true, LastOK: &secretBackupRecord{At: now.Add(-50 * time.Hour)}, Last: &secretBackupRecord{At: now.Add(-50 * time.Hour)}}, DoctorWarn, "is 50h0m0s old"},
		{"last failed", &secretFoldersStatus{Enabled: true, LastOK: ok, Last: &secretBackupRecord{At: now, Error: "403"}}, DoctorWarn, "the last try failed: 403"},
		{"no store", &secretFoldersStatus{Error: errSecretFoldersOneLayer}, DoctorWarn, "both layers"},
		{"not looked at yet", nil, DoctorWarn, "not looked at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := healthyMac()
			st := vaultStatus{Enabled: true, State: vaultRunning, UpdatedAt: now.Add(-time.Minute), SecretFolders: tc.sf}
			data, _ := json.Marshal(st)
			s.contents = map[string]string{"/home/you/.fridolin/core/vault-status.json": string(data)}
			env := s.doctorEnv(doctorConfig(func(c *config.Settings) {
				c.Vault.Enabled, c.Sandbox.CredentialsDir = true, "/home/you/.fridolin-secrets"
			}), nil)
			env.Now = func() time.Time { return now }
			got := result(t, RunDoctor(context.Background(), env, DoctorRegistry), "vault-secret-folders")
			if got.Status != tc.status || !strings.Contains(got.Detail, tc.detail) {
				t.Errorf("%+v, want %s with %q", got, tc.status, tc.detail)
			}
		})
	}
	// Without a secret folder the check does not apply.
	env := healthyMac().doctorEnv(doctorConfig(func(c *config.Settings) { c.Vault.Enabled = true }), nil)
	for _, c := range RunDoctor(context.Background(), env, DoctorRegistry).Checks {
		if c.ID == "vault-secret-folders" {
			t.Error("the check applies without a secret folder")
		}
	}
}
