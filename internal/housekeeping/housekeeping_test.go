package housekeeping

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// helperEnv makes the test binary a stand-in process: "sleep" sleeps,
// "tree" starts a sleeping child of itself and sleeps.
const helperEnv = "FRIDOLIN_HOUSEKEEPING_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "tree":
		self, _ := os.Executable()
		child := exec.Command(self)
		child.Env = append(os.Environ(), helperEnv+"=sleep")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	default:
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
}

func TestBytesAndCommand(t *testing.T) {
	if got := Bytes(3 << 30); got != "3.0 GB" {
		t.Errorf("Bytes = %q", got)
	}
	if got := Bytes(5 << 20); got != "5 MB" {
		t.Errorf("Bytes = %q", got)
	}
	long := platform.Proc{Name: "x", Args: []string{strings.Repeat("a", 200)}}
	if c := Command(long); len([]rune(c)) != 158 {
		t.Errorf("Command not cut: %d", len([]rune(c)))
	}
}

func TestFlagsAndUnder(t *testing.T) {
	args := []string{"restic", "-r", "x", "--cache-dir", "/data/core/restic-cache", "backup"}
	if v := flagValue(args, "--cache-dir"); v != "/data/core/restic-cache" {
		t.Errorf("flagValue = %q", v)
	}
	if v := flagValue([]string{"x", "-config=/a/b"}, "-config"); v != "/a/b" {
		t.Errorf("flagValue = = %q", v)
	}
	for _, c := range []struct {
		dir, path string
		want      bool
	}{
		{"/data/run-tokens", "/data/run-tokens/main/run-1.token", true},
		{"/data/run-tokens", "/data/run-tokens", false},
		{"/data/run-tokens", "/data/run-tokens-other/x", false},
		{"/data/run-tokens", "/data/x", false},
	} {
		if got := under(c.dir, c.path); got != c.want {
			t.Errorf("under(%q, %q) = %v", c.dir, c.path, got)
		}
	}
}

