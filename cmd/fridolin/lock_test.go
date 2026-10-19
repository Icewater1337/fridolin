//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/store"
)

func TestNewBridgeTakesTheInstanceLock(t *testing.T) {
	// The lock used to be taken only by `fridolin run`, so a test or any other
	// caller of NewBridge opened bridge.db next to a running bridge.
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, "data")
	cfgs, err := openConfigStore(writeConfig(t, `{"data_dir": "`+dataDir+`", "http_listen": "off"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())

	running, err := platform.AcquireLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true}); err == nil {
		b.Stop()
		t.Fatal("NewBridge must refuse a data dir another bridge holds")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "bridge.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("bridge.db was opened next to the running bridge (stat: %v)", err)
	}
	running.Release()

	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	if l, err := platform.AcquireLock(dataDir); err == nil {
		l.Release()
		t.Error("a bridge must hold the lock until Stop")
	}
	b.Stop()
	l, err := platform.AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("Stop must release the lock: %v", err)
	}
	l.Release()

	// A NewBridge that fails after taking the lock gives it back. A folder
	// in place of dashboard.token makes it fail late.
	token := filepath.Join(dataDir, "dashboard.token")
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(token, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true}); err == nil {
		b.Stop()
		t.Fatal("NewBridge must fail without a usable dashboard token")
	}
	l, err = platform.AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("a failed NewBridge must release the lock: %v", err)
	}
	l.Release()
}

func TestBridgeClosesTheStoreWhenItGivesUpTheLock(t *testing.T) {
	// The lock only keeps a second bridge off bridge.db if giving it back
	// means the database is closed, whether the bridge stopped or failed to
	// start while or after opening it. This checks what is still open
	// afterwards, not the order of the steps inside Stop, NewBridge and
	// store.Open.
	tests := []struct {
		name string
		run  func(t *testing.T, cfgs *config.Store, dataDir string)
	}{
		{"stopped", func(t *testing.T, cfgs *config.Store, dataDir string) {
			b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
			if err != nil {
				t.Fatal(err)
			}
			if !containsSuffix(openFilesUnder(t, dataDir), "bridge.db") {
				b.Stop()
				t.Fatal("bridge.db is not listed as open while the bridge runs: the check below would prove nothing")
			}
			b.Stop()
		}},
		{"failed after opening the store", func(t *testing.T, cfgs *config.Store, dataDir string) {
			// A folder in place of dashboard.token makes NewBridge fail late.
			if err := os.MkdirAll(filepath.Join(dataDir, "dashboard.token", "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			if b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true}); err == nil {
				b.Stop()
				t.Fatal("NewBridge must fail without a usable dashboard token")
			}
			if _, err := os.Stat(filepath.Join(dataDir, "bridge.db")); err != nil {
				t.Fatalf("NewBridge failed before it opened the store: %v", err)
			}
		}},
		{"failed while opening the store", func(t *testing.T, cfgs *config.Store, dataDir string) {
			// whatsmeow's container opens on a read-only bridge.db whose
			// schema is current, then the bridge's own migration fails on a
			// table it has to create: one of a database from before the
			// bridge versioned its migrations.
			if os.Geteuid() == 0 {
				t.Skip("root can write a read-only file")
			}
			cfg := cfgs.Get()
			if err := cfgs.Boot().EnsureDirs(); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(cfg.DBPath, cfg.LogLevel)
			if err != nil {
				t.Fatal(err)
			}
			db := testStoreSQL(t, cfg.DBPath)
			_, err = db.Exec(`DROP TABLE ignored_chats`)
			if err == nil {
				_, err = db.Exec(`DROP TABLE fridolin_version`)
			}
			if err := errors.Join(err, db.Close(), s.Close()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(cfg.DBPath, 0o400); err != nil {
				t.Fatal(err)
			}
			b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
			if err == nil {
				b.Stop()
				t.Fatal("NewBridge must fail on a read-only bridge.db it has to migrate")
			}
			if !strings.Contains(err.Error(), "migrating store") {
				t.Fatalf("NewBridge failed elsewhere than in the store's migration: %v", err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := fsx.Canonical(t.TempDir())
			t.Setenv("HOME", home)
			dataDir := filepath.Join(home, "data")
			cfgs, err := openConfigStore(writeConfig(t, `{"data_dir": "`+dataDir+`", "http_listen": "off"}`))
			if err != nil {
				t.Fatal(err)
			}
			requireTestDataDir(t, cfgs.Get())
			tc.run(t, cfgs, dataDir)
			if open := openFilesUnder(t, dataDir); len(open) > 0 {
				t.Errorf("the lock is released, but these are still open: %v", open)
			}
		})
	}
}

// openFilesUnder lists the files under dir that this process has open. It
// skips the test where there is no way to list them.
func openFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	switch runtime.GOOS {
	case "linux":
		fds, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skipf("listing open files: %v", err)
		}
		for _, fd := range fds {
			if p, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil {
				paths = append(paths, p)
			}
		}
	case "darwin":
		// -n and -P: list sockets without looking up host names or ports.
		out, err := exec.Command("/usr/sbin/lsof", "-n", "-P", "-Fn", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			t.Skipf("listing open files: %v", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if p, ok := strings.CutPrefix(line, "n"); ok {
				paths = append(paths, p)
			}
		}
	default:
		t.Skipf("no way to list open files on %s", runtime.GOOS)
	}
	var under []string
	for _, p := range paths {
		if fsx.Within(p, dir) {
			under = append(under, p)
		}
	}
	return under
}

// containsSuffix reports whether one of paths ends in suffix.
func containsSuffix(paths []string, suffix string) bool {
	for _, p := range paths {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}
