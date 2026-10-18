package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

var mediaTestStarted = time.Now()
var mediaTestMigration = MediaMigration{Started: mediaTestStarted}

func testMediaInboxAt(dir string) MediaInbox {
	workingDir, _ := os.Getwd()
	return MediaInbox{Dir: dir, WorkingDir: workingDir, Migration: &mediaTestMigration}
}

func mediaTestFile(t *testing.T, p, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mediaDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

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
			return mediaTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "%PDF")
		}, "a/scan.pdf", nil},
		{"dropped by a script into a folder of its own", func(t *testing.T, media string) string {
			return mediaTestFile(t, filepath.Join(media, "upload", "scan.pdf"), "%PDF")
		}, "a/scan.pdf", nil},
		{"at the top of the media folder", func(t *testing.T, media string) string {
			return mediaTestFile(t, filepath.Join(media, "note.txt"), "x")
		}, "a/note.txt", nil},
		{"already in its inbox", func(t *testing.T, media string) string {
			return mediaTestFile(t, filepath.Join(media, "a", "plaud", "t.txt"), "x")
		}, "", nil},
		{"outside the media folder", func(t *testing.T, media string) string {
			return mediaTestFile(t, filepath.Join(filepath.Dir(media), "elsewhere.txt"), "x")
		}, "", nil},
		{"a name already taken in the inbox", func(t *testing.T, media string) string {
			mediaTestFile(t, filepath.Join(media, "a", "scan.pdf"), "older")
			return mediaTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "newer")
		}, "a/scan-2.pdf", func(t *testing.T, media string) {
			if data, _ := os.ReadFile(filepath.Join(media, "a", "scan.pdf")); string(data) != "older" {
				t.Errorf("the file already in the inbox was replaced: %q", data)
			}
		}},
		// A brain may plant anything in its own inbox, a symlink where its
		// next file will go among them; the file lands beside it.
		{"a symlink planted in the inbox", func(t *testing.T, media string) string {
			target := mediaTestFile(t, filepath.Join(filepath.Dir(media), "outside.txt"), "keep")
			if err := os.MkdirAll(filepath.Join(media, "a"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(media, "a", "scan.pdf")); err != nil {
				t.Fatal(err)
			}
			return mediaTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "%PDF")
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
			return mediaTestFile(t, filepath.Join(media, incomingMediaDir, "scan.pdf"), "%PDF")
		}, "", func(t *testing.T, media string) {
			if names := mediaDirNames(t, filepath.Join(media, "b")); len(names) != 0 {
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
			media := filepath.Join(fsx.Canonical(t.TempDir()), "media")
			// Ready for inboxes, as the bridge has it before any message,
			// so what the case puts there stays.
			if err := testMediaInboxAt(media).RetireLegacy(); err != nil {
				t.Fatal(err)
			}
			p := tc.setup(t, media)
			got := testMediaInboxAt(media).Claim("a", []string{p})
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
		root := fsx.Canonical(t.TempDir())
		real := filepath.Join(root, "real-media")
		if err := os.MkdirAll(filepath.Join(real, incomingMediaDir), 0o700); err != nil {
			t.Fatal(err)
		}
		media := filepath.Join(root, "media")
		if err := os.Symlink(real, media); err != nil {
			t.Fatal(err)
		}
		p := mediaTestFile(t, filepath.Join(real, incomingMediaDir, "x.jpg"), "x")
		if got := testMediaInboxAt(media).Claim("a", []string{p}); !maps.Equal(got, map[string]string{p: filepath.Join(media, "a", "x.jpg")}) {
			t.Errorf("claimMedia = %q", got)
		}
	})
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
	media := filepath.Join(fsx.Canonical(t.TempDir()), "media")
	for _, f := range []string{"plaud/2026-01-15 standup.txt", "upload/scan.pdf", "top.pdf", incomingMediaDir + "/voice.ogg", legacyMediaDir + "/upload/older.pdf"} {
		mediaTestFile(t, filepath.Join(media, f), "content of "+f)
	}
	// Downloaded by an older version, before this bridge started.
	before := mediaTestStarted.Add(-time.Hour)
	for _, f := range []string{"invoice.pdf", "3EB0A1B2.jpg", "older.pdf"} {
		p := mediaTestFile(t, filepath.Join(media, f), "content of "+f)
		if err := os.Chtimes(p, before, before); err != nil {
			t.Fatal(err)
		}
	}
	mediaTestFile(t, filepath.Join(media, legacyMediaDir, "older.pdf"), "an older one")
	inbox, err := testMediaInboxAt(media).Open("plaud")
	if err != nil {
		t.Fatal(err)
	}
	inbox.Close()
	if names := mediaDirNames(t, filepath.Join(media, "plaud")); len(names) != 0 {
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
	if names := mediaDirNames(t, media); !slices.Equal(names, []string{mediaInboxesMarker, incomingMediaDir, legacyMediaDir, "plaud", "top.pdf"}) {
		t.Errorf("the media folder holds %q, want only the new file at its top", names)
	}
	// The marker is a folder, which /ingest and claimMedia, taking
	// regular files only, cannot move into a brain's inbox: the next
	// inbox would then have moved every inbox aside.
	marker := filepath.Join(media, mediaInboxesMarker)
	if fi, err := os.Lstat(marker); err != nil || !fi.IsDir() {
		t.Errorf("%s is not a folder: %v", marker, err)
	}
	if got := testMediaInboxAt(media).Claim("plaud", []string{marker}); len(got) != 0 {
		t.Errorf("claimMedia moved the marker: %q", got)
	}
	// Once the folder is ready, every folder in it is an inbox and stays.
	mediaTestFile(t, filepath.Join(media, "notes", "photo.jpg"), "x")
	inbox, err = testMediaInboxAt(media).Open("notes")
	if err != nil {
		t.Fatal(err)
	}
	inbox.Close()
	if names := mediaDirNames(t, filepath.Join(media, "notes")); !slices.Equal(names, []string{"photo.jpg"}) {
		t.Errorf("brain notes' inbox holds %q, want its photo", names)
	}
}

// Attachment names come from the sender. Whatever they send, the saved name
// stays in the media directory, is not hidden, keeps to one line in the
// prompt and the history, and is short enough to be saved at all.
func TestSanitizeMediaName(t *testing.T) {
	long := strings.Repeat("ä", 150) + ".pdf" // 304 bytes
	cases := []struct {
		name, in, want string
	}{
		{"plain", "scan.pdf", "scan.pdf"},
		{"directories", "../../etc/passwd", "passwd"},
		{"absolute", "/etc/passwd", "passwd"},
		{"hidden", ".bashrc", "bashrc"},
		{"dot dot", "..", "file"},
		{"empty", "", "file"},
		{"root", "/", "file"},
		{"newline", "a\nb.pdf", "ab.pdf"},
		{"carriage return and tab", "a\r\tb.pdf", "ab.pdf"},
		{"NUL", "a\x00b.pdf", "ab.pdf"},
		{"line separator", "a\u2028b\u2029c.pdf", "abc.pdf"},
		{"only control characters", "\n\n", "file"},
		{"unicode kept", "Rechnung März 🧾.pdf", "Rechnung März 🧾.pdf"},
		{"too long", long, strings.Repeat("ä", 56) + "….pdf"},
		{"too long, no real extension", strings.Repeat("x", 100) + "." + strings.Repeat("y", 100), strings.Repeat("x", 100) + "." + strings.Repeat("y", 16) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeMediaName(c.in)
			if got != c.want {
				t.Errorf("sanitizeMediaName(%q) = %q, want %q", c.in, got, c.want)
			}
			if len(got) > maxMediaNameBytes {
				t.Errorf("%d bytes, more than %d", len(got), maxMediaNameBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("%q is not valid UTF-8", got)
			}
		})
	}
}

// FuzzSanitizeMediaName: whatever name the sender gives an attachment, the
// file is saved in the media directory, under a name that is not hidden,
// keeps to one line and is short enough to save; and so is every name
// placeMedia falls back to when that one is taken.
func FuzzSanitizeMediaName(f *testing.F) {
	for _, s := range []string{
		"scan.pdf", "../../etc/passwd", "/etc/passwd", ".bashrc", "..", "", "/", "a\nb.pdf", "a\r\tb.pdf",
		"a\x00b.pdf", "a\u2028b\u2029c.pdf", "\n\n", "Rechnung März 🧾.pdf", "x/.", "./..", ".../...",
		strings.Repeat("ä", 150) + ".pdf", strings.Repeat("x", 100) + "." + strings.Repeat("y", 100), "\xff\xfe.\xc3",
	} {
		f.Add(s)
	}
	const dir = "/media"
	f.Fuzz(func(t *testing.T, name string) {
		got := sanitizeMediaName(name)
		if len(got) > maxMediaNameBytes {
			t.Errorf("sanitizeMediaName(%q) = %q, %d bytes, more than %d", name, got, len(got), maxMediaNameBytes)
		}
		for _, i := range []int{1, 2, lastMediaCandidate - 1, lastMediaCandidate} {
			c := mediaNameCandidate(got, i)
			if filepath.Dir(filepath.Join(dir, c)) != dir || strings.HasPrefix(c, ".") {
				t.Errorf("%q, from %q, is not a visible file in the media directory", c, name)
			}
			if strings.IndexFunc(c, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' }) >= 0 {
				t.Errorf("%q, from %q, has a control character or a line break", c, name)
			}
			if !utf8.ValidString(c) {
				t.Errorf("%q, from %q, is not valid UTF-8", c, name)
			}
			if len(c) > 255 {
				t.Errorf("%q, from %q, is longer than a file name may be", c, name)
			}
		}
	})
}

// cappedFile refuses every way whatsmeow could grow the file past the cap,
// and allows everything up to it. A refusal counts as a cancellation, which
// whatsmeow does not retry on its next server.
func TestCappedFile(t *testing.T) {
	ops := []struct {
		name string
		op   func(f mediaFile) error
		over bool
	}{
		{"write up to the cap", func(f mediaFile) error { _, err := f.Write(make([]byte, 10)); return err }, false},
		{"write past the cap", func(f mediaFile) error { _, err := f.Write(make([]byte, 11)); return err }, true},
		{"write after a seek past the cap", func(f mediaFile) error {
			if _, err := f.Seek(8, io.SeekStart); err != nil {
				return err
			}
			_, err := f.Write(make([]byte, 3))
			return err
		}, true},
		{"write at the end of the cap", func(f mediaFile) error { _, err := f.WriteAt(make([]byte, 2), 8); return err }, false},
		{"write at past the cap", func(f mediaFile) error { _, err := f.WriteAt(make([]byte, 3), 8); return err }, true},
		{"truncate to the cap", func(f mediaFile) error { return f.Truncate(10) }, false},
		{"truncate past the cap", func(f mediaFile) error { return f.Truncate(11) }, true},
		{"copy past the cap", func(f mediaFile) error {
			_, err := io.Copy(f, io.TeeReader(strings.NewReader(strings.Repeat("x", 11)), io.Discard))
			return err
		}, true},
	}
	for _, c := range ops {
		t.Run(c.name, func(t *testing.T) {
			tmp, err := os.CreateTemp(t.TempDir(), "capped")
			if err != nil {
				t.Fatal(err)
			}
			defer tmp.Close()
			stopped := false
			f := &cappedFile{f: tmp, limit: 10, stop: func() { stopped = true }}
			err = c.op(f)
			if c.over != errors.Is(err, errMediaTooLarge) || c.over != f.over || c.over != stopped {
				t.Errorf("err %v, over %v, stopped %v; want refused = %v", err, f.over, stopped, c.over)
			}
			if c.over && !errors.Is(err, context.Canceled) {
				t.Errorf("err %v, want it to count as context.Canceled", err)
			}
			if !c.over && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if st, _ := tmp.Stat(); st.Size() > 10 {
				t.Errorf("the file grew to %d bytes", st.Size())
			}
		})
	}
}
