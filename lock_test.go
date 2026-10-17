//go:build unix

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstanceLockRefusesASecondHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireInstanceLock(dir); err == nil {
		second.release()
		t.Fatal("a second lock on the same data dir must fail")
	} else if !strings.Contains(err.Error(), "another bridge is already running") {
		t.Errorf("err = %v", err)
	}
	other, err := acquireInstanceLock(t.TempDir())
	if err != nil {
		t.Fatalf("another data dir must still lock: %v", err)
	}
	other.release()

	first.release()
	first.release() // a second release is harmless
	again, err := acquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("after release the lock must be free: %v", err)
	}
	again.release()
}

func TestNewBridgeTakesTheInstanceLock(t *testing.T) {
	// The lock used to be taken only by `fridolin run`, so a test or any other
	// caller of NewBridge opened bridge.db next to a running bridge.
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, "data")
	cfgs, err := OpenConfigStore(writeConfig(t, `{"data_dir": "`+dataDir+`", "http_listen": "off"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())

	running, err := acquireInstanceLock(dataDir)
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
	running.release()

	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	if l, err := acquireInstanceLock(dataDir); err == nil {
		l.release()
		t.Error("a bridge must hold the lock until Stop")
	}
	b.Stop()
	l, err := acquireInstanceLock(dataDir)
	if err != nil {
		t.Fatalf("Stop must release the lock: %v", err)
	}
	l.release()

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
	l, err = acquireInstanceLock(dataDir)
	if err != nil {
		t.Fatalf("a failed NewBridge must release the lock: %v", err)
	}
	l.release()
}
