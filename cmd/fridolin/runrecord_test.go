//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// groupOf starts script under sh in a process group of its own, as a run's
// is, and returns the command and the PIDs the script prints, one a line.
func groupOf(t *testing.T, script string, pids int) (*exec.Cmd, []int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	var got []int
	sc := bufio.NewScanner(out)
	for len(got) < pids && sc.Scan() {
		n, err := strconv.Atoi(strings.TrimSpace(sc.Text()))
		if err != nil {
			t.Fatalf("script printed %q", sc.Text())
		}
		got = append(got, n)
	}
	go func() { _, _ = bufio.NewReader(out).ReadString(0) }() // keep the pipe drained
	return cmd, got
}

// alive reports whether process pid exists and has not ended: a zombie,
// which nothing has waited for yet, counts as ended.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// TestReapOrphanedRuns: at startup the bridge stops the process groups of
// runs a crashed bridge left running, and only those: not a process that
// got a recorded PID since, nor anything recorded in an earlier boot, and
// it forgets every record.
func TestReapOrphanedRuns(t *testing.T) {
	prev := reapGrace
	reapGrace = 500 * time.Millisecond
	t.Cleanup(func() { reapGrace = prev })
	boot, err := bootID()
	if err != nil {
		t.Skip("no boot id on this system:", err)
	}

	cases := []struct {
		name    string
		record  func(t *testing.T, dataDir string, leader int)
		stopped bool
	}{
		{"its run", func(t *testing.T, dataDir string, leader int) {
			if recordRun(dataDir, "a", leader) == "" {
				t.Fatal("not recorded")
			}
		}, true},
		{"a PID used again", func(t *testing.T, dataDir string, leader int) {
			writeRunRecord(t, dataDir, runRecord{Brain: "a", PGID: leader, Start: "an earlier process", Boot: boot})
		}, false},
		{"an earlier boot", func(t *testing.T, dataDir string, leader int) {
			start, _ := procStart(leader)
			writeRunRecord(t, dataDir, runRecord{Brain: "a", PGID: leader, Start: start, Boot: "another boot"})
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			cmd, pids := groupOf(t, `sleep 60 & echo $!; wait`, 1)
			tc.record(t, dataDir, cmd.Process.Pid)
			reapOrphanedRuns(dataDir)
			if entries, _ := os.ReadDir(runsDir(dataDir)); len(entries) != 0 {
				t.Errorf("records left: %v", entries)
			}
			waitedFor := time.Now().Add(5 * time.Second)
			for alive(pids[0]) && tc.stopped && time.Now().Before(waitedFor) {
				time.Sleep(20 * time.Millisecond)
			}
			if alive(pids[0]) == tc.stopped {
				t.Errorf("the run's command alive = %v, want stopped: %v", alive(pids[0]), tc.stopped)
			}
		})
	}

	// The CLI went with the crash, but a command it started lives on in its
	// process group.
	t.Run("the leader gone, a command left", func(t *testing.T) {
		dataDir := t.TempDir()
		cmd, pids := groupOf(t, `sleep 60 & echo $!; wait`, 1)
		if recordRun(dataDir, "a", cmd.Process.Pid) == "" {
			t.Fatal("not recorded")
		}
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		if !alive(pids[0]) {
			t.Fatal("the command went with its shell")
		}
		reapOrphanedRuns(dataDir)
		deadline := time.Now().Add(5 * time.Second)
		for alive(pids[0]) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(pids[0]) {
			t.Error("the command the run left is still running")
		}
	})

	t.Run("no such process, or no record", func(t *testing.T) {
		dataDir := t.TempDir()
		writeRunRecord(t, dataDir, runRecord{Brain: "a", PGID: 999999, Start: "1.0", Boot: boot})
		if err := os.WriteFile(filepath.Join(runsDir(dataDir), "junk.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		reapOrphanedRuns(dataDir)
		if entries, _ := os.ReadDir(runsDir(dataDir)); len(entries) != 0 {
			t.Errorf("records left: %v", entries)
		}
	})
}

// TestBridgeReapsWhenItStarts: a bridge that starts on a data dir stops the
// runs recorded there before any brain runs, which TestReapOrphanedRuns
// only shows for reapOrphanedRuns on its own.
func TestBridgeReapsWhenItStarts(t *testing.T) {
	prev := reapGrace
	reapGrace = 500 * time.Millisecond
	t.Cleanup(func() { reapGrace = prev })
	if _, err := bootID(); err != nil {
		t.Skip("no boot id on this system:", err)
	}
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	data := filepath.Join(home, "data")
	cmd, pids := groupOf(t, `sleep 60 & echo $!; wait`, 1)
	if recordRun(data, "a", cmd.Process.Pid) == "" {
		t.Fatal("not recorded")
	}
	cfgs, err := OpenConfigStore(writeConfig(t, `{"brain_mode": true, "data_dir": "`+data+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireTestDataDir(t, cfgs.Get())
	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	if entries, _ := os.ReadDir(runsDir(data)); len(entries) != 0 {
		t.Errorf("records left: %v", entries)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alive(pids[0]) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(pids[0]) {
		t.Error("the run the last bridge left is still going")
	}
}

func writeRunRecord(t *testing.T, dataDir string, rec runRecord) {
	t.Helper()
	data, _ := json.Marshal(rec)
	if err := os.MkdirAll(runsDir(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir(dataDir), strconv.Itoa(rec.PGID)+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunsAreRecordedWhileTheyRun: a brain's run is in data_dir/runs while
// it lasts, with the brain and its process group, and is gone after it.
func TestRunsAreRecordedWhileTheyRun(t *testing.T) {
	mgr, d, home := queueTestManager(t, 30000, "")
	br := mgr.Get("a")
	if _, _, _, err := mgr.Submit(dm("long")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })
	pid := br.Snapshot().Running.PID
	var rec runRecord
	waitFor(t, 5*time.Second, "the record", func() bool {
		data, err := os.ReadFile(filepath.Join(runsDir(filepath.Join(home, "data")), strconv.Itoa(pid)+".json"))
		return err == nil && json.Unmarshal(data, &rec) == nil
	})
	if rec.Brain != "a" || rec.PGID != pid || rec.Start == "" || rec.Boot == "" {
		t.Errorf("record = %+v", rec)
	}
	br.StopCurrent(true)
	waitFor(t, 5*time.Second, "the run to end", func() bool { return d.count() == 1 })
	if entries, err := os.ReadDir(runsDir(filepath.Join(home, "data"))); err != nil || len(entries) != 0 {
		t.Errorf("records after the run: %v, %v", entries, err)
	}
	if _, err := procStart(pid); !errors.Is(err, syscall.ESRCH) {
		// The leader was waited for, so it is gone, not a zombie.
		t.Errorf("procStart of the ended run = %v, want ESRCH", err)
	}
}
