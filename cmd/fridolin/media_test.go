package main

import (
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestClaimMedia: a brain's files move from wherever they arrived in the
// media folder into its own inbox, the only part of it its sandbox opens,
// and nothing already there is replaced or followed. Files elsewhere, or
// in the inbox already, stay where they are.
func TestClaimMedia(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, media string) string // returns the path handed in
		want  string                                  // relative to the media folder, "" for unchanged
		check func(t *testing.T, media string)
	}{
		{"downloaded attachment", func(t *testing.T, media string) string {
			return writeTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "%PDF")
		}, "a/scan.pdf", nil},
		{"dropped by a script into a folder of its own", func(t *testing.T, media string) string {
			return writeTestFile(t, filepath.Join(media, "upload", "scan.pdf"), "%PDF")
		}, "a/scan.pdf", nil},
		{"at the top of the media folder", func(t *testing.T, media string) string {
			return writeTestFile(t, filepath.Join(media, "note.txt"), "x")
		}, "a/note.txt", nil},
		{"already in its inbox", func(t *testing.T, media string) string {
			return writeTestFile(t, filepath.Join(media, "a", "plaud", "t.txt"), "x")
		}, "", nil},
		{"outside the media folder", func(t *testing.T, media string) string {
			return writeTestFile(t, filepath.Join(filepath.Dir(media), "elsewhere.txt"), "x")
		}, "", nil},
		{"a name already taken in the inbox", func(t *testing.T, media string) string {
			writeTestFile(t, filepath.Join(media, "a", "scan.pdf"), "older")
			return writeTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "newer")
		}, "a/scan-2.pdf", func(t *testing.T, media string) {
			if data, _ := os.ReadFile(filepath.Join(media, "a", "scan.pdf")); string(data) != "older" {
				t.Errorf("the file already in the inbox was replaced: %q", data)
			}
		}},
		// A brain may plant anything in its own inbox, a symlink where its
		// next file will go among them; the file lands beside it.
		{"a symlink planted in the inbox", func(t *testing.T, media string) string {
			target := writeTestFile(t, filepath.Join(filepath.Dir(media), "outside.txt"), "keep")
			if err := os.MkdirAll(filepath.Join(media, "a"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(media, "a", "scan.pdf")); err != nil {
				t.Fatal(err)
			}
			return writeTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "%PDF")
		}, "a/scan-2.pdf", func(t *testing.T, media string) {
			if data, _ := os.ReadFile(filepath.Join(filepath.Dir(media), "outside.txt")); string(data) != "keep" {
				t.Errorf("written through the symlink: %q", data)
			}
		}},
		// The inbox itself must be a folder: one that is a symlink (to
		// another brain's inbox, say) gets nothing.
		{"an inbox that is a symlink", func(t *testing.T, media string) string {
			if err := os.MkdirAll(filepath.Join(media, "b"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(media, "b"), filepath.Join(media, "a")); err != nil {
				t.Fatal(err)
			}
			return writeTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "%PDF")
		}, "", func(t *testing.T, media string) {
			if names := dirNames(t, filepath.Join(media, "b")); len(names) != 0 {
				t.Errorf("moved into the symlink's target: %q", names)
			}
		}},
		{"not a regular file", func(t *testing.T, media string) string {
			dir := filepath.Join(media, "upload", "folder")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			media := filepath.Join(canonicalDir(t.TempDir()), "media")
			// Ready for inboxes, as the bridge has it before any message,
			// so what the case puts there stays.
			if err := retireLegacyMedia(media); err != nil {
				t.Fatal(err)
			}
			p := tc.setup(t, media)
			got := claimMedia(media, "a", []string{p})
			want := map[string]string{}
			if tc.want != "" {
				want[p] = filepath.Join(media, tc.want)
			}
			if !maps.Equal(got, want) {
				t.Fatalf("claimMedia = %q, want %q", got, want)
			}
			if dst, ok := want[p]; ok {
				if _, err := os.Lstat(p); !os.IsNotExist(err) {
					t.Errorf("the file is still at %s: %v", p, err)
				}
				if fi, err := os.Lstat(dst); err != nil || !fi.Mode().IsRegular() {
					t.Errorf("%s is not a regular file: %v, %v", dst, fi, err)
				}
			}
			if tc.check != nil {
				tc.check(t, media)
			}
		})
	}

	t.Run("the media folder by its real path", func(t *testing.T) {
		root := canonicalDir(t.TempDir())
		real := filepath.Join(root, "real-media")
		if err := os.MkdirAll(filepath.Join(real, incomingMediaDir), 0o700); err != nil {
			t.Fatal(err)
		}
		media := filepath.Join(root, "media")
		if err := os.Symlink(real, media); err != nil {
			t.Fatal(err)
		}
		p := writeTestFile(t, filepath.Join(real, incomingMediaDir, "x.jpg"), "x")
		if got := claimMedia(media, "a", []string{p}); !maps.Equal(got, map[string]string{p: filepath.Join(media, "a", "x.jpg")}) {
			t.Errorf("claimMedia = %q", got)
		}
	})
}

