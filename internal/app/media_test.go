package app

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"

	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// TestBrainTakesItsFilesIntoItsInbox: a message's files move into the inbox
// of the brain that takes it, and the history names them there. Another
// brain's inbox gets nothing.
func TestBrainTakesItsFilesIntoItsInbox(t *testing.T) {
	tb := newTestBridge(t, "")
	dmDir := tb.addFolderBrain(t, manifest.Manifest{ID: "dm", Name: "DM", Routing: manifest.Routing{HandlesDMs: true}})
	tb.addFolderBrain(t, manifest.Manifest{ID: "g", Name: "Group", Routing: manifest.Routing{Groups: []string{"120363000000000000@g.us"}}})
	media := tb.cfgs.Get().MediaDir
	p := writeTestFile(t, filepath.Join(media, mediafiles.IncomingDir, "voice.ogg"), "OggS")

	tb.handleInbound(t.Context(), brain.Inbound{Inbound: chat.Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "listen", Media: []string{p}, Source: "whatsapp", At: time.Now()}})
	testutil.WaitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.dev.Outbox()) > 0 })

	want := filepath.Join(media, "dm", "voice.ogg")
	if data, err := os.ReadFile(want); err != nil || string(data) != "OggS" {
		t.Errorf("%s: %q, %v", want, data, err)
	}
	if hist, _ := os.ReadFile(filepath.Join(dmDir, history.FileName)); !strings.Contains(string(hist), "[attachment: "+want+"]") {
		t.Errorf("history does not name the file in the inbox:\n%s", hist)
	}
	if names, _ := os.ReadDir(filepath.Join(media, "g")); len(names) != 0 {
		t.Errorf("the group brain's inbox got %v", names)
	}
}

