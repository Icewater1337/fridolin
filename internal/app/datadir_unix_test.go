//go:build unix

package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
)

func TestProtectDataDirMakesExistingDataDirPrivate(t *testing.T) {
	// Serial: protectDataDir's warnings go to the process's standard logger
	// (captureLog), and it changes the working directory.
	for _, tc := range []struct {
		name     string
		mode     os.FileMode
		relative bool // data_dir "data", started from HOME
		wantLog  bool
	}{
		{"world-readable", 0o755, false, true},
		{"group-readable", 0o750, false, true},
		{"writable by everyone", 0o777, false, true},
		{"already private", 0o700, false, false},
		// The log names the folder by its full path, not by the relative one.
		{"relative path", 0o755, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			setenv(t, "HOME", home)
			data := filepath.Join(home, "data")
			mkdirMode(t, data, tc.mode)
			dataDir := data
			if tc.relative {
				t.Chdir(home)
				dataDir = "data"
			}
			logs := captureLog(t)

			cfg, err := loadConfig(t, writeConfig(t, `{"data_dir": "`+dataDir+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			protectDataDir(cfg)
			if got := permOf(t, data); got != 0o700 {
				t.Errorf("data dir has mode %04o, want 0700", got)
			}
			if got := logContains(logs, data+" was open to other users"); got != tc.wantLog {
				t.Errorf("logged the change = %v, want %v", got, tc.wantLog)
			}
		})
	}
}

func TestProtectDataDirLeavesHomeAlone(t *testing.T) {
	// Serial: protectDataDir's warnings go to the process's standard logger
	// (captureLog), and it changes the working directory.
	// Making the home folder 0700 would cut other users and services off
	// from all of it, so a data_dir there or above it only gets a warning.
	for _, tc := range []struct {
		name    string
		dataDir func(root, home string) string
		mode    os.FileMode
		warn    bool
	}{
		{"home folder", func(_, _ string) string { return "~" }, 0o755, true},
		{"home folder by path", func(_, home string) string { return home }, 0o755, true},
		{"parent of home", func(root, _ string) string { return root }, 0o755, true},
		{"private home folder", func(_, _ string) string { return "~" }, 0o700, false},
		// Relative paths depend on where the bridge was started; these
		// tests start it from HOME.
		{"home folder as relative path", func(_, _ string) string { return "." }, 0o755, true},
		{"parent of home as relative path", func(_, _ string) string { return ".." }, 0o755, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fsx.Canonical(t.TempDir())
			home := filepath.Join(root, "home")
			mkdirMode(t, home, tc.mode)
			if err := os.Chmod(root, tc.mode); err != nil {
				t.Fatal(err)
			}
			setenv(t, "HOME", home)
			t.Chdir(home)
			cfg, err := loadConfig(t, writeConfig(t, `{"data_dir": "`+tc.dataDir(root, home)+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			logs := captureLog(t)

			protectDataDir(cfg)

			for _, dir := range []string{root, home} {
				if got := permOf(t, dir); got != tc.mode {
					t.Errorf("%s changed from %04o to %04o", dir, tc.mode, got)
				}
			}
			want := cfg.DataDir
			if !filepath.IsAbs(want) {
				want = filepath.Join(home, want)
			}
			if got := logContains(logs, "warning: data_dir "+want+" is your home folder or one above it"); got != tc.warn {
				t.Errorf("warned = %v, want %v", got, tc.warn)
			}
		})
	}
}

func TestProtectDataDirOnlyWarnsAboutOpenDBFolder(t *testing.T) {
	// Serial: protectDataDir's warnings go to the process's standard logger
	// (captureLog).
	// A db_path outside the data dir may live in a folder shared on purpose.
	for _, tc := range []struct {
		name     string
		dataDir  string // relative to HOME; "link" is a symlink to HOME/data
		dbDir    string // relative to HOME; HOME/data is the data dir's folder
		mode     os.FileMode
		wantWarn bool
		advice   bool // the warning suggests chmod 700
	}{
		{"open to others", "data", "shared", 0o755, true, true},
		{"private", "data", "shared", 0o700, false, false},
		// Inside the data dir it is the bridge's own folder: no warning, and
		// only the data dir itself is made private.
		{"inside the data dir", "data", "data/db", 0o755, false, false},
		{"inside the data dir through a symlink", "link", "data/db", 0o755, false, false},
		// A 0700 home folder is what the data dir rule avoids, so the
		// warning does not suggest it.
		{"home folder", "data", ".", 0o755, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			setenv(t, "HOME", home)
			mkdirMode(t, filepath.Join(home, "data"), 0o700)
			if err := os.Symlink(filepath.Join(home, "data"), filepath.Join(home, "link")); err != nil {
				t.Fatal(err)
			}
			dbDir := filepath.Join(home, tc.dbDir)
			if dbDir == home {
				if err := os.Chmod(home, tc.mode); err != nil {
					t.Fatal(err)
				}
			} else {
				mkdirMode(t, dbDir, tc.mode)
			}
			cfg, err := loadConfig(t, writeConfig(t, `{"data_dir": "`+home+`/`+tc.dataDir+`", "db_path": "`+dbDir+`/bridge.db"}`))
			if err != nil {
				t.Fatal(err)
			}
			logs := captureLog(t)

			protectDataDir(cfg)

			if got := permOf(t, dbDir); got != tc.mode {
				t.Errorf("db folder mode changed from %04o to %04o", tc.mode, got)
			}
			if got := logContains(logs, "warning: "+dbDir+", the folder of db_path, is open to other users"); got != tc.wantWarn {
				t.Errorf("warned = %v, want %v", got, tc.wantWarn)
			}
			if got := logContains(logs, "consider chmod 700"); got != tc.advice {
				t.Errorf("suggested chmod 700 = %v, want %v", got, tc.advice)
			}
		})
	}
}

func TestProtectDataDirLeavesOtherUsersFolderAlone(t *testing.T) {
	// Serial: protectDataDir's warnings go to the process's standard logger
	// (captureLog).
	// A data dir that belongs to another user is theirs to set. Only root
	// could change it, and should not.
	setenv(t, "HOME", t.TempDir())
	var data string
	if os.Geteuid() == 0 {
		data = filepath.Join(t.TempDir(), "data")
		mkdirMode(t, data, 0o755)
		if err := os.Chown(data, 65534, 65534); err != nil {
			t.Fatal(err)
		}
	} else {
		// Any system folder that root owns will do; nothing is written.
		for _, dir := range []string{"/usr", "/bin", "/etc"} {
			st, err := os.Stat(dir)
			if err != nil {
				continue
			}
			if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() && st.Mode().Perm()&0o077 != 0 {
				data = dir
				break
			}
		}
		if data == "" {
			t.Skip("no folder of another user that others can open")
		}
	}
	before := permOf(t, data)
	logs := captureLog(t)

	protectDataDir(resolvedSettings(t, &config.File{DataDir: data, DBPath: filepath.Join(data, "bridge.db")}))

	if got := permOf(t, data); got != before {
		t.Errorf("data dir of another user changed from %04o to %04o", before, got)
	}
	if !logContains(logs, "warning: data_dir "+data+" belongs to another user") {
		t.Error("no warning that the data dir belongs to another user")
	}
	if logContains(logs, "could not make") {
		t.Error("tried to chmod a data dir of another user")
	}
}