// TestBrainTakesItsFilesIntoItsInbox: a message's files move into the inbox
// of the brain that takes it, and the history names them there. Another
// brain's inbox gets nothing.
func TestBrainTakesItsFilesIntoItsInbox(t *testing.T) {
	tb := newTestBridge(t, "")
	dmDir := tb.addFolderBrain(t, BrainManifest{ID: "dm", Name: "DM", Routing: BrainRouting{HandlesDMs: true}})
	tb.addFolderBrain(t, BrainManifest{ID: "g", Name: "Group", Routing: BrainRouting{Groups: []string{"120363000000000000@g.us"}}})
	media := tb.cfgs.Get().MediaDir
	p := writeTestFile(t, filepath.Join(media, incomingMediaDir, "voice.ogg"), "OggS")

	tb.handleInbound(t.Context(), Inbound{JID: "15551234567@s.whatsapp.net", Sender: "15551234567", Text: "listen", Media: []string{p}, Source: "whatsapp", At: time.Now()})
	waitFor(t, 10*time.Second, "the reply", func() bool { return len(tb.dev.Outbox()) > 0 })

	want := filepath.Join(media, "dm", "voice.ogg")
	if data, err := os.ReadFile(want); err != nil || string(data) != "OggS" {
		t.Errorf("%s: %q, %v", want, data, err)
	}
	if hist, _ := os.ReadFile(filepath.Join(dmDir, historyFileName)); !strings.Contains(string(hist), "[attachment: "+want+"]") {
		t.Errorf("history does not name the file in the inbox:\n%s", hist)
	}
	if names, _ := os.ReadDir(filepath.Join(media, "g")); len(names) != 0 {
		t.Errorf("the group brain's inbox got %v", names)
	}
}

