package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Icewater1337/fridolin/internal/backup"
)

// TestBackupSchedulerTicks: Start looks at the schedule every minute, runs
// the backup once its time has come, only once that day, and stops with
// Stop.
func TestBackupSchedulerTicks(t *testing.T) {
	// A bubble's clock starts at 2000-01-01 00:00 UTC. Where 90 minutes on
	// is the next day already (a zone a little behind UTC), today's time
	// would be past at the first tick: the test then starts at midnight.
	bubble := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	start := bubble.In(time.Local)
	if due := start.Add(90 * time.Minute); due.Day() != start.Day() {
		start = time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.Local)
	}
	dailyAt := start.Add(90 * time.Minute).Format("15:04")
	fx := newBackupFixture(t, func(s *BackupSettings) { s.Enabled = true; s.IncludeBridgeConfig = false; s.DailyAt = dailyAt })
	_ = os.MkdirAll(filepath.Dir(fx.manager.rcloneConf()), 0o700)
	_ = os.WriteFile(fx.manager.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	fx.addTarget(t, "notes")
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(start.Sub(bubble))
		m := newBackupManager(fx.options)
		d := &backupDeletionRunner{src: []string{"a"}, cur: []string{"a"}}
		fx.runner = d.run
		m.Start(context.Background())
		syncs := func() int {
			synctest.Wait()
			d.mu.Lock()
			defer d.mu.Unlock()
			return len(d.syncs)
		}
		time.Sleep(89*time.Minute + time.Second)
		if n := syncs(); n != 0 {
			t.Fatalf("%d syncs before the time", n)
		}
		time.Sleep(time.Minute)
		if n := syncs(); n != 1 {
			t.Fatalf("%d syncs once the time came, want 1", n)
		}
		time.Sleep(3 * time.Hour)
		if n := syncs(); n != 1 {
			t.Errorf("%d syncs later that day, want still 1", n)
		}
		m.mu.Lock()
		res, last := m.state.Brains["notes"], m.state.LastScheduledDay
		m.mu.Unlock()
		if res == nil || !res.OK || res.Trigger != "scheduled" || last != start.Format(backup.DayLayout) {
			t.Errorf("result %+v, last scheduled day %q; want a scheduled run on %s", res, last, start.Format(backup.DayLayout))
		}
		m.Stop()
	})
}

// TestBackupNextRun: the status shows the run that will happen: tomorrow's
// once today's ran, now for one that is due, and none while Drive is not
// connected.
func TestBackupNextRun(t *testing.T) {
	fx := newBackupFixture(t, func(s *BackupSettings) { s.Enabled = true })
	m := fx.manager
	clock := time.Date(2026, 10, 18, 9, 0, 0, 0, time.Local)
	m.now = func() time.Time { return clock }
	next := func() *time.Time {
		var st struct {
			NextRun *time.Time `json:"next_run"`
		}
		_ = json.Unmarshal(mustBackupJSON(t, m.Status()), &st)
		if s := m.Summary().NextRun; (s == nil) != (st.NextRun == nil) || s != nil && !s.Equal(*st.NextRun) {
			t.Errorf("the overview's next run %v differs from the Backup page's %v", s, st.NextRun)
		}
		return st.NextRun
	}
	if n := next(); n != nil {
		t.Errorf("next run %v while Drive is not connected", n)
	}
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)
	_ = os.WriteFile(m.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	if n := next(); n == nil || !n.Equal(clock) {
		t.Errorf("next run = %v, want now (today's is due)", n)
	}
	m.mu.Lock()
	m.state.LastScheduledDay = clock.Format(backup.DayLayout)
	m.mu.Unlock()
	if n, want := next(), time.Date(2026, 10, 19, 3, 30, 0, 0, time.Local); n == nil || !n.Equal(want) {
		t.Errorf("next run = %v, want %v", n, want)
	}
}

