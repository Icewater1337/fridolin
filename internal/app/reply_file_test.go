package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/devchat"
	"github.com/Icewater1337/fridolin/internal/mcp"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/store"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// replyFile calls reply_file as run of br, and returns what it answered
// and whether that is an error.
func replyFile(t *testing.T, tb *testBridge, br *brain.Brain, run runInfo, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	run.Brain = br.ID()
	c := newMCPCaller(run, br.Manifest(), func(string) bool { return true })
	res, err := tb.mcpTools.Call(context.Background(), c, "reply_file", raw)
	if err != nil {
		t.Fatalf("reply_file: %v", err)
	}
	return res.Content[0].Text, res.IsError
}

// dmRun is a run that answers a DM someone wrote.
func dmRun() runInfo {
	return runInfoFor("", chat.Inbound{JID: testDMChat, Sender: "15551234567", Text: "send me the chart", Source: chat.SourceWhatsApp})
}

// writeBrainFile writes data to name in br's folder and returns its path.
func writeBrainFile(t *testing.T, br *brain.Brain, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(br.Dir(), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// testPNG is a small PNG image.
func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testOgg is Ogg Opus audio that plays seconds seconds.
func testOgg(seconds uint64) []byte {
	page := func(granule uint64, packet []byte) []byte {
		h := make([]byte, 27)
		copy(h, "OggS")
		binary.LittleEndian.PutUint64(h[6:], granule)
		h[26] = 1
		return append(append(h, byte(len(packet))), packet...)
	}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	return append(page(0, head), page(seconds*48000, []byte{1})...)
}

// sentFiles are the files the dev chat got, oldest first.
func sentFiles(tb *testBridge, jid string) []devchat.Message {
	var out []devchat.Message
	box := tb.dev.Outbox()
	for i := len(box) - 1; i >= 0; i-- {
		if box[i].JID == jid && box[i].File != "" {
			out = append(out, box[i])
		}
	}
	return out
}

// shelved is what the outbox's shelf holds.
func shelved(t *testing.T, tb *testBridge) []string {
	t.Helper()
	entries, err := os.ReadDir(tb.outbox.Shelf().Dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A brain sends a file from its folder into the chat it answers: a PNG as
// an image, a PDF or anything else as a document, each with its caption,
// out of the outbox's copy, which goes once it went out. The brain's
// history records each with a line that names it.
func TestReplyFileSendsIntoTheRunsChat(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, "")
	photo := writeBrainFile(t, br, "charts/sales.png", testPNG(t))
	report := writeBrainFile(t, br, "report.pdf", []byte("%PDF-1.7\n"))
	data := writeBrainFile(t, br, "data.json", []byte(`{"a": 1}`))
	for _, c := range []struct {
		args map[string]any
		as   string
	}{
		{map[string]any{"path": photo, "caption": "Sales by month"}, chat.FileImage},
		{map[string]any{"path": report}, chat.FileDocument},
		{map[string]any{"path": data, "as": "document", "caption": "The numbers"}, chat.FileDocument},
		{map[string]any{"path": photo, "as": "document"}, chat.FileDocument},
	} {
		got, isErr := replyFile(t, tb, br, dmRun(), c.args)
		if isErr || !strings.Contains(got, `"as": "`+c.as+`"`) {
			t.Errorf("reply_file %v: %s", c.args, got)
		}
	}
	testutil.WaitFor(t, 10*time.Second, "the files", func() bool { return len(sentFiles(tb, testDMChat)) == 4 })
	var got []string
	for _, m := range sentFiles(tb, testDMChat) {
		got = append(got, m.Kind+" "+m.File+" "+m.Text)
	}
	want := []string{"image sales.png Sales by month", "document report.pdf ", "document data.json The numbers", "document sales.png "}
	if !slices.Equal(got, want) {
		t.Errorf("sent %q, want %q", got, want)
	}
	testutil.WaitFor(t, 5*time.Second, "the history", func() bool { return len(outbound(t, br)[testDMChat]) == 4 })
	if h := outbound(t, br)[testDMChat]; h[0] != "[sent image: sales.png]\nSales by month" || h[1] != "[sent document: report.pdf]" {
		t.Errorf("history = %q", h)
	}
	testutil.WaitFor(t, 5*time.Second, "the copies to go", func() bool { return len(shelved(t, tb)) == 0 })
}

// Whether a file shows as a photo is up to its content, not its name: a
// text file named report.jpg and random bytes named x.png go as documents,
// and as an image they are refused; a real JPEG and PNG still go as images.
func TestReplyFilePhotoIsWhatTheContentSays(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, "")
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 20, 10)), nil); err != nil {
		t.Fatal(err)
	}
	text := writeBrainFile(t, br, "report.jpg", []byte("hello\n"))
	noise := writeBrainFile(t, br, "x.png", []byte{0x13, 0x37, 0x00, 0xfe, 0x42, 0x99, 0x01, 0x7f})
	for _, c := range []struct {
		path, as, mime string
	}{
		{text, chat.FileDocument, "text/plain"},
		{noise, chat.FileDocument, "application/octet-stream"},
		{writeBrainFile(t, br, "photo.jpg", jpg.Bytes()), chat.FileImage, "image/jpeg"},
		{writeBrainFile(t, br, "chart.png", testPNG(t)), chat.FileImage, "image/png"},
	} {
		got, isErr := replyFile(t, tb, br, dmRun(), map[string]any{"path": c.path})
		if isErr || !strings.Contains(got, `"as": "`+c.as+`"`) || !strings.Contains(got, `"mime": "`+c.mime+`"`) {
			t.Errorf("%s: %s, want it as %s, %s", filepath.Base(c.path), got, c.as, c.mime)
		}
	}
	for _, p := range []string{text, noise} {
		got, isErr := replyFile(t, tb, br, dmRun(), map[string]any{"path": p, "as": "image"})
		if !isErr || !strings.Contains(got, "only a JPEG or PNG shows as an image") || !strings.Contains(got, "send it as a document") {
			t.Errorf("%s as an image: %s, want it refused", filepath.Base(p), got)
		}
	}
	testutil.WaitFor(t, 10*time.Second, "the files", func() bool { return len(sentFiles(tb, testDMChat)) == 4 })
	testutil.WaitFor(t, 5*time.Second, "the copies to go", func() bool { return len(shelved(t, tb)) == 0 })
}

