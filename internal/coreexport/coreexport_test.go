package coreexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/Icewater1337/fridolin/internal/store"
)

type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (s *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objs[key]
	if !ok {
		return nil, &fs.PathError{Op: "get", Path: key, Err: fs.ErrNotExist}
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *memStore) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs[key] = b
	return nil
}

type sqliteDB struct{}

func (sqliteDB) VacuumInto(ctx context.Context, src, dst string) error {
	return store.VacuumInto(ctx, src, dst)
}
func (sqliteDB) IntegrityCheck(ctx context.Context, p string) error {
	return store.IntegrityCheck(ctx, p)
}

func write(t *testing.T, root, rel, content string, mode fs.FileMode) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// liveDB makes a SQLite database at path and leaves it open in WAL mode,
// as the running bridge keeps bridge.db.
func liveDB(t *testing.T, path, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES (?)", value); err != nil {
		t.Fatal(err)
	}
}

func dataDir(t *testing.T) string {
	dir := t.TempDir()
	write(t, dir, "config.json", `{"data_dir": "~/.fridolin", "brain_dirs": ["~/brains"]}`, 0o600)
	write(t, dir, "dashboard.token", "secret-token", 0o600)
	write(t, dir, "scripts.token", "script-token", 0o600)
	write(t, dir, "secrets/b2.age", "ciphertext", 0o600)
	write(t, dir, "audit/2026-10-20.jsonl", "{}\n", 0o600)
	write(t, dir, "brains/family/claude/settings.json", "{}", 0o600)
	write(t, dir, "brains/family/tmp/scratch", "x", 0o600)
	write(t, dir, "brains/family/cache/blob", "x", 0o600)
	write(t, dir, "brains-folders/family/CLAUDE.md", "# family", 0o644)
	write(t, dir, "brains-folders/family/node_modules/x/index.js", "x", 0o644)
	write(t, dir, "google/oauth_client.json", "{}", 0o600)
	write(t, dir, "otp/family.json", "{}", 0o600)
	write(t, dir, "rclone/rclone.conf", "[drive]", 0o600)
	write(t, dir, "tsnet/tailscaled.state", "state", 0o600)
	write(t, dir, "nodes/registry.json", "{}", 0o600)
	write(t, dir, "runs/123.json", "{}", 0o600)
	write(t, dir, "run-tokens/family/run-1.token", "t", 0o600)
	write(t, dir, "media/family/photo.jpg", "jpg", 0o600)
	write(t, dir, "vault/documents/big.pdf", "pdf", 0o600)
	write(t, dir, "core/device-id", "0123456789abcdef0123456789abcdef", 0o600)
	write(t, dir, "bridge.lock", "", 0o600)
	write(t, dir, "config.json.tmp", "half", 0o600)
	liveDB(t, filepath.Join(dir, "bridge.db"), "whatsapp-session")
	liveDB(t, filepath.Join(dir, "vault.db"), "index")
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func identity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var t0 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func TestExportImportRoundTrip(t *testing.T) {
	src := dataDir(t)
	st := newMemStore()
	id := identity(t)
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }
	res, err := Export(context.Background(), src, []age.Recipient{id.Recipient()}, st, ExportOptions{
		DB: sqliteDB{}, Version: "v1.2.3", Hostname: "mac-mini", Now: func() time.Time { return t0 },
		BrainFolders: []string{filepath.Join(src, "brains-folders", "family"), "/Users/you/work-brain"}, Logf: logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "core/exports/20261020T120000Z.tar.gz.age" {
		t.Errorf("key %q", res.Key)
	}
	if got := strings.TrimSpace(string(st.objs[LatestKey])); got != res.Key {
		t.Errorf("latest points at %q", got)
	}
	if bytes.Contains(st.objs[res.Key], []byte("secret-token")) || bytes.Contains(st.objs[res.Key], []byte("whatsapp-session")) {
		t.Error("the export is not encrypted")
	}
	m := res.Manifest
	if !slices.Equal(m.Databases, []string{"bridge.db", "vault.db"}) {
		t.Errorf("databases %v", m.Databases)
	}
	if !slices.Equal(m.OutsideBrainFolders, []string{"/Users/you/work-brain"}) {
		t.Errorf("outside brain folders %v", m.OutsideBrainFolders)
	}
	if !slices.Equal(m.Skipped, []string{"link"}) {
		t.Errorf("skipped %v", m.Skipped)
	}

	target := t.TempDir()
	staging := filepath.Join(target, "core", "import", "1")
	imp, err := Import(context.Background(), st, []age.Identity{id}, staging, ImportOptions{DB: sqliteDB{}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Key != res.Key || imp.Manifest.Files != m.Files {
		t.Errorf("imported %+v", imp)
	}
	want := []string{
		"audit/2026-10-20.jsonl", "brains-folders/family/CLAUDE.md", "brains/family/claude/settings.json",
		"bridge.db", "config.json", "dashboard.token", "google/oauth_client.json", "nodes/registry.json",
		"otp/family.json", "rclone/rclone.conf", "scripts.token", "secrets/b2.age", "tsnet/tailscaled.state", "vault.db",
	}
	if got := listFiles(t, staging); !slices.Equal(got, want) {
		t.Errorf("staged\n %v\nwant\n %v", got, want)
	}
	if st, err := os.Stat(filepath.Join(staging, "secrets", "b2.age")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("secret's mode: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Join(staging, "brains-folders", "family", "CLAUDE.md")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("a 0644 file must come back owner-only: %v %v", st, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(staging, "bridge.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "whatsapp-session" {
		t.Errorf("bridge.db holds %q, %v", v, err)
	}

	unlocked := false
	lock := func(string) (func(), error) { return func() { unlocked = true }, nil }
	if err := Activate(staging, target, false, t0, lock); err != nil {
		t.Fatal(err)
	}
	if !unlocked {
		t.Error("the lock was not given back")
	}
	if _, err := os.Stat(staging); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("staging left behind: %v", err)
	}
	got := listFiles(t, target)
	if !slices.Contains(got, "bridge.db") || !slices.Contains(got, "brains-folders/family/CLAUDE.md") {
		t.Errorf("activated %v", got)
	}
	warnings, err := FixDataDir(target)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	data, _ := os.ReadFile(filepath.Join(target, "config.json"))
	if err := json.Unmarshal(data, &cfg); err != nil || cfg["data_dir"] != target {
		t.Errorf("config after FixDataDir: %s", data)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "~/brains") {
		t.Errorf("warnings %q", warnings)
	}
}

func TestImportRefusesAnotherIdentity(t *testing.T) {
	st := newMemStore()
	if _, err := Export(context.Background(), dataDir(t), []age.Recipient{identity(t).Recipient()}, st, ExportOptions{DB: sqliteDB{}}); err != nil {
		t.Fatal(err)
	}
	_, err := Import(context.Background(), st, []age.Identity{identity(t)}, t.TempDir(), ImportOptions{DB: sqliteDB{}})
	if err == nil || !strings.Contains(err.Error(), "FRIDOLIN_AGE_IDENTITY") {
		t.Errorf("error %v", err)
	}
}

func TestImportWithoutExport(t *testing.T) {
	_, err := Import(context.Background(), newMemStore(), []age.Identity{identity(t)}, t.TempDir(), ImportOptions{DB: sqliteDB{}})
	if err == nil || !strings.Contains(err.Error(), "no export yet") {
		t.Errorf("error %v", err)
	}
}

func TestImportRefusesANonEmptyStaging(t *testing.T) {
	staging := t.TempDir()
	write(t, staging, "x", "x", 0o600)
	st := newMemStore()
	id := identity(t)
	if _, err := Export(context.Background(), dataDir(t), []age.Recipient{id.Recipient()}, st, ExportOptions{DB: sqliteDB{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), st, []age.Identity{id}, staging, ImportOptions{DB: sqliteDB{}}); err == nil {
		t.Error("staged into a folder with files in it")
	}
}

// archive encrypts a hand-made archive: the manifest m, then members.
func archive(t *testing.T, id *age.X25519Identity, m Manifest, members ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc, err := age.Encrypt(&buf, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	md, _ := json.Marshal(m)
	_ = tw.WriteHeader(&tar.Header{Name: manifestName, Size: int64(len(md)), Mode: 0o600, Typeflag: tar.TypeReg})
	_, _ = tw.Write(md)
	for _, h := range members {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
		}
	}
	if err := errors.Join(tw.Close(), gz.Close(), enc.Close()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportRefusesBadArchives(t *testing.T) {
	ok := Manifest{Format: 1, Files: 1, Bytes: 1}
	cases := map[string]struct {
		m       Manifest
		members []*tar.Header
		want    string
	}{
		"dot dot":       {ok, []*tar.Header{{Name: "data/../../etc/x", Size: 1, Typeflag: tar.TypeReg}}, "unsafe member"},
		"absolute":      {ok, []*tar.Header{{Name: "/etc/x", Size: 1, Typeflag: tar.TypeReg}}, "unsafe member"},
		"outside data":  {ok, []*tar.Header{{Name: "x", Size: 1, Typeflag: tar.TypeReg}}, "unsafe member"},
		"symlink":       {ok, []*tar.Header{{Name: "data/x", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}}, "not a regular file"},
		"twice":         {Manifest{Format: 1, Files: 2, Bytes: 2}, []*tar.Header{{Name: "data/x", Size: 1, Typeflag: tar.TypeReg}, {Name: "data/x", Size: 1, Typeflag: tar.TypeReg}}, "twice"},
		"count":         {Manifest{Format: 1, Files: 3, Bytes: 3}, []*tar.Header{{Name: "data/x", Size: 1, Typeflag: tar.TypeReg}}, "its manifest says 3"},
		"newer format":  {Manifest{Format: 99}, nil, "format 99"},
		"missing db":    {Manifest{Format: 1, Files: 1, Bytes: 1, Databases: []string{"bridge.db"}}, []*tar.Header{{Name: "data/x", Size: 1, Typeflag: tar.TypeReg}}, "not in the archive"},
		"corrupt db":    {Manifest{Format: 1, Files: 1, Bytes: 4096, Databases: []string{"bridge.db"}}, []*tar.Header{{Name: "data/bridge.db", Size: 4096, Typeflag: tar.TypeReg}}, "bridge.db"},
		"backslash":     {ok, []*tar.Header{{Name: `data/..\x`, Size: 1, Typeflag: tar.TypeReg}}, "unsafe member"},
		"dot component": {ok, []*tar.Header{{Name: "data/./x", Size: 1, Typeflag: tar.TypeReg}}, "unsafe member"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			id := identity(t)
			st := newMemStore()
			key := ExportKey(t0)
			st.objs[key] = archive(t, id, c.m, c.members...)
			_, err := Import(context.Background(), st, []age.Identity{id}, t.TempDir(), ImportOptions{Key: key, DB: sqliteDB{}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %v, want one saying %q", err, c.want)
			}
		})
	}
}

func TestLatestRefusesABadPointer(t *testing.T) {
	st := newMemStore()
	st.objs[LatestKey] = []byte("core/exports/../lease.json")
	if _, err := Latest(context.Background(), st); err == nil {
		t.Error("followed a pointer out of core/exports/")
	}
}

func TestActivate(t *testing.T) {
	noLock := func(string) (func(), error) { return func() {}, nil }
	staged := func(t *testing.T, target string) string {
		s := filepath.Join(target, "core", "import", "x")
		write(t, s, "config.json", "new", 0o600)
		write(t, s, "brains/a/x", "new", 0o600)
		return s
	}
	t.Run("an empty target", func(t *testing.T) {
		target := t.TempDir()
		write(t, target, "bridge.lock", "", 0o600)
		if err := Activate(staged(t, target), target, false, t0, noLock); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(filepath.Join(target, "config.json")); string(data) != "new" {
			t.Errorf("config.json %q", data)
		}
	})
	t.Run("a target with data, without force", func(t *testing.T) {
		target := t.TempDir()
		write(t, target, "bridge.db", "old", 0o600)
		err := Activate(staged(t, target), target, false, t0, noLock)
		if !errors.Is(err, ErrTargetInUse) || !strings.Contains(err.Error(), "bridge.db") {
			t.Fatalf("error %v", err)
		}
		if data, _ := os.ReadFile(filepath.Join(target, "bridge.db")); string(data) != "old" {
			t.Error("the old data changed")
		}
	})
	t.Run("a target with data, with force", func(t *testing.T) {
		target := t.TempDir()
		write(t, target, "bridge.db", "old", 0o600)
		write(t, target, "config.json", "old", 0o600)
		if err := Activate(staged(t, target), target, true, t0, noLock); err != nil {
			t.Fatal(err)
		}
		aside := filepath.Join(target, "core", "replaced-20261020T120000Z")
		if data, _ := os.ReadFile(filepath.Join(aside, "bridge.db")); string(data) != "old" {
			t.Error("the old bridge.db was not kept aside")
		}
		if data, _ := os.ReadFile(filepath.Join(target, "config.json")); string(data) != "new" {
			t.Error("the new config is not in place")
		}
		if _, err := os.Stat(filepath.Join(target, "bridge.db")); !errors.Is(err, fs.ErrNotExist) {
			t.Error("the old bridge.db is still in place")
		}
	})
	t.Run("a running bridge", func(t *testing.T) {
		target := t.TempDir()
		busy := func(string) (func(), error) { return nil, errors.New("another bridge is running") }
		if err := Activate(staged(t, target), target, true, t0, busy); err == nil || !strings.Contains(err.Error(), "stop the core first") {
			t.Errorf("error %v", err)
		}
	})
}

func TestExportNeedsRecipientsAndDB(t *testing.T) {
	if _, err := Export(context.Background(), t.TempDir(), nil, newMemStore(), ExportOptions{DB: sqliteDB{}}); err == nil {
		t.Error("exported without a recipient")
	}
	if _, err := Export(context.Background(), t.TempDir(), []age.Recipient{identity(t).Recipient()}, newMemStore(), ExportOptions{}); err == nil {
		t.Error("exported without a database copier")
	}
	if _, err := Export(context.Background(), filepath.Join(t.TempDir(), "none"), []age.Recipient{identity(t).Recipient()}, newMemStore(), ExportOptions{DB: sqliteDB{}}); err == nil {
		t.Error("exported a missing data dir")
	}
}