// TestPrunePurgesExpiredVersionFolders checks the rclone calls prune makes, for
// machines without rclone: it purges the expired version folders by name and
// never filters files by their age.
func TestPrunePurgesExpiredVersionFolders(t *testing.T) {
	fx := newBackupFixture(t, nil)
	expired := backup.VersionName(time.Now().AddDate(0, 0, -31))
	recent := backup.VersionName(time.Now().AddDate(0, 0, -29))
	var mu sync.Mutex
	var calls [][]string
	fx.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
		mu.Lock()
		calls = append(calls, args)
		mu.Unlock()
		if args[0] != "lsjson" {
			return nil, nil
		}
		if strings.HasSuffix(args[len(args)-1], "/_bridge/versions") {
			fmt.Fprintln(stderr, "ERROR : : error listing: directory not found")
			return nil, fmt.Errorf("exit status 3")
		}
		return []byte(`[{"Name":"` + expired + `"},{"Name":"` + recent + `"},{"Name":"keep-me"}]`), nil
	}
	notes := BackupTarget{ID: "notes"}
	fx.manager.prune(context.Background(), []BackupTarget{notes})
	var purged []string
	for _, c := range calls {
		switch c[0] {
		case "purge":
			purged = append(purged, c[1])
		case "lsjson":
		default:
			t.Errorf("unexpected rclone call %v", c)
		}
	}
	want := []string{"brain-gdrive:" + "fridolin-backup" + "/notes/versions/" + expired}
	if fmt.Sprint(purged) != fmt.Sprint(want) {
		t.Errorf("purged %v, want %v", purged, want)
	}
}

