//go:build unix

package store

import (
	"database/sql"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// setUmask sets the process umask for one test. The mode SQLite gives a new
// database depends on it; 022 is the usual default and makes such a file
// readable by everyone.
func setUmask(t *testing.T, mask int) {
	t.Helper()
	old := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(old) })
}

func TestNewStoreCreatesPrivateDB(t *testing.T) {
	setUmask(t, 0o022)
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	s := openTestStore(t, dbPath)
	if err := s.SavePoll("m1", "a@s.whatsapp.net", "main", "Lunch?", []string{"yes", "no"}); err != nil {
		t.Fatal(err)
	}
	if got := permOf(t, dbPath); got != 0o600 {
		t.Fatalf("new bridge.db has mode %04o, want 0600", got)
	}

	// SQLite creates its journal and WAL files with the database's mode, so
	// they must come out private too.
	for _, tc := range []struct {
		name, journalMode string
		suffixes          []string
	}{
		{"rollback journal", "PERSIST", []string{"-journal"}},
		{"write-ahead log", "WAL", []string{"-wal", "-shm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode("+tc.journalMode+")")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`INSERT OR REPLACE INTO polls (msg_id, chat_jid, question, options, created_at) VALUES ('m2', 'b@s.whatsapp.net', 'Lunch?', '[]', 0)`); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range tc.suffixes {
				if got := permOf(t, dbPath+suffix); got != 0o600 {
					t.Errorf("bridge.db%s has mode %04o, want 0600", suffix, got)
				}
			}
		})
	}
}

func TestNewStoreRestrictsExistingDB(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   os.FileMode
		logged bool
	}{
		{"world-readable", 0o644, true},
		{"group-readable", 0o640, true},
		{"writable by everyone", 0o666, true},
		{"already private", 0o600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			dbPath := filepath.Join(t.TempDir(), "bridge.db")
			// Empty files: SQLite treats them as a new database and ignores
			// stray WAL files next to a database in rollback mode.
			for _, p := range append([]string{dbPath}, sidecarPaths(dbPath)...) {
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, tc.mode); err != nil {
					t.Fatal(err)
				}
			}

			openTestStore(t, dbPath)

			if got := permOf(t, dbPath); got != 0o600 {
				t.Errorf("bridge.db has mode %04o, want 0600", got)
			}
			for _, p := range sidecarPaths(dbPath) {
				// SQLite may delete an empty journal; one that is left must be private.
				if st, err := os.Stat(p); err == nil && st.Mode().Perm() != 0o600 {
					t.Errorf("%s has mode %04o, want 0600", filepath.Base(p), st.Mode().Perm())
				}
			}
			if got := logContains(logs, dbPath+" was open to other users"); got != tc.logged {
				t.Errorf("logged the change = %v, want %v", got, tc.logged)
			}
		})
	}
}

func TestMakePrivateDBKeepsReadOnlyDB(t *testing.T) {
	// A database the bridge may only read is SQLite's to report, on the
	// first write. Opening it for writing here failed early with a
	// misleading "creating database file" error.
	if os.Geteuid() == 0 {
		t.Skip("root can open any file for writing")
	}
	captureLog(t)
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	if err := os.WriteFile(dbPath, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := makePrivateDB(dbPath); err != nil {
		t.Fatalf("makePrivateDB: %v", err)
	}
	if got := permOf(t, dbPath); got != 0o400 {
		t.Errorf("read-only bridge.db has mode %04o, want 0400", got)
	}
}

func TestRestrictToOwner(t *testing.T) {
	dir := t.TempDir()
	const special = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	for _, tc := range []struct {
		name       string
		dir        bool
		mode, want os.FileMode
	}{
		{"world-readable file", false, 0o644, 0o600},
		{"read-only file stays read-only", false, 0o444, 0o400},
		{"private file", false, 0o600, 0o600},
		{"setuid file keeps setuid", false, os.ModeSetuid | 0o755, os.ModeSetuid | 0o700},
		{"world-readable directory", true, 0o755, 0o700},
		{"private directory", true, 0o700, 0o700},
		// Only group and other access goes: a setgid folder keeps handing
		// its group to new files, and a sticky one keeps protecting them.
		{"setgid and sticky directory", true, os.ModeSetgid | os.ModeSticky | 0o775, os.ModeSetgid | os.ModeSticky | 0o700},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			p := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-"))
			var err error
			if tc.dir {
				err = os.Mkdir(p, 0o700)
			} else {
				err = os.WriteFile(p, nil, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
			if got := modeOf(t, p, special); got != tc.mode {
				// Some systems drop setgid for a folder whose group the
				// user is not in.
				t.Skipf("could not set mode %v, got %v", tc.mode, got)
			}
			if err := fsx.RestrictToOwner(p); err != nil {
				t.Fatal(err)
			}
			if got := modeOf(t, p, special); got != tc.want {
				t.Errorf("mode %v became %v, want %v", tc.mode, got, tc.want)
			}
			if got, want := logContains(logs, p+" was open to other users"), tc.mode != tc.want; got != want {
				t.Errorf("logged the change = %v, want %v", got, want)
			}
		})
	}

	t.Run("missing path", func(t *testing.T) {
		if err := fsx.RestrictToOwner(filepath.Join(dir, "missing")); err != nil {
			t.Errorf("missing path: %v", err)
		}
	})
}

// modeOf returns the permission bits of path and those of extra it has.
func modeOf(t *testing.T, path string, extra os.FileMode) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode() & (os.ModePerm | extra)
}

// A bridge.db from before the migrations were versioned gets the newer
// ones, and what it held stays: a poll it kept still counts its votes.
func TestStoreMigratesAnUnversionedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bridge.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE polls (msg_id TEXT PRIMARY KEY, chat_jid TEXT NOT NULL, question TEXT NOT NULL, options TEXT NOT NULL, created_at DATETIME NOT NULL)`,
		`CREATE TABLE poll_votes (poll_msg_id TEXT NOT NULL, voter_jid TEXT NOT NULL, selected_options TEXT NOT NULL, voted_at DATETIME NOT NULL, PRIMARY KEY (poll_msg_id, voter_jid))`,
		`INSERT INTO polls VALUES ('old', '120363000000000000@g.us', 'Lunch?', '["yes","no"]', '2026-01-01 00:00:00')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	for i := 0; i < 2; i++ { // and opening it again changes nothing
		s, err := Open(dbPath, "error")
		if err != nil {
			t.Fatalf("open %d: %v", i+1, err)
		}
		var version int
		if err := s.db.QueryRow(`SELECT version FROM fridolin_version`).Scan(&version); err != nil || version != len(appMigrations) {
			t.Errorf("open %d: version %d, %v; want %d", i+1, version, err, len(appMigrations))
		}
		p, err := s.SentPoll(t.Context(), "old")
		if err != nil || p.Brain != "" || p.Question != "Lunch?" || len(p.Options) != 2 {
			t.Errorf("open %d: the old poll is %+v, %v", i+1, p, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