// A voice note goes as it is when it is Ogg Opus, with its length; other
// audio is made one with ffmpeg; without ffmpeg it goes as a document,
// which the answer says. A failed conversion sends nothing.
func TestReplyFileVoiceNotes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ogg := filepath.Join(dir, "converted.ogg")
	if err := os.WriteFile(ogg, testOgg(4), 0o600); err != nil {
		t.Fatal(err)
	}
	script := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := script("ffmpeg-good", "cat >/dev/null; cat '"+ogg+"'")
	bad := script("ffmpeg-bad", "cat >/dev/null; echo 'Invalid data found' >&2; exit 1")
	var ffmpeg string
	optionsOf(t).hooks.ffmpeg = func() string { return ffmpeg }
	tb, br := newMainBridge(t, "")
	speech := writeBrainFile(t, br, "speech.mp3", []byte("ID3\x03fake mp3"))
	note := writeBrainFile(t, br, "note.ogg", testOgg(3))

	got, isErr := replyFile(t, tb, br, dmRun(), map[string]any{"path": note, "as": "voice"})
	if isErr || !strings.Contains(got, `"as": "voice"`) {
		t.Errorf("an Ogg Opus voice note: %s", got)
	}
	got, isErr = replyFile(t, tb, br, dmRun(), map[string]any{"path": speech, "as": "voice"})
	if isErr || !strings.Contains(got, `"as": "document"`) || !strings.Contains(got, "ffmpeg is not installed") {
		t.Errorf("without ffmpeg: %s", got)
	}
	ffmpeg = good
	got, isErr = replyFile(t, tb, br, dmRun(), map[string]any{"path": speech, "as": "voice"})
	if isErr || !strings.Contains(got, `"as": "voice"`) || !strings.Contains(got, `"mime": "audio/ogg"`) {
		t.Errorf("made with ffmpeg: %s", got)
	}
	testutil.WaitFor(t, 10*time.Second, "the voice notes", func() bool { return len(sentFiles(tb, testDMChat)) == 3 })
	var kinds []string
	for _, m := range sentFiles(tb, testDMChat) {
		kinds = append(kinds, m.Kind+" "+m.File)
	}
	if want := []string{"voice note.ogg", "document speech.mp3", "voice speech.mp3"}; !slices.Equal(kinds, want) {
		t.Errorf("sent %q, want %q", kinds, want)
	}
	// The outbox takes a reply out only after its last part went out, so
	// the row may still be there for a moment after the send shows.
	testutil.WaitFor(t, 10*time.Second, "an empty outbox", func() bool {
		rows, err := tb.store.PendingOutbox(context.Background())
		return err == nil && len(rows) == 0
	})

	ffmpeg = bad
	got, isErr = replyFile(t, tb, br, dmRun(), map[string]any{"path": speech, "as": "voice"})
	if !isErr || !strings.Contains(got, "could not make a voice note of it") || !strings.Contains(got, "Invalid data found") {
		t.Errorf("a failed conversion: %s", got)
	}
	testutil.WaitFor(t, 5*time.Second, "the copies to go", func() bool { return len(shelved(t, tb)) == 0 })
}