// TestPruneKeepsItsConfigAndStopsWhenCancelled: the listings and purges of
// one prune all use the config it started with, and a cancelled job stops
// it at once instead of trying (and logging) every remaining id.
func TestPruneKeepsItsConfigAndStopsWhenCancelled(t *testing.T) {
	listing := []byte(`[{"Name":"` + backup.VersionName(time.Now().AddDate(0, 0, -31)) + `"},` +
		`{"Name":"` + backup.VersionName(time.Now().AddDate(0, 0, -60)) + `"}]`)
	brains := []BackupTarget{{ID: "notes"}, {ID: "work"}}

	t.Run("backup.root changed mid-run", func(t *testing.T) {
		fx := newBackupFixture(t, nil)
		var calls [][]string
		fx.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
			calls = append(calls, args)
			if len(calls) == 1 {
				fx.update(func(s *BackupSettings) { s.Root = "moved" })
			}
			if args[0] == "lsjson" {
				return listing, nil
			}
			return nil, nil
		}
		fx.manager.prune(context.Background(), brains)
		if len(calls) != 9 {
			t.Errorf("%d rclone calls, want a listing and two purges for each of 3 ids: %v", len(calls), calls)
		}
		for _, c := range calls {
			if p := c[len(c)-1]; !strings.HasPrefix(p, "brain-gdrive:"+"fridolin-backup"+"/") {
				t.Errorf("%s %s is not under the backup.root prune started with", c[0], p)
			}
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		fx := newBackupFixture(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls [][]string
		fx.runner = func(ctx context.Context, args []string, _ io.Writer) ([]byte, error) {
			calls = append(calls, args)
			if args[0] == "purge" {
				cancel() // the job is cancelled while rclone purges
				return nil, ctx.Err()
			}
			return listing, nil
		}
		fx.manager.prune(ctx, brains)
		if len(calls) != 2 {
			t.Errorf("prune went on after the cancel: %v", calls)
		}
	})
}

// TestBackupRefusesMassDeletion runs the check before a brain's sync: it
// mirrors ordinary changes, and unless forced refuses an empty or missing
// brain folder, one that lost more than half of its backed-up files, more
// than a fifth once that is ten or more, or 250 or more, and one with a top
// folder of five or more files that lost more than half of them, as a brain
// that wiped its memories/ leaves it. The sync's --max-delete is the most
// the counts allow. A folder rclone can only partly read is synced with no
// deletion allowed.
func TestBackupRefusesMassDeletion(t *testing.T) {
	fx := newBackupFixture(t, nil)
	dir := fx.addTarget(t, "notes").Dir
	br := fx.target("notes")
	files := func(names ...string) []string { return append([]string{}, names...) }
	// seq is n files, prefix0 to prefix<n-1>.
	seq := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + strconv.Itoa(i)
		}
		return out
	}
	// A larger brain folder: 88 files of its own and a memories/ of 12.
	notes, memories := seq("n", 88), seq("memories/m", 12)
	large := append(slices.Clone(notes), memories...)
	const unreadable = `ERROR : locked: failed to open directory "locked": open locked: permission denied`
	cases := []struct {
		name      string
		src, cur  []string
		missing   bool // the brain folder does not exist
		srcErr    string
		listErr   string
		syncErr   string // the sync fails with this; refused and failed then expect it to have run
		force     bool
		refused   bool
		says      string // a refusal's error says this
		failed    string // the result's error contains this, no sync ran unless syncErr is set
		maxDelete string // the sync's --max-delete, "" for none
	}{
		{name: "unchanged", src: files("a", "b", "c", "d"), cur: files("a", "b", "c", "d"), maxDelete: "2"},
		{name: "first backup", src: files("a"), cur: nil, maxDelete: "0"},
		{name: "empty folder, empty backup", src: files(), cur: files(), maxDelete: "0"},
		{name: "half gone is allowed", src: files("a", "b"), cur: files("a", "b", "c", "d"), maxDelete: "2"},
		{name: "more than half gone", src: files("a"), cur: files("a", "b", "c", "d"), refused: true, says: "3 of 4 backed-up files are gone"},
		{name: "empty brain folder", src: files(), cur: files("a", "b"), refused: true},
		{name: "missing brain folder", missing: true, cur: files("a"), refused: true},
		{name: "new files do not offset deletions", src: files("v", "w", "x", "y", "z"), cur: files("a", "b", "c"), refused: true},
		{name: "force mirrors anyway", src: files(), cur: files("a", "b"), force: true},
		{name: "3 of 100 gone", src: seq("n", 97), cur: seq("n", 100), maxDelete: "20"},
		{name: "a fifth gone", src: seq("n", 40), cur: seq("n", 50), maxDelete: "10"},
		{name: "more than a fifth, fewer than ten gone", src: seq("n", 21), cur: seq("n", 30), maxDelete: "9"},
		{name: "more than a fifth, ten or more gone", src: seq("n", 38), cur: seq("n", 50), refused: true, says: "12 of 50 backed-up files are gone"},
		{name: "a few of a large folder gone", src: seq("n", 1990), cur: seq("n", 2000), maxDelete: "249"},
		{name: "300 of 2000 gone", src: seq("n", 1700), cur: seq("n", 2000), refused: true, says: "300 of 2000 backed-up files are gone"},
		{name: "a wiped memories/", src: notes, cur: large, refused: true, says: "12 of the 12 backed-up files in memories/ are gone"},
		{name: "half of memories/ gone", src: append(slices.Clone(notes), memories[:6]...), cur: large, maxDelete: "20"},
		{name: "a wiped top folder of four files", src: seq("n", 96), cur: append(seq("n", 96), seq("tmp/t", 4)...), maxDelete: "20"},
		{name: "deeper folders count for their top folder", src: append(slices.Clone(notes), "memories/a/1", "memories/b/2"),
			cur: append(slices.Clone(large), "memories/a/1", "memories/b/2"), refused: true, says: "12 of the 14 backed-up files in memories/"},
		{name: "force mirrors a wiped memories/", src: notes, cur: large, force: true},
		{name: "listing the backup fails", src: files("a"), listErr: "ERROR : couldn't connect", failed: "checking the backup before syncing failed"},
		// The folder is there, so rclone failing to read it is no proof it
		// is empty, and no reason to leave its readable files out either.
		{name: "brain folder partly unreadable", srcErr: unreadable, cur: files("a"), syncErr: "not deleting files as there were IO errors",
			failed: "parts of the brain folder could not be read, so the backup synced the rest and deleted nothing", maxDelete: "0"},
		{name: "brain folder unreadable for a moment", srcErr: unreadable, cur: files("a"), maxDelete: "0"},
		{name: "brain folder vanishes while listed", src: nil, cur: files("a"), maxDelete: "0"},
		{name: "the sync of a partly read folder finds deletions", srcErr: unreadable, cur: files("a"),
			syncErr: "Got fatal error on delete: --max-delete threshold reached", refused: true, maxDelete: "0"},
		{name: "brain folder and backup unreadable", srcErr: unreadable, listErr: "ERROR : couldn't connect", failed: "checking the backup before syncing failed"},
		// rclone sync treats differently normalized names as the same file.
		{name: "decomposed names in the backup", src: files("Gr\u00fcsse.md", "M\u00e4rz.md"), cur: files("Gru\u0308sse.md", "Ma\u0308rz.md"), maxDelete: "1"},
		{name: "decomposed names in the folder", src: files("Gru\u0308sse.md", "Ma\u0308rz.md"), cur: files("Gr\u00fcsse.md", "M\u00e4rz.md"), maxDelete: "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if c.missing {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			d := &backupDeletionRunner{src: c.src, cur: c.cur, srcErr: c.srcErr, listErr: c.listErr, syncErr: c.syncErr}
			fx.runner = d.run
			res := fx.manager.backupBrain(context.Background(), br, "manual", c.force)
			synced := 0
			if c.syncErr != "" {
				synced = 1
			}
			switch {
			case c.refused:
				if res.OK || !res.Refused || !strings.HasPrefix(res.Error, massDeletionError) || !strings.Contains(res.Error, c.says) || len(d.syncs) != synced {
					t.Errorf("want a refusal saying %q after %d syncs, got %+v, syncs %v", c.says, synced, res, d.syncs)
				}
				if res.Seconds != res.Finished.Sub(res.Started).Seconds() {
					t.Errorf("seconds = %v, want the time the run took", res.Seconds)
				}
			case c.failed != "":
				if res.OK || res.Refused || !strings.Contains(res.Error, c.failed) || len(d.syncs) != synced {
					t.Errorf("want a failure %q after %d syncs, got %+v", c.failed, synced, res)
				}
			default:
				if !res.OK || res.Refused || len(d.syncs) != 1 {
					t.Fatalf("want one sync, got %+v, syncs %v", res, d.syncs)
				}
			}
			if len(d.syncs) == 1 {
				if got := backupMaxDelete(d.syncs[0]); got != c.maxDelete {
					t.Errorf("--max-delete = %q, want %q", got, c.maxDelete)
				}
			}
		})
	}
}

