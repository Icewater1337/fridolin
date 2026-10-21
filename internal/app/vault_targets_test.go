package app

import (
	"context"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/nodes"
	"github.com/Icewater1337/fridolin/internal/snapshot"
)

// The owner's rule: the work folders never reach the cloud, and go to a
// node target when the brain chooses one.
var workBackup = manifest.Backup{Enabled: true,
	Exclude:  []string{"node_modules/", "*.log", "/work/", "/work-clones/"},
	Targets:  map[string]bool{"node:nas": true},
	NodeOnly: []string{"/work/", "/work-clones/"}}

// TestNodeOnlyRouting: node_only paths stay out of the cloud (the
// snapshots, the cold tier, the Drive backup) and go to the node targets
// the brain chooses, and only when the manifest the bridge runs the brain
// with chooses them too.
func TestNodeOnlyRouting(t *testing.T) {
	dir := t.TempDir()
	disk := writeBrainJSON(t, dir, "personal", workBackup)
	b := brainBackupOf("personal", dir, &disk, false)
	for rel, out := range map[string]bool{"work/contract.pdf": true, "work-clones/r/f.go": true, "memories/a.md": false, "run.log": true} {
		if b.excluded(rel) != out {
			t.Errorf("cloud: excluded(%q) = %v", rel, !out)
		}
	}
	patterns, ok := b.toNode("nas")
	if !ok {
		t.Fatal("the brain chooses nas")
	}
	for rel, out := range map[string]bool{"work/contract.pdf": false, "work-clones/r/f.go": false, "memories/a.md": false, "run.log": true, "x/node_modules/y": true, ".env": true} {
		if got := excludedBy(rel, patterns); got != out {
			t.Errorf("node: excluded(%q) = %v", rel, got)
		}
	}
	if _, ok := b.toNode("other"); ok {
		t.Error("a target the brain does not choose")
	}
	// The Drive backup leaves node_only out too.
	if !slices.Contains(disk.Backup.CloudExcludes(), "/work/") {
		t.Error("the Drive backup's excludes")
	}

	// The brain chose nas on disk; the bridge runs it with a manifest that
	// does not: nothing goes to nas, and the cloud still leaves work out.
	approved := disk
	approved.Backup = manifest.Backup{Enabled: true, Exclude: []string{"/work/"}}
	b = brainBackupOf("personal", dir, &approved, false)
	if _, ok := b.toNode("nas"); ok || !b.excluded("work/contract.pdf") {
		t.Errorf("an unapproved choice of target: %+v", b)
	}
	// node_only on one side only: work stays out of the node too (it is in
	// the approved exclude), and out of the cloud.
	approved.Backup = workBackup
	approved.Backup.NodeOnly = nil
	b = brainBackupOf("personal", dir, &approved, false)
	if p, ok := b.toNode("nas"); !ok || !excludedBy("work/contract.pdf", p) || !b.excluded("work/contract.pdf") {
		t.Errorf("node_only on one side only: %+v", b)
	}
	// cloud: false keeps the whole folder out of the cloud, and it still
	// goes to the node.
	off := workBackup
	off.Targets = map[string]bool{"cloud": false, "node:nas": true}
	m := writeBrainJSON(t, dir, "personal", off)
	b = brainBackupOf("personal", dir, &m, false)
	if b.Skip == "" || !b.excluded("memories/a.md") {
		t.Errorf("cloud false: %+v", b)
	}
	if _, ok := b.toNode("nas"); !ok {
		t.Error("cloud false still goes to the node")
	}
	// An unreadable brain.json goes nowhere.
	writeFile(t, filepath.Join(dir, manifest.File), "{")
	if _, ok := brainBackupOf("personal", dir, &m, false).toNode("nas"); ok {
		t.Error("an unreadable brain.json went to the node")
	}
}

func excludedBy(rel string, patterns []string) bool {
	return brainBackup{patterns: patterns}.excluded(rel)
}