// TestPlanClassifies runs Plan on made-up processes: what is the bridge's
// and over goes, what is live, another bridge's or nobody's stays.
func TestPlanClassifies(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	tokens := "/data/run-tokens"
	envs := map[int][]string{
		20: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/run-tokens/main/run-gone.token"},
		21: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/run-tokens/main/run-gone.token"},
		30: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/run-tokens/main/run-live.token"},
		40: {"FRIDOLIN_BRAIN_TOKEN_FILE=/other/run-tokens/main/run-x.token"},
		50: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/run-tokens/main/run-gone.token"},
		60: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/run-tokens/main/run-rgone.token"},
		62: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/live-tokens/main/run-gone.token"},
		63: {"FRIDOLIN_BRAIN_TOKEN_FILE=/data/live-tokens/main/run-live.token"},
	}
	procs := []platform.Proc{
		{PID: 1, Name: "init", Start: old},
		{PID: 10, PPID: 1, Name: "fridolin", Args: []string{"/opt/fridolin", "run"}, Start: old},
		// An orphaned run: claude and the shell it started.
		{PID: 20, PPID: 1, Name: "claude", Args: []string{"claude", "-p"}, Start: old},
		{PID: 21, PPID: 20, Name: "bash", Args: []string{"bash"}, Start: old, RSS: 10 << 20},
		// A live run, under the bridge.
		{PID: 30, PPID: 10, Name: "claude", Args: []string{"claude", "-p"}, Start: old},
		// Another bridge's run.
		{PID: 40, PPID: 1, Name: "claude", Args: []string{"claude"}, Start: old},
		// An orphan too young to stop.
		{PID: 50, PPID: 1, Name: "node", Args: []string{"node"}, Start: now.Add(-time.Minute)},
		// The MCP server of a run that is over, and one of a live run.
		{PID: 60, PPID: 1, Name: "fridolin", Args: []string{"/opt/fridolin", "mcp", "--run", "rgone"}, Start: old},
		{PID: 61, PPID: 1, Name: "fridolin", Args: []string{"/opt/fridolin", "mcp", "--run", "rlive"}, Start: old},
		// What a live session that is over left, and what one that goes on
		// started.
		{PID: 62, PPID: 1, Name: "bash", Args: []string{"bash"}, Start: old},
		{PID: 63, PPID: 1, Name: "bash", Args: []string{"bash"}, Start: old},
		// The vault's rclone daemon without a bridge, and with one.
		{PID: 70, PPID: 1, Name: "rclone", Args: []string{"rclone", "rcd", "--rc-addr", "unix:///tmp/fridolin-rc-1/rc.sock"}, Start: old},
		{PID: 71, PPID: 10, Name: "rclone", Args: []string{"rclone", "rcd", "--rc-addr", "unix:///tmp/fridolin-rc-2/rc.sock"}, Start: old},
		// The user's own rclone and a heavy app of theirs.
		{PID: 72, PPID: 1, Name: "rclone", Args: []string{"rclone", "rcd", "--rc-addr", "localhost:5572"}, Start: old},
		{PID: 80, PPID: 1, Name: "Xcode", Start: old, RSS: 8 << 30},
		// restic and litestream of this data dir, orphaned.
		{PID: 90, PPID: 1, Name: "restic", Args: []string{"restic", "-r", "b2:x:restic", "--cache-dir", "/data/core/restic-cache", "backup"}, Start: old},
		{PID: 91, PPID: 1, Name: "litestream", Args: []string{"litestream", "replicate", "-no-expand-env", "-config", "/data/core/litestream-1.yml"}, Start: old},
		// A zombie of the bridge.
		{PID: 95, PPID: 10, Name: "sh", Zombie: true},
	}
	s := Sample{At: now, Procs: procs, CPU: map[int]float64{80: 95}}
	o := Ownership{Self: 10, Exe: "/opt/fridolin", RunTokensDir: tokens, LiveTokensDir: "/data/live-tokens", CoreDir: "/data/core",
		TokenLive:  func(p string) bool { return strings.HasSuffix(p, "run-live.token") },
		MCPRunLive: func(id string) bool { return id == "rlive" }}
	env := func(pid int) ([]string, error) {
		if e, ok := envs[pid]; ok {
			return e, nil
		}
		return []string{"HOME=/x"}, nil
	}
	set := Settings{CPUPercent: 80, RSSBytes: 4 << 30, MinAge: 30 * time.Minute}

	got := map[int]Finding{}
	for _, f := range Plan(s, env, set, o) {
		got[f.PID] = f
	}
	want := map[int]struct{ kind, action string }{
		20: {KindRunOrphan, ActionStop},
		50: {KindRunOrphan, ActionReport},
		60: {KindMCPOrphan, ActionStop},
		62: {KindRunOrphan, ActionStop},
		70: {KindRcloneRcd, ActionStop},
		90: {KindRestic, ActionStop},
		91: {KindLitestream, ActionStop},
		95: {KindZombie, ActionReport},
	}
	for pid, w := range want {
		f, ok := got[pid]
		if !ok {
			t.Errorf("PID %d: no finding, want %s", pid, w.kind)
			continue
		}
		if f.Kind != w.kind || f.Action != w.action {
			t.Errorf("PID %d: %s/%s, want %s/%s", pid, f.Kind, f.Action, w.kind, w.action)
		}
	}
	if len(got) != len(want) {
		t.Errorf("findings for %v, want only %v", keys(got), want)
	}
	if f := got[20]; len(f.Members) != 2 || f.RSS != 10<<20 {
		t.Errorf("the orphaned run's tree = %+v", f)
	}
	if !got[95].Problem {
		t.Error("the bridge's zombie is no problem")
	}

	// In the container the heavy process is put to the owner.
	set.Container = true
	got = map[int]Finding{}
	for _, f := range Plan(s, env, set, o) {
		got[f.PID] = f
	}
	if f := got[80]; f.Kind != KindForeign || f.Action != ActionAsk {
		t.Errorf("in the container the heavy process is %+v", f)
	}
}

func keys(m map[int]Finding) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// spawn starts the test binary as a helper with args and env, in a
// process group of its own, and collects it when it ends.
func spawn(t *testing.T, bin string, mode string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), bin, args...)
	cmd.Env = append(append(os.Environ(), helperEnv+"="+mode), env...)
	platform.IsolateProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = platform.KillProcessGroup(cmd.Process.Pid)
		<-done
	})
	return cmd
}