// TestBackupBrainKeepsItsConfig: the check and the sync of one brain use
// the config the brain's backup started with, so a backup.root changed in
// between cannot count one folder in Drive and sync into another.
func TestBackupBrainKeepsItsConfig(t *testing.T) {
	fx := newBackupFixture(t, nil)
	fx.addTarget(t, "notes")
	d := &backupDeletionRunner{src: []string{"a"}, cur: []string{"a"}}
	moved := false
	fx.runner = func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
		if args[0] == "lsjson" && !moved {
			moved = true
			fx.update(func(s *BackupSettings) { s.Root = "moved" })
		}
		return d.run(ctx, args, stderr)
	}
	if res := fx.manager.backupBrain(context.Background(), fx.target("notes"), "manual", false); !res.OK {
		t.Fatalf("backup = %+v", res)
	}
	if len(d.syncs) != 1 {
		t.Fatalf("want one sync, got %v", d.syncs)
	}
	for _, p := range []string{d.syncs[0][2], backupFlagValue(d.syncs[0], "--backup-dir")} {
		if !strings.HasPrefix(p, backupFixtureBase+"/") {
			t.Errorf("the sync writes to %s, not under the backup.root the check counted", p)
		}
	}
}

// TestMassDeletionCheckListsLikeTheSync: the check lists both sides with the
// sync's filter file and symlink handling. Without the brain's excludes,
// files backed up before they were excluded, which the sync never deletes
// from Drive, would count as gone on every run; without --links, a symlink
// the sync backed up as .rclonelink would be missing from the brain folder.
func TestMassDeletionCheckListsLikeTheSync(t *testing.T) {
	fx := newBackupFixture(t, nil)
	fx.addTarget(t, "notes")
	br := fx.target("notes")
	d := &backupDeletionRunner{src: []string{"a"}, cur: []string{"a"}}
	fx.runner = d.run
	if res := fx.manager.backupBrain(context.Background(), br, "manual", false); !res.OK {
		t.Fatalf("backup = %+v", res)
	}
	filter := filepath.Join(fx.manager.dir(), "filters-notes.txt")
	if len(d.syncs) != 1 || backupFlagValue(d.syncs[0], "--filter-from") != filter || !slices.Contains(d.syncs[0], "--links") {
		t.Fatalf("syncs = %v, want one with --filter-from %s and --links", d.syncs, filter)
	}
	for _, side := range []struct{ name, path string }{
		{"brain folder", br.Dir + "/"},
		{"backup", backupFixtureBase + "/current"},
	} {
		t.Run(side.name, func(t *testing.T) {
			var args []string
			for _, l := range d.lists {
				if slices.Contains(l, side.path) {
					args = l
				}
			}
			if args == nil {
				t.Fatalf("no listing of %s in %v", side.path, d.lists)
			}
			if got := backupFlagValue(args, "--filter-from"); got != filter {
				t.Errorf("--filter-from = %q, want the sync's %s", got, filter)
			}
			if !slices.Contains(args, "--links") {
				t.Errorf("listed without --links: %v", args)
			}
		})
	}
}