// What a run has seconds for: the voice notes' lengths go along.
func TestReplyFileVoiceNoteLength(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, "")
	tb.wa = newFlakyMessenger(func() int { return 4000 }, false) // holds it in the outbox
	note := writeBrainFile(t, br, "note.ogg", testOgg(9))
	if got, isErr := replyFile(t, tb, br, dmRun(), map[string]any{"path": note, "as": "voice"}); isErr {
		t.Fatal(got)
	}
	rows, err := tb.store.PendingOutbox(context.Background())
	if err != nil || len(rows) != 1 || len(rows[0].Parts) != 1 || rows[0].Parts[0].File == nil {
		t.Fatalf("outbox = %+v, %v", rows, err)
	}
	if f := rows[0].Parts[0].File; f.Seconds != 9 || f.Kind != chat.FileVoice || f.Mime != "audio/ogg" || f.Name != "note.ogg" {
		t.Errorf("the file = %+v", f)
	}
}

// reply_file sends only a file of the brain's own, of a kind it can
// send as asked, within max_media_mb; a link out of its folder, a path
// elsewhere and a relative one are refused, and so is a caption it cannot
// take.
func TestReplyFileRefuses(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, `, "max_media_mb": 1`)
	outside := filepath.Join(t.TempDir(), "secret.pdf")
	if err := os.WriteFile(outside, []byte("%PDF secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(br.Dir(), "link.pdf")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	big := writeBrainFile(t, br, "big.pdf", bytes.Repeat([]byte("x"), 1<<20+1))
	report := writeBrainFile(t, br, "report.pdf", []byte("%PDF-1.7\n"))
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"a file elsewhere", map[string]any{"path": outside}, "must be a file in your folder"},
		{"a link out of the folder", map[string]any{"path": link}, "escapes"},
		{"a relative path", map[string]any{"path": "report.pdf"}, "must be an absolute path"},
		{"a missing file", map[string]any{"path": filepath.Join(br.Dir(), "gone.pdf")}, "no such file"},
		{"a folder", map[string]any{"path": br.Dir()}, "must be a file in your folder"},
		{"too large", map[string]any{"path": big}, "more than max_media_mb (1 MB)"},
		{"a PDF as an image", map[string]any{"path": report, "as": "image"}, "only a JPEG or PNG shows as an image"},
		{"an unknown kind", map[string]any{"path": report, "as": "sticker"}, "as is image, document or voice"},
		{"a voice note's caption", map[string]any{"path": report, "as": "voice", "caption": "hi"}, "a voice note has no caption"},
		{"a long caption", map[string]any{"path": report, "caption": strings.Repeat("a", maxCaption+1)}, "longer than 1024 characters"},
		{"a secret in the caption", map[string]any{"path": report, "caption": "the key is " + testOpenAIKey}, "the argument caption looks like a secret"},
	} {
		got, isErr := replyFile(t, tb, br, dmRun(), c.args)
		if !isErr || !strings.Contains(got, c.want) {
			t.Errorf("%s: %s, want an error with %q", c.name, got, c.want)
		}
	}
	if rows, _ := tb.store.PendingOutbox(context.Background()); len(rows) != 0 {
		t.Errorf("queued %+v", rows)
	}
	if left := shelved(t, tb); len(left) != 0 {
		t.Errorf("copies left behind: %v", left)
	}
}