// TestStopsAnOrphanedRunAndMCPTree spawns real processes: a run whose
// token is gone with a child it started, the MCP server of a run that is
// over, a live run and an unrelated process. Dry-run finds and leaves
// them; stopping ends the orphans, children included, and nothing else.
func TestStopsAnOrphanedRunAndMCPTree(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	if _, err := platform.Processes(); err != nil {
		t.Skip(err)
	}
	if _, err := platform.ProcessEnv(os.Getpid()); err != nil {
		t.Skip("process environments cannot be read here:", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	tokens := filepath.Join(data, "run-tokens")
	if err := os.MkdirAll(filepath.Join(tokens, "main"), 0o700); err != nil {
		t.Fatal(err)
	}
	liveToken := filepath.Join(tokens, "main", "run-live.token")
	if err := os.WriteFile(liveToken, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The bridge's binary, as a run's MCP server names it.
	exe := filepath.Join(t.TempDir(), "fridolin")
	copyFile(t, self, exe)

	orphan := spawn(t, self, "tree", []string{"FRIDOLIN_BRAIN_TOKEN_FILE=" + filepath.Join(tokens, "main", "run-gone.token")})
	mcp := spawn(t, exe, "sleep", []string{"FRIDOLIN_BRAIN_TOKEN_FILE=" + filepath.Join(tokens, "main", "run-gone2.token")}, "mcp", "--run", "rgone")
	live := spawn(t, self, "tree", []string{"FRIDOLIN_BRAIN_TOKEN_FILE=" + liveToken})
	unrelated := spawn(t, self, "tree", nil)

	o := Ownership{Self: -1, Exe: exe, RunTokensDir: tokens, CoreDir: filepath.Join(data, "core"),
		TokenLive: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		MCPRunLive: func(string) bool { return false }}
	plan := func() map[int]Finding {
		t.Helper()
		// Wait until each tree has its child.
		var s Sample
		for range 100 {
			var err error
			s, err = TakeSample(context.Background(), platform.Processes, 0)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, p := range s.Procs {
				if p.PPID == orphan.Process.Pid || p.PPID == live.Process.Pid || p.PPID == unrelated.Process.Pid {
					n++
				}
			}
			if n == 3 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		got := map[int]Finding{}
		for _, f := range Plan(s, platform.ProcessEnv, Settings{DryRun: true}, o) {
			got[f.PID] = f
		}
		return got
	}
	got := plan()
	if f := got[orphan.Process.Pid]; f.Kind != KindRunOrphan || f.Action != ActionStop || len(f.Members) != 2 {
		t.Fatalf("orphaned run: %+v", f)
	}
	if f := got[mcp.Process.Pid]; f.Kind != KindMCPOrphan || f.Action != ActionStop {
		t.Fatalf("orphaned MCP server: %+v", f)
	}
	for _, c := range []*exec.Cmd{live, unrelated} {
		if f, ok := got[c.Process.Pid]; ok {
			t.Fatalf("PID %d should not be found: %+v", c.Process.Pid, f)
		}
	}
	orphanChild := got[orphan.Process.Pid].Members[1].PID

	// A dry run only reports: everything still runs.
	for _, pid := range []int{orphan.Process.Pid, orphanChild, mcp.Process.Pid} {
		if !running(t, pid) {
			t.Fatalf("PID %d ended without a stop", pid)
		}
	}

	st := Stopper{List: platform.Processes, Signal: platform.SignalProcess, Grace: 3 * time.Second}
	for _, pid := range []int{orphan.Process.Pid, mcp.Process.Pid} {
		n, err := st.Stop(context.Background(), got[pid])
		if err != nil || n != len(got[pid].Members) {
			t.Fatalf("Stop(%d) = %d, %v", pid, n, err)
		}
	}
	for _, pid := range []int{orphan.Process.Pid, orphanChild, mcp.Process.Pid} {
		if running(t, pid) {
			t.Errorf("PID %d still runs", pid)
		}
	}
	for _, c := range []*exec.Cmd{live, unrelated} {
		if !running(t, c.Process.Pid) {
			t.Errorf("PID %d was stopped", c.Process.Pid)
		}
	}
	// A finding whose process is gone, or whose PID now names a later
	// process, stops nothing.
	stale := got[orphan.Process.Pid]
	stale.Members = []Member{{PID: live.Process.Pid, Start: time.Unix(1, 0)}}
	if n, err := st.Stop(context.Background(), stale); n != 0 || err != nil || !running(t, live.Process.Pid) {
		t.Errorf("a stale finding stopped %d (%v)", n, err)
	}
}

// running reports whether pid runs (a zombie does not).
func running(t *testing.T, pid int) bool {
	t.Helper()
	procs, err := platform.Processes()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.PID == pid {
			return !p.Zombie
		}
	}
	return false
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