func TestTargetDue(t *testing.T) {
	tg := config.VaultTarget{Name: "nas", NightlyAt: "03:00", Hourly: true}
	now := time.Date(2026, 10, 21, 4, 0, 0, 0, time.Local)
	h, n, c, r := targetDue(tg, targetStatus{}, now)
	if !h || !n || c || r {
		t.Fatalf("a new target: %v %v %v %v", h, n, c, r)
	}
	ok := &snapshotRecord{At: now.Add(-30 * time.Minute), ID: "abc", Kind: snapshotNightly}
	st := targetStatus{LastNightly: ok, LastHourly: ok, Last: ok}
	if h, n, c, r = targetDue(tg, st, now); h || n || !c || !r {
		t.Fatalf("after a snapshot: %v %v %v %v", h, n, c, r)
	}
	failed := &snapshotRecord{At: now.Add(-30 * time.Minute), Error: "the node nas is offline"}
	if _, n, _, _ = targetDue(tg, targetStatus{LastNightly: failed}, now); n {
		t.Fatal("a failed nightly waits before it tries again")
	}
	failed.At = now.Add(-2 * time.Hour)
	if _, n, _, _ = targetDue(tg, targetStatus{LastNightly: failed}, now); !n {
		t.Fatal("a failed nightly is tried again after an hour")
	}
}

type lockedSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *lockedSecrets) Get(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[name]
	if !ok {
		return "", hostsecrets.ErrNotFound
	}
	return v, nil
}

func (s *lockedSecrets) Set(name, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = v
	return nil
}

func (s *lockedSecrets) Has(name string) (bool, error) { _, err := s.Get(name); return err == nil, nil }