// Only a run that answers someone in a WhatsApp chat has reply_file: not
// an observe-only run, a scheduled agent's or a delegated task's, nor a
// call's, whose answer is read aloud. In a lockdown, or with the run's
// risk level read, it is refused.
func TestReplyFileIsForChatsSomeoneReads(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, "")
	report := writeBrainFile(t, br, "report.pdf", []byte("%PDF-1.7\n"))
	has := func(run runInfo) bool {
		run.Brain = br.ID()
		for _, tool := range tb.mcpTools.List(newMCPCaller(run, br.Manifest(), func(string) bool { return true })) {
			if tool.Name == "reply_file" {
				return true
			}
		}
		return false
	}
	group := runInfoFor("", chat.Inbound{JID: testGroup, Sender: "15551234567", Source: chat.SourceWhatsApp})
	handedIn := runInfoFor("", chat.Inbound{JID: testDMChat, Brain: "main", Sender: "plaud", Source: chat.SourceIngest})
	if !has(dmRun()) || !has(group) || !has(handedIn) {
		t.Error("a run that answers a chat has no reply_file")
	}
	for name, run := range map[string]runInfo{
		"observe-only":    runInfoFor("", chat.Inbound{JID: testGroup, Sender: "15551234567", Silent: true}),
		"a schedule":      runInfoFor("", chat.Inbound{JID: chat.ScheduleChatPrefix + "daily", Source: chat.SourceScheduled}),
		"a delegated one": runInfoFor("", chat.Inbound{JID: chat.DelegateChatPrefix + "r1", Source: chat.SourceDelegate}),
		"a call":          runInfoFor("", chat.Inbound{JID: testDMChat, Source: chat.SourceCall, AnswerOnly: true}),
		"a Status":        runInfoFor("", chat.Inbound{JID: "status@broadcast"}),
		"silent policy":   {Chat: testDMChat, Policy: &runPolicy{Silent: true}},
	} {
		if has(run) {
			t.Errorf("%s has reply_file", name)
		}
		raw, _ := json.Marshal(map[string]any{"path": report})
		run.Brain = br.ID()
		if _, err := tb.mcpTools.Call(context.Background(), newMCPCaller(run, br.Manifest(), func(string) bool { return true }), "reply_file", raw); !errors.Is(err, mcp.ErrUnknownTool) {
			t.Errorf("%s called reply_file: %v", name, err)
		}
	}
	readOnly := dmRun()
	readOnly.Policy = &runPolicy{Risk: "read"}
	if got, isErr := replyFile(t, tb, br, readOnly, map[string]any{"path": report}); !isErr || !strings.Contains(got, "does not allow writing") {
		t.Errorf("a read-only run: %s", got)
	}
	if _, err := tb.setLockdown(context.Background(), true, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got, isErr := replyFile(t, tb, br, dmRun(), map[string]any{"path": report}); !isErr || !strings.Contains(got, "lockdown") {
		t.Errorf("in a lockdown: %s", got)
	}
}