// TestLegacyMediaFoldersMoveAside: an inbox is the folder named after its
// brain, and older versions kept other folders in the media folder: plaud/
// with every brain's Plaud transcripts, folders scripts made for /ingest.
// A brain called plaud got the first as its inbox and read them all. They
// also downloaded every chat's attachments to the top, where any brain
// could have /ingest hand it one by guessing its name (a document keeps
// its sender's, scan.pdf). The first inbox now moves such folders, and the
// files at the top older than the bridge, into media/.old/, which no brain
// can read; a file a script dropped there since and the bridge's own dot
// folders stay, and so do the inboxes made after it.
func TestLegacyMediaFoldersMoveAside(t *testing.T) {
	media := filepath.Join(canonicalDir(t.TempDir()), "media")
	for _, f := range []string{"plaud/2026-01-15 standup.txt", "upload/scan.pdf", "top.pdf", incomingMediaDir + "/voice.ogg", legacyMediaDir + "/upload/older.pdf"} {
		writeTestFile(t, filepath.Join(media, f), "content of "+f)
	}
	// Downloaded by an older version, before this bridge started.
	before := processStarted.Add(-time.Hour)
	for _, f := range []string{"invoice.pdf", "3EB0A1B2.jpg", "older.pdf"} {
		p := writeTestFile(t, filepath.Join(media, f), "content of "+f)
		if err := os.Chtimes(p, before, before); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(media, legacyMediaDir, "older.pdf"), "an older one")
	inbox, err := openInbox(media, "plaud")
	if err != nil {
		t.Fatal(err)
	}
	inbox.Close()
	if names := dirNames(t, filepath.Join(media, "plaud")); len(names) != 0 {
		t.Errorf("brain plaud's inbox holds %q, want it new", names)
	}
	for _, f := range []string{
		legacyMediaDir + "/plaud/2026-01-15 standup.txt",
		legacyMediaDir + "/upload-2/scan.pdf", // .old/upload was taken
		legacyMediaDir + "/upload/older.pdf",
		legacyMediaDir + "/invoice.pdf",
		legacyMediaDir + "/3EB0A1B2.jpg",
		legacyMediaDir + "/older-2.pdf", // .old/older.pdf was taken
		"top.pdf",
		incomingMediaDir + "/voice.ogg",
		mediaInboxesMarker,
	} {
		if _, err := os.Stat(filepath.Join(media, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if names := dirNames(t, media); !slices.Equal(names, []string{mediaInboxesMarker, incomingMediaDir, legacyMediaDir, "plaud", "top.pdf"}) {
		t.Errorf("the media folder holds %q, want only the new file at its top", names)
	}
	// The marker is a folder, which /ingest and claimMedia, taking
	// regular files only, cannot move into a brain's inbox: the next
	// inbox would then have moved every inbox aside.
	marker := filepath.Join(media, mediaInboxesMarker)
	if fi, err := os.Lstat(marker); err != nil || !fi.IsDir() {
		t.Errorf("%s is not a folder: %v", marker, err)
	}
	if got := claimMedia(media, "plaud", []string{marker}); len(got) != 0 {
		t.Errorf("claimMedia moved the marker: %q", got)
	}
	// Once the folder is ready, every folder in it is an inbox and stays.
	writeTestFile(t, filepath.Join(media, "notes", "photo.jpg"), "x")
	inbox, err = openInbox(media, "notes")
	if err != nil {
		t.Fatal(err)
	}
	inbox.Close()
	if names := dirNames(t, filepath.Join(media, "notes")); !slices.Equal(names, []string{"photo.jpg"}) {
		t.Errorf("brain notes' inbox holds %q, want its photo", names)
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
		brainFolder(t, home+"/b", BrainManifest{ID: "b", Routing: BrainRouting{HandlesDMs: true}}),
	)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	a := mgr.Get("a")
	var once sync.Once
	// Between a's taking the files and its finding out that it is removed:
	// its history line is written in between.
	mgr.SetHooks(BrainHooks{Deliver: d.add, OnHistory: func(id string, _ historyEntry) {
		if id != "a" {
			return
		}
		once.Do(func() {
			if err := mgr.Apply(next); err != nil {
				t.Error(err)
			}
			waitFor(t, 10*time.Second, "brain a to be removed", func() bool {
				a.mu.Lock()
				defer a.mu.Unlock()
				return a.retired
			})
		})
	}})
	media := next.MediaDir
	p := writeTestFile(t, filepath.Join(media, incomingMediaDir, "voice.ogg"), "OggS")
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
	waitFor(t, 10*time.Second, "b's run", func() bool { return d.count() == 1 })
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
		change *messageChange // applied while the message waits
		want   []string       // the files the run gets, in the inbox
	}{
		{"as merged", nil, []string{"one.jpg", "two.jpg"}},
		{"one part edited", &messageChange{Kind: changeEdit, Target: "3EB0ONE", Text: "the first photo"}, []string{"one.jpg", "two.jpg"}},
		{"one part deleted", &messageChange{Kind: changeRevoke, Target: "3EB0ONE"}, []string{"two.jpg"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr, d, _ := queueTestManager(t, 30000, "")
			br := mgr.Get("a")
			if _, _, _, err := mgr.Submit(dm("a long report")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 5*time.Second, "the first run", func() bool { r := br.Snapshot().Running; return r != nil && r.PID > 0 })

			media := br.settings.Load().mediaDir
			inbox := mediaInbox(media, "a")
			part := func(id, file string) Inbound {
				m := dm("")
				m.ID = id
				m.Media = []string{writeTestFile(t, filepath.Join(media, incomingMediaDir, file), file)}
				return m
			}
			m := part("3EB0ONE", "one.jpg").merge(part("3EB0TWO", "two.jpg"))
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
				br.recordChange(ch, waiting, edited)
			}

			// Every file the history names is in the inbox.
			var named []string
			for _, line := range historyTexts(t, br, historyIn) {
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
			waitFor(t, 10*time.Second, "the merged message's run", func() bool { return d.count() == 2 })
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
	home := canonicalDir(t.TempDir())
	t.Setenv("HOME", home)
	path := writeConfig(t, `{
		"brain_mode": true,
		"claude_path": "`+home+`/no-claude",
		"data_dir": "`+home+`/data"`+brainDirsJSON(t,
		brainFolder(t, home+"/personal", BrainManifest{ID: "personal", Routing: BrainRouting{HandlesDMs: true}}),
	)+`}`)
	media := filepath.Join(home, "data", "media")
	before := processStarted.Add(-time.Hour)
	for _, f := range []string{"old.jpg", "plaud/2026-01-15 standup.txt"} {
		p := writeTestFile(t, filepath.Join(media, f), "content of "+f)
		if err := os.Chtimes(p, before, before); err != nil {
			t.Fatal(err)
		}
	}
	o := checkOptions{config: path, routes: []string{"15551234567@s.whatsapp.net"}}
	if runtime.GOOS == "darwin" {
		o.sandboxFor = "personal"
	}
	if err := writeCheck(io.Discard, o); err != nil {
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
	b, err := NewBridge(cfgs, BridgeOptions{NoWhatsApp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	if names := dirNames(t, media); !slices.Contains(names, mediaInboxesMarker) || slices.Contains(names, "old.jpg") || slices.Contains(names, "plaud") {
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
	root := canonicalDir(t.TempDir())
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
	if names := dirNames(t, media); !slices.Equal(names, []string{mediaInboxesMarker, legacyMediaDir, "a", "legacy"}) {
		t.Errorf("the media folder holds %q, want all but legacy moved aside", names)
	}
	if names := dirNames(t, filepath.Join(media, legacyMediaDir)); !slices.Equal(names, []string{"old.jpg", "plaud"}) {
		t.Errorf("%s holds %q", legacyMediaDir, names)
	}
	if _, err := openInbox(media, "legacy"); !errors.Is(err, errNotTheInbox) {
		t.Errorf("openInbox(legacy) = %v, want %v", err, errNotTheInbox)
	}
	if _, err := prepareBrainState(data, media, "legacy", false); !errors.Is(err, errNotTheInbox) {
		t.Errorf("brain legacy's run: %v, want it refused with %v", err, errNotTheInbox)
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
	inbox, err := openInbox(media, "legacy")
	if err != nil {
		t.Fatalf("openInbox(legacy) once it was moved out: %v", err)
	}
	inbox.Close()
	if names := dirNames(t, filepath.Join(media, "legacy")); len(names) != 0 {
		t.Errorf("brain legacy's new inbox holds %q", names)
	}
	if _, err := os.Stat(filepath.Join(media, mediaInboxesMarker, mediaStuckFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the list of what stayed is still there: %v", err)
	}

	t.Run("a media folder that cannot be readied", func(t *testing.T) {
		root := canonicalDir(t.TempDir())
		data, media := filepath.Join(root, "data"), filepath.Join(root, "data", "media")
		if err := os.MkdirAll(media, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(media, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(media, 0o700) })
		if _, err := openInbox(media, "a"); err == nil || errors.Is(err, errNotTheInbox) {
			t.Fatalf("openInbox = %v, want it to fail readying the folder", err)
		}
		env, err := prepareBrainState(data, media, "a", false)
		if err != nil || !slices.Contains(env, "TMPDIR="+filepath.Join(data, "brains", "a", "tmp")) {
			t.Errorf("brain a's run: %v (env %q), want it to go ahead without an inbox", err, env)
		}
	})
}