// TestNodeTargetEndToEnd: a core with an in-process node agent and the
// real restic. The target is added with its folder "on the node"; the
// personal brain's work folder goes there and nowhere else; the brain that
// does not choose the target stays out; the restore drill passes; and
// the folder is a restic repository on its own, as on another machine.
func TestNodeTargetEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("runs restic")
	}
	bin := findVaultTool("restic")
	if bin == "" {
		t.Skip("restic is not installed")
	}
	home := t.TempDir()
	data := filepath.Join(home, "data")
	personal := filepath.Join(home, "Personal")
	other := filepath.Join(data, "brains-folders", "other")
	for _, dir := range []string{personal, other} {
		for _, name := range []string{"memories/a.md", "work/contract.pdf", "work-clones/repo/f.go", "run.log"} {
			writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), "data of "+name)
		}
	}
	mp := writeBrainJSON(t, personal, "personal", workBackup)
	mo := writeBrainJSON(t, other, "other", manifest.Backup{Enabled: true})
	writeFile(t, filepath.Join(home, "config.json"), `{"data_dir": "`+filepath.ToSlash(data)+`"}`)
	cfgs, err := config.Open(filepath.Join(home, "config.json"), config.Options{GOOS: runtime.GOOS,
		Paths: config.Paths{Home: home, DefaultDataDir: data}, Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	brains := func() []vaultBrain {
		return []vaultBrain{{ID: "personal", Dir: personal, Manifest: mp}, {ID: "other", Dir: other, Manifest: mo}}
	}
	v := newVaultService(cfgs, false, logger, brains, nil)

	// The node: a backup disk it lends.
	svc, err := nodes.New(nodes.Options{Dir: filepath.Join(home, "nodes"), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Serve(ln); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Stop)
	disk := t.TempDir()
	code, _, _ := svc.NewJoinCode()
	nodeDir := t.TempDir()
	if _, err := nodeagent.Join(context.Background(), nodeagent.JoinOptions{Dir: nodeDir, Core: svc.Addr(), Code: code, Name: "nas",
		Capabilities: nodeproto.Capabilities{Backup: &nodeproto.BackupCap{Roots: []string{disk}}}, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	agent, err := nodeagent.Load(nodeDir, "test")
	if err != nil {
		t.Fatal(err)
	}
	agent.Logf, agent.MinBackoff, agent.MaxBackoff = t.Logf, 10*time.Millisecond, 100*time.Millisecond
	actx, stopAgent := context.WithCancel(context.Background())
	agentDone := make(chan struct{})
	go func() { _ = agent.Run(actx); close(agentDone) }()
	t.Cleanup(func() { stopAgent(); <-agentDone })
	waitLong(t, "the node connects", agent.Connected)
	if _, err := svc.ApproveDevice(agent.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveCommands(agent.ID()); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "backup approved", func() bool { return agent.State().Approved.Backup != nil })

	ts := newTargetService(cfgs, v, svc, false, logger)
	secrets := &lockedSecrets{m: map[string]string{}}
	ts.secrets.store, ts.secrets.getenv = secrets, func(string) string { return "" }
	ts.restic = func() string { return bin }
	ctx, cancel := context.WithCancel(context.Background())
	ts.Start(ctx)
	t.Cleanup(func() { cancel(); ts.Stop() })

	folder := filepath.Join(disk, "fridolin")
	if _, err := ts.Add(ctx, config.VaultTarget{Name: "nas", Node: "nobody123456", Folder: folder}); err == nil {
		t.Fatal("a target on an unknown node")
	}
	view, err := ts.Add(ctx, config.VaultTarget{Name: "nas", Node: agent.ID(), Folder: folder})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Ready || !slices.Equal(view.Brains, []string{"personal"}) || view.NodeName != "nas" {
		t.Fatalf("the new target: %+v", view)
	}
	if _, err := ts.Add(ctx, config.VaultTarget{Name: "nas", Node: agent.ID(), Folder: folder}); err == nil {
		t.Fatal("two targets of one name")
	}

	runJob := func(start func() error) targetView {
		t.Helper()
		if err := start(); err != nil {
			t.Fatal(err)
		}
		var got targetView
		waitLong(t, "the job", func() bool {
			got = ts.Views()[0]
			return got.Job == ""
		})
		return got
	}
	got := runJob(func() error { return ts.Snapshot("nas", "") })
	if got.LastNightly == nil || got.LastNightly.ID == "" {
		t.Fatalf("the snapshot: %+v", got.LastNightly)
	}
	if got.Size == 0 || got.LastPrune == nil || got.LastPrune.Error != "" {
		t.Fatalf("size and prune after the nightly: %+v", got.targetStatus)
	}
	if pw, _ := secrets.Get(targetSecretName("nas")); len(pw) < 32 {
		t.Fatal("the target's password is its own host secret")
	}
	got = runJob(func() error { return ts.RestoreDrill("nas") })
	if d := got.LastDrill; d == nil || !d.OK {
		t.Fatalf("the restore drill: %+v", d)
	}
	got = runJob(func() error { return ts.Check("nas", "") })
	if c := got.LastCheck; c == nil || !c.OK {
		t.Fatalf("check: %+v", c)
	}

	// The folder alone, opened as a local repository: what another machine
	// restores from.
	pw, _ := secrets.Get(targetSecretName("nas"))
	local := &snapshot.Restic{Binary: bin, Repo: folder, Env: []string{"RESTIC_PASSWORD=" + pw, "HOME=" + home, "PATH=/usr/bin:/bin"}}
	target := t.TempDir()
	if err := local.Restore(ctx, "latest", target); err != nil {
		t.Fatal(err)
	}
	restored := map[string]bool{}
	_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			restored[filepath.ToSlash(strings.TrimPrefix(p, target))] = true
		}
		return nil
	})
	in := func(dir, rel string) bool { return restored[filepath.ToSlash(filepath.Join(dir, rel))] }
	for _, rel := range []string{"work/contract.pdf", "work-clones/repo/f.go", "memories/a.md", "brain.json"} {
		if !in(personal, rel) {
			t.Errorf("the node target lacks personal's %s: %v", rel, restored)
		}
	}
	if in(personal, "run.log") {
		t.Error("an exclude that is not node_only stays out of the node too")
	}
	if in(other, "memories/a.md") {
		t.Error("a brain that does not choose the target went to it")
	}

	// And the cloud's snapshot paths keep work out.
	v.refreshLayout()
	vs := &vaultSnapshots{s: v, run: &vaultRun{}, cfg: cfgs.Get()}
	_, excludes := vs.paths(snapshotNightly)
	if !slices.Contains(excludes, personal+string(filepath.Separator)+"work") || !slices.Contains(excludes, personal+string(filepath.Separator)+"work-clones") {
		t.Errorf("the cloud snapshot does not leave work out: %q", excludes)
	}

	// Removing the target withdraws the folder; the repository stays.
	if err := ts.Remove("nas"); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "the folder withdrawn", func() bool { return !agent.State().Approved.AllowsBackupFolder(folder) })
	if _, err := os.Stat(filepath.Join(folder, "config")); err != nil {
		t.Fatal("the repository stays on the node")
	}
	if len(ts.Views()) != 0 {
		t.Fatal("removed")
	}
}

func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