// TestRemovedBrainHandsItsFilesOn: a message that lands on a brain being
// removed at that moment is routed again, to the brain that now has its
// chat. The removed brain had already moved the message's files into its
// inbox, and the retry named them where they had been, so the next brain
// got paths that no longer existed and the files stayed in the removed
// brain's inbox. The retry now takes them from there.
func TestRemovedBrainHandsItsFilesOn(t *testing.T) {
	mgr, d, home := queueTestManager(t, 0, "")
	// The same brains, with a removed and b taking the direct messages.
	next, err := LoadConfig(writeConfig(t, `{
		"brain_mode": true,
		"data_dir": "`+home+`/data",
		"claude_path": "`+os.Args[0]+`"`+brainDirsJSON(t,
		brainFolder(t, home+"/b", manifest.Manifest{ID: "b", Routing: manifest.Routing{HandlesDMs: true}}),
	)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	// Between a's taking the files and its finding out that it is removed:
	// its history line is written in between.
	mgr.SetHooks(BrainHooks{Deliver: d.add, OnHistory: func(id string, _ history.Entry) {
		if id != "a" {
			return
		}
		once.Do(func() {
			if err := mgr.Apply(next); err != nil {
				t.Error(err)
			}
			// Apply freezes the removed queue before returning; its worker
			// may still finish delivery in the background.
			if mgr.Get("a") != nil {
				t.Error("removed brain remained routable")
			}
		})
	}})
	media := next.MediaDir
	p := writeTestFile(t, filepath.Join(media, mediafiles.IncomingDir, "voice.ogg"), "OggS")
	m := dm("listen")
	m.Media = []string{p}
	br, _, _, err := mgr.Submit(m)
	if err != nil || br == nil || br.ID() != "b" {
		t.Fatalf("Submit = %v, %v; want brain b to take it", br, err)
	}
	want := filepath.Join(media, "b", "voice.ogg")
	if data, err := os.ReadFile(want); err != nil || string(data) != "OggS" {
		t.Errorf("%s: %q, %v; want the file in b's inbox", want, data, err)
	}
	if names := dirNames(t, filepath.Join(media, "a")); len(names) != 0 {
		t.Errorf("the removed brain's inbox kept %q", names)
	}
	testutil.WaitFor(t, 10*time.Second, "b's run", func() bool { return d.count() == 1 })
	if got := d.all()[0].msg.Media; !slices.Equal(got, []string{want}) {
		t.Errorf("b's run got the files %q, want [%s]", got, want)
	}
}

// TestMergedMessageNamesItsFilesInTheInbox: a message the debouncer merged
// (an album, a caption and its files) keeps its parts, so that an edit or
// a deletion of one can merge the others again, and the history has a line
// per part. The files moved into the brain's inbox, but only the merged
// message's own list said so: every part named its file in
// media/.incoming/, where it had been downloaded. The history lines named
// files that were gone, and so did the run of a message one of whose parts
// was edited or deleted while it waited.
func TestMergedMessageNamesItsFilesInTheInbox(t *testing.T) {
	cases := []struct {
		name   string
		change *chat.Change // applied while the message waits
		want   []string     // the files the run gets, in the inbox
	}{
		{"as merged", nil, []string{"one.jpg", "two.jpg"}},
		{"one part edited", &chat.Change{Kind: chat.ChangeEdit, Target: "3EB0ONE", Text: "the first photo"}, []string{"one.jpg", "two.jpg"}},
		{"one part deleted", &chat.Change{Kind: chat.ChangeRevoke, Target: "3EB0ONE"}, []string{"two.jpg"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, d, home := queueTestManager(t, 30000, "")
			br := mgr.Get("a")
			if _, _, _, err := mgr.Submit(dm("a long report")); err != nil {
				t.Fatal(err)
			}
			testutil.WaitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })

			media := filepath.Join(home, "data", "media")
			inbox := mediaInboxAt(media).Path("a")
			part := func(id, file string) brain.Inbound {
				m := dm("")
				m.ID = id
				m.Media = []string{writeTestFile(t, filepath.Join(media, mediafiles.IncomingDir, file), file)}
				return m
			}
			m := part("3EB0ONE", "one.jpg").Merge(part("3EB0TWO", "two.jpg"))
			if _, _, _, err := mgr.Submit(m); err != nil {
				t.Fatal(err)
			}
			if c.change != nil {
				ch := *c.change
				ch.Chat, ch.Sender = m.JID, m.Sender
				waiting, edited := br.Change(ch)
				if !waiting {
					t.Fatal("the merged message was not waiting")
				}
				br.RecordChange(ch, waiting, edited)
			}

			// Every file the history names is in the inbox.
			var named []string
			for _, line := range historyTexts(t, br, history.In) {
				for _, m := range attachmentRe.FindAllStringSubmatch(line, -1) {
					named = append(named, m[1])
				}
			}
			var want []string
			for _, f := range c.want {
				want = append(want, filepath.Join(inbox, f))
			}
			if !slices.Equal(named, want) {
				t.Errorf("the history names the files %q, want %q", named, want)
			}

			t.Setenv("FRIDOLIN_FAKE_SLEEP_MS", "10")
			br.StopCurrent(false)
			testutil.WaitFor(t, 10*time.Second, "the merged message's run", func() bool { return d.count() == 2 })
			got := d.all()[1].msg.Media
			if !slices.Equal(got, want) {
				t.Errorf("the run got the files %q, want %q", got, want)
			}
			for _, p := range got {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("the run's file: %v", err)
				}
			}
		})
	}
}

// attachmentRe finds the files a history line names (historyTextWithMedia).
var attachmentRe = regexp.MustCompile(`\[attachment: ([^\]\n]+)\]`)