// TestBackupBridgeHasNoDeletionLimit: the bridge's own settings are staged
// by the bridge, so they get neither the check nor --max-delete; removing a
// brain drops its manifest from them, and that must reach the backup.
func TestBackupBridgeHasNoDeletionLimit(t *testing.T) {
	fx := newBackupFixture(t, nil)
	d := &backupDeletionRunner{src: []string{}, cur: []string{"config.json", "brains/a.brain.json", "brains/b.brain.json"}}
	fx.runner = d.run
	if res := fx.manager.backupBridge(context.Background(), "manual"); !res.OK {
		t.Fatalf("bridge backup = %+v", res)
	}
	if len(d.lists) != 0 {
		t.Errorf("the bridge backup listed files for a deletion check: %v", d.lists)
	}
	if len(d.syncs) != 1 || backupMaxDelete(d.syncs[0]) != "" {
		t.Errorf("want one sync without --max-delete, got %v", d.syncs)
	}
}

// TestSyncDirReportsMaxDelete: rclone stopping at --max-delete is a refusal,
// not an unexplained failure.
func TestSyncDirReportsMaxDelete(t *testing.T) {
	fx := newBackupFixture(t, nil)
	fx.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
		fmt.Fprintln(stderr, `{"level":"error","msg":"Got fatal error on delete: --max-delete threshold reached","object":"a"}`)
		fmt.Fprintln(stderr, `{"level":"error","msg":"Fatal error received - not attempting retries"}`)
		return nil, fmt.Errorf("exit status 7")
	}
	cases := []struct {
		name      string
		maxDelete int
		want      func(res *BackupResult) string
	}{
		// Unlike the check before the sync, this refusal comes after rclone
		// moved up to the limit out of current/; the message must say where.
		{"limit 1", 1, func(res *BackupResult) string {
			return "up to that many files were already moved from current/ to versions/" + res.Version
		}},
		// A partly readable brain folder is synced with no deletion allowed.
		{"limit 0", 0, func(*BackupResult) string { return "nothing was removed from current/" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := fx.manager.syncDir(context.Background(), t.TempDir(), backupFixtureBase, "", "manual", c.maxDelete)
			if res.OK || !res.Refused || !strings.HasPrefix(res.Error, massDeletionError) {
				t.Errorf("result = %+v", res)
			}
			if want := c.want(res); !strings.Contains(res.Error, want) {
				t.Errorf("error = %q, want it to say %q", res.Error, want)
			}
		})
	}
}

// TestBackupStopWaitsForTheRun: Stop cancels a running backup and waits
// for it, so its result is saved before the bridge closes down, and nothing
// starts after it.
func TestBackupStopWaitsForTheRun(t *testing.T) {
	fx := newBackupFixture(t, nil)
	m := fx.manager
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)
	_ = os.WriteFile(m.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	fx.addTarget(t, "notes")
	syncing := make(chan struct{})
	fx.runner = func(ctx context.Context, args []string, stderr io.Writer) ([]byte, error) {
		if args[0] != "sync" {
			return []byte("[]"), nil
		}
		close(syncing)
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // rclone winding down
		return nil, ctx.Err()
	}
	if err := m.StartRun([]string{"notes"}, false, "manual", true); err != nil {
		t.Fatal(err)
	}
	<-syncing
	m.Stop()
	m.mu.Lock()
	res, job := m.state.Brains["notes"], m.job
	m.mu.Unlock()
	if res == nil || res.Error != "cancelled" || job != nil {
		t.Errorf("after Stop: result %+v, job %+v; want the cancelled run recorded and done", res, job)
	}
	if err := m.StartRun([]string{"notes"}, false, "manual", true); !errors.Is(err, errBackupStopped) {
		t.Errorf("StartRun after Stop = %v, want errBackupStopped", err)
	}
}