// A file waits in the outbox like text while WhatsApp is away, and goes
// once it is back; one whose copy is gone, or that waited past the
// outbox's TTL, is given up on, its copy taken away.
func TestReplyFileWaitsInTheOutbox(t *testing.T) {
	t.Parallel()
	tb, br := newMainBridge(t, "")
	f := newFlakyMessenger(func() int { return 4000 }, false)
	tb.wa = f
	report := writeBrainFile(t, br, "report.pdf", []byte("%PDF-1.7\n"))
	if got, isErr := replyFile(t, tb, br, dmRun(), map[string]any{"path": report, "caption": "Here"}); isErr {
		t.Fatal(got)
	}
	testutil.WaitFor(t, 5*time.Second, "a failed attempt", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.tries > 0
	})
	if len(shelved(t, tb)) != 1 {
		t.Fatalf("shelf = %v, want the copy kept", shelved(t, tb))
	}
	f.setOnline(true)
	testutil.WaitFor(t, 10*time.Second, "the file", func() bool {
		box := f.Outbox()
		return len(box) == 1 && box[0].File == "report.pdf" && box[0].Text == "Here"
	})
	testutil.WaitFor(t, 5*time.Second, "the copy to go", func() bool { return len(shelved(t, tb)) == 0 })

	// A row whose copy is gone, and one too old, are given up on.
	ctx := context.Background()
	blob, name, err := tb.outbox.Shelf().Create(".pdf")
	if err != nil {
		t.Fatal(err)
	}
	blob.Close()
	for _, r := range []store.OutboxRow{
		{Chat: testDMChat, CreatedAt: time.Now(), Parts: []store.OutboxPart{{File: &store.OutboxFile{Blob: "missing.pdf", Name: "a.pdf", Kind: chat.FileDocument}}}},
		{Chat: testGroup, CreatedAt: time.Now().Add(-2 * outboundmsg.OutboxTTL), Parts: []store.OutboxPart{{File: &store.OutboxFile{Blob: name, Name: "b.pdf", Kind: chat.FileDocument}}}},
	} {
		if _, err := tb.store.AddOutbox(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	tb.outbox.Wake()
	testutil.WaitFor(t, 10*time.Second, "both given up on", func() bool {
		sum, err := tb.store.OutboxSummary(ctx, time.Now())
		return err == nil && sum.Failed == 2 && sum.Waiting == 0
	})
	if left := shelved(t, tb); len(left) != 0 {
		t.Errorf("copies left behind: %v", left)
	}
}

// The copies of the files brains send stay off the snapshots, whose
// copies go to the cloud: a brain whose folder never does may send one.
func TestOutboxFilesStayOffTheSnapshots(t *testing.T) {
	t.Parallel()
	data := filepath.Join(t.TempDir(), ".fridolin")
	if ex := dataDirExcludes(data, nil); !slices.Contains(ex, config.Layout{Root: data}.OutboxFiles()) {
		t.Errorf("the snapshots take the outbox's files: %q", ex)
	}
}

// The lease gate refuses a file once the lease is lost, and a build chat
// takes none.
func TestFilesThroughTheMessengersWrappers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dev := devchat.New(nil)
	lost := false
	site := siteChatMessenger{Messenger: leaseGate{Messenger: dev, lost: func() bool { return lost }}}
	f := chat.File{Path: "/nowhere", Name: "a.pdf", Kind: chat.FileDocument}
	if err := site.SendFile(ctx, testDMChat, f); err != nil || len(dev.Outbox()) != 1 {
		t.Errorf("a DM: %v, %+v", err, dev.Outbox())
	}
	if err := site.SendFile(ctx, buildChatJID("main", 1), f); !errors.Is(err, outboundmsg.ErrNotAChat) {
		t.Errorf("a build chat: %v", err)
	}
	lost = true
	if err := site.SendFile(ctx, testDMChat, f); !errors.Is(err, errLeaseLost) {
		t.Errorf("after the lease was lost: %v", err)
	}
	if len(dev.Outbox()) != 1 {
		t.Errorf("outbox = %+v", dev.Outbox())
	}
}