// writeTestFile writes content to p, creating its folder, and returns p.
func writeTestFile(t *testing.T, p, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCheckLeavesTheMediaFolderAlone: check --print-sandbox and --route
// load the brains as the bridge does, and that used to move what older
// versions left in the media folder aside. Run with a new build against
// the config of a bridge still running, as the upgrade notes say, it
// reorganised that bridge's media folder under it, and set the marker, so
// the new bridge never moved what the old one downloaded meanwhile. Now
// only the bridge's start and a brain's first inbox do it.
func TestCheckLeavesTheMediaFolderAlone(t *testing.T) {
	home := fsx.Canonical(t.TempDir())
	t.Setenv("HOME", home)
	path := writeConfig(t, `{
		"brain_mode": true,
		"claude_path": "`+home+`/no-claude",
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/personal", manifest.Manifest{ID: "personal", Routing: manifest.Routing{HandlesDMs: true}}),
	)+`}`)
	media := filepath.Join(home, "data", "media")
	before := processStarted.Add(-time.Hour)
	for _, f := range []string{"old.jpg", "plaud/2026-01-15 standup.txt"} {
		p := writeTestFile(t, filepath.Join(media, f), "content of "+f)
		if err := os.Chtimes(p, before, before); err != nil {
			t.Fatal(err)
		}
	}
	o := CheckOptions{Config: path, Routes: []string{"15551234567@s.whatsapp.net"}}
	if runtime.GOOS == "darwin" {
		o.SandboxFor = "personal"
	}
	if err := WriteCheck(io.Discard, o); err != nil {
		t.Fatal(err)
	}
	if names := dirNames(t, media); !slices.Equal(names, []string{"old.jpg", "plaud"}) {
		t.Errorf("check left the media folder holding %q, want it as it was", names)
	}
	// The bridge itself still does it when it starts.
	cfgs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Options{Config: cfgs, NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	if names := dirNames(t, media); !slices.Contains(names, mediafiles.InboxesMarker) || slices.Contains(names, "old.jpg") || slices.Contains(names, "plaud") {
		t.Errorf("after the bridge started the media folder holds %q, want what was there moved aside", names)
	}
}

// TestLegacyMediaThatCannotMoveStays: one entry of the media folder that
// could not be moved aside (a folder without write permission here) failed
// the whole move, every time, and with it every brain's every run, as
// "sandbox unavailable". The move now goes on with the rest and leaves
// such an entry where it is; only a brain named like it gets no inbox, and
// no run, until it is gone. A media folder that cannot be readied at all
// leaves runs without an inbox rather than refusing them.
func TestLegacyMediaThatCannotMoveStays(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root moves any folder")
	}
	root := fsx.Canonical(t.TempDir())
	data, media := filepath.Join(root, "data"), filepath.Join(root, "data", "media")
	before := processStarted.Add(-time.Hour)
	for _, f := range []string{"old.jpg", "plaud/standup.txt", "legacy/kept.txt"} {
		p := writeTestFile(t, filepath.Join(media, f), "content of "+f)
		if err := os.Chtimes(p, before, before); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(media, "legacy")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if _, err := prepareBrainState(data, media, "a", false); err != nil {
		t.Fatalf("brain a's run was refused: %v", err)
	}
	if names := dirNames(t, media); !slices.Equal(names, []string{mediafiles.InboxesMarker, mediafiles.LegacyDir, "a", "legacy"}) {
		t.Errorf("the media folder holds %q, want all but legacy moved aside", names)
	}
	if names := dirNames(t, filepath.Join(media, mediafiles.LegacyDir)); !slices.Equal(names, []string{"old.jpg", "plaud"}) {
		t.Errorf("%s holds %q", mediafiles.LegacyDir, names)
	}
	if _, err := mediaInboxAt(media).Open("legacy"); !errors.Is(err, mediafiles.ErrNotTheInbox) {
		t.Errorf("mediaInboxAt(legacy).Open() = %v, want %v", err, mediafiles.ErrNotTheInbox)
	}
	if _, err := prepareBrainState(data, media, "legacy", false); !errors.Is(err, mediafiles.ErrNotTheInbox) {
		t.Errorf("brain legacy's run: %v, want it refused with %v", err, mediafiles.ErrNotTheInbox)
	}
	if data, err := os.ReadFile(filepath.Join(locked, "kept.txt")); err != nil || string(data) != "content of legacy/kept.txt" {
		t.Errorf("what stayed: %q, %v", data, err)
	}
	// Moved out by hand, the name is free for an inbox again.
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(locked, filepath.Join(root, "moved-out")); err != nil {
		t.Fatal(err)
	}
	inbox, err := mediaInboxAt(media).Open("legacy")
	if err != nil {
		t.Fatalf("mediaInboxAt(legacy).Open() once it was moved out: %v", err)
	}
	inbox.Close()
	if names := dirNames(t, filepath.Join(media, "legacy")); len(names) != 0 {
		t.Errorf("brain legacy's new inbox holds %q", names)
	}
	if _, err := os.Stat(filepath.Join(media, mediafiles.InboxesMarker, mediafiles.StuckFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the list of what stayed is still there: %v", err)
	}

	t.Run("a media folder that cannot be readied", func(t *testing.T) {
		root := fsx.Canonical(t.TempDir())
		data, media := filepath.Join(root, "data"), filepath.Join(root, "data", "media")
		if err := os.MkdirAll(media, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(media, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(media, 0o700) })
		if _, err := mediaInboxAt(media).Open("a"); err == nil || errors.Is(err, mediafiles.ErrNotTheInbox) {
			t.Fatalf("openInbox = %v, want it to fail readying the folder", err)
		}
		env, err := prepareBrainState(data, media, "a", false)
		if err != nil || !slices.Contains(env, "TMPDIR="+filepath.Join(data, "brains", "a", "tmp")) {
			t.Errorf("brain a's run: %v (env %q), want it to go ahead without an inbox", err, env)
		}
	})
}