// TestUnreadableBackupStateIsKept: a state file that cannot be parsed is
// kept as state.json.corrupt rather than overwritten by the next save.
func TestUnreadableBackupStateIsKept(t *testing.T) {
	fx := newBackupFixture(t, nil)
	path := fx.manager.statePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"brains": {"notes": tru`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newBackupManager(fx.options)
	if data, err := os.ReadFile(path + ".corrupt"); err != nil || string(data) != `{"brains": {"notes": tru` {
		t.Errorf("state.json.corrupt = %q, %v; want the unreadable file", data, err)
	}
	if m.state.Brains == nil || len(m.state.Brains) != 0 {
		t.Errorf("state = %+v, want an empty one", m.state)
	}
}

// TestDailyBackupWithoutDriveIsLogged: a daily backup with no Drive link
// to run on says so in the log, once a day rather than every minute.
func TestDailyBackupWithoutDriveIsLogged(t *testing.T) {
	fx := newBackupFixture(t, func(s *BackupSettings) { s.Enabled = true })
	logs := captureBackupLog(t)
	day := time.Date(2026, 10, 18, 4, 0, 0, 0, time.Local)
	for i := range 3 {
		fx.manager.tick(day.Add(time.Duration(i) * time.Minute))
	}
	fx.manager.tick(day.AddDate(0, 0, 1))
	all := strings.Join(logs(), "\n")
	if n := strings.Count(all, "Google Drive is not connected"); n != 2 {
		t.Errorf("logged %d times over two days, want 2:\n%s", n, all)
	}
}

// TestBackupSummarySaysWhatRuns: the overview tells a restore that runs
// from a backup.
func TestBackupSummarySaysWhatRuns(t *testing.T) {
	cases := []struct {
		name               string
		job                *backupJob
		running, restoring bool
	}{
		{"nothing", nil, false, false},
		{"a backup", &backupJob{kind: jobBackup}, true, false},
		{"a restore", &backupJob{kind: jobRestore}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newBackupFixture(t, nil).manager
			m.mu.Lock()
			m.job = tc.job
			m.mu.Unlock()
			t.Cleanup(func() { m.mu.Lock(); m.job = nil; m.mu.Unlock() })
			if s := m.Summary(); s.Running != tc.running || s.Restoring != tc.restoring {
				t.Errorf("running %v, restoring %v; want %v, %v", s.Running, s.Restoring, tc.running, tc.restoring)
			}
		})
	}
}

// TestBackupSummaryShowsTheLatestFailure: with several brains failing, the
// overview shows the latest failure and whose it is, the same on every
// refresh, and a removed brain's failure neither shows nor stays saved.
func TestBackupSummaryShowsTheLatestFailure(t *testing.T) {
	fx := newBackupFixture(t, func(s *BackupSettings) { s.IncludeBridgeConfig = false })
	m := fx.manager
	_ = os.MkdirAll(filepath.Dir(m.rcloneConf()), 0o700)
	_ = os.WriteFile(m.rcloneConf(), []byte("[brain-gdrive]\ntype = alias\nremote = /nowhere\n"), 0o600)
	fx.addTarget(t, "a")
	fx.addTarget(t, "b")
	at := time.Date(2026, 10, 18, 3, 30, 0, 0, time.Local)
	failed := func(min int, msg string) *BackupResult {
		return &BackupResult{Started: at, Finished: at.Add(time.Duration(min) * time.Minute), Error: msg}
	}
	m.mu.Lock()
	m.state.Brains["a"] = failed(1, "a failed")
	m.state.Brains["b"] = failed(2, "b failed")
	m.state.Brains["gone"] = failed(3, "gone failed") // a brain removed since
	m.state.Bridge = &BackupResult{OK: true, Finished: at.Add(4 * time.Minute)}
	m.mu.Unlock()
	for range 20 {
		s := m.Summary()
		if s.LastError != "b failed" || s.LastErrorBrain != "b" {
			t.Fatalf("summary error = %q of %q, want b's, the latest of the configured brains", s.LastError, s.LastErrorBrain)
		}
	}

	fx.runner = (&backupDeletionRunner{src: []string{"x"}, cur: []string{"x"}}).run
	if res := fx.run(t, "a"); !res.OK {
		t.Fatalf("backup of a = %+v", res)
	}
	saved := newBackupManager(fx.options).state.Brains
	if _, ok := saved["gone"]; ok || saved["a"] == nil || saved["b"] == nil {
		t.Errorf("saved results for %v, want a and b only", slices.Collect(maps.Keys(saved)))
	}
}

// TestBackupBridgeRefusesAnIncompleteStage: the bridge's settings are
// synced only when every file of them could be staged. A sync of what
// could would move the good copies in Drive to versions/ and report OK.
func TestBackupBridgeRefusesAnIncompleteStage(t *testing.T) {
	cases := []struct {
		name    string
		failing string // the staged file whose write fails; "" for none
	}{
		// What the last run staged cannot be cleared: its brains folder
		// became read-only.
		{"the last stage stays", ""},
		{"config.json", "config.json"},
		{"a brain's manifest", "notes.brain.json"},
		{"the README", "README.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBackupFixture(t, nil)
			fx.addTarget(t, "notes")
			m := fx.manager
			var mu sync.Mutex
			var calls [][]string
			fx.runner = func(_ context.Context, args []string, _ io.Writer) ([]byte, error) {
				mu.Lock()
				calls = append(calls, args)
				mu.Unlock()
				return nil, nil
			}
			if tc.failing == "" {
				if os.Geteuid() == 0 {
					t.Skip("root writes to read-only folders")
				}
				brains := filepath.Join(m.dir(), "_staging", "brains")
				if err := os.MkdirAll(brains, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(brains, "old.brain.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(brains, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(brains, 0o700) })
			} else {
				prev := writeStaged
				writeStaged = func(name string, data []byte, perm os.FileMode) error {
					if filepath.Base(name) == tc.failing {
						return errors.New("disk full")
					}
					return prev(name, data, perm)
				}
				t.Cleanup(func() { writeStaged = prev })
			}

			res := m.backupBridge(context.Background(), "manual")
			if res.OK || !strings.Contains(res.Error, "staging the bridge's settings failed") {
				t.Errorf("result = %+v, want a staging failure", res)
			}
			if tc.failing != "" && !strings.Contains(res.Error, "disk full") {
				t.Errorf("result = %+v, want the write's error", res)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 0 {
				t.Errorf("rclone ran %v", calls)
			}
		})
	}
}

// TestSyncDirRetriesFilesThatChange: rclone fails a file whose size changes
// while it copies it ("source file is being updated"), and a brain's
// history gets a line whenever a message comes in. The sync asks rclone to
// try again after a pause, and the log says when it had to.
func TestSyncDirRetriesFilesThatChange(t *testing.T) {
	fx := newBackupFixture(t, nil)
	var got []string
	fx.runner = func(_ context.Context, args []string, stderr io.Writer) ([]byte, error) {
		got = args
		fmt.Fprintln(stderr, `{"level":"error","msg":"conversation-history.jsonl: Failed to copy: can't copy - source file is being updated (size changed from 10 to 12)","object":"conversation-history.jsonl"}`)
		fmt.Fprintln(stderr, `{"level":"error","msg":"Attempt 1/3 failed with 1 errors and: can't copy - source file is being updated"}`)
		fmt.Fprintln(stderr, `{"level":"notice","msg":"Attempt 2/3 succeeded"}`)
		return nil, nil
	}
	logged := captureBackupLog(t)

	res := fx.manager.syncDir(t.Context(), t.TempDir(), backupFixtureBase, "", "manual", -1)
	if !res.OK {
		t.Errorf("result = %+v, want OK", res)
	}
	if backupFlagValue(got, "--retries") != "3" || backupFlagValue(got, "--retries-sleep") != "10s" {
		t.Errorf("sync args %q, want --retries 3 --retries-sleep 10s", got)
	}
	if all := strings.Join(logged(), "\n"); !strings.Contains(all, "rclone tried 1 more time(s)") {
		t.Errorf("log = %q, want it to say rclone retried", all)
	}
}
