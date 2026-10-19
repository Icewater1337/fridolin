package media

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// Resolve validates the canonical spelling before inspecting the file type.
// Replace an entry in that interval without a timing-dependent goroutine.
func TestResolveRegularFileCheckStaysRooted(t *testing.T) {
	t.Parallel()
	for _, replacement := range []string{"file", "parent"} {
		t.Run(replacement, func(t *testing.T) {
			t.Parallel()
			base := fsx.Canonical(t.TempDir())
			media := filepath.Join(base, "media")
			p := mediaTestFile(t, filepath.Join(media, "a", "nested", "note.txt"), "inside")
			outside := mediaTestFile(t, filepath.Join(base, "outside", "note.txt"), "outside")
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil || !fsx.Within(resolved, media) {
				t.Fatalf("initial resolution = %q, %v", resolved, err)
			}
			entry, target := p, outside
			if replacement == "parent" {
				entry, target = filepath.Dir(p), filepath.Dir(outside)
			}
			if err := os.Rename(entry, entry+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, entry); err != nil {
				t.Fatal(err)
			}
			if info, err := statResolved(media, resolved); err == nil {
				t.Fatalf("metadata lookup followed replacement outside media: %v", info)
			}
		})
	}
}

func TestResolveScriptPaths(t *testing.T) {
	t.Parallel()
	base := fsx.Canonical(t.TempDir())
	media := filepath.Join(base, "media")
	regular := mediaTestFile(t, filepath.Join(media, "note.txt"), "inside")
	outside := mediaTestFile(t, filepath.Join(base, "outside.txt"), "outside")
	insideLink := filepath.Join(media, "absolute-link")
	relativeLink := filepath.Join(media, "relative-link")
	outsideLink := filepath.Join(media, "outside-link")
	for link, target := range map[string]string{insideLink: regular, relativeLink: "note.txt", outsideLink: outside} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	in := Inbox{Dir: media}
	for _, p := range []string{regular, insideLink, relativeLink} {
		got, err := in.Resolve("a", []string{p}, false)
		if err != nil || !slices.Equal(got, []string{regular}) {
			t.Errorf("Resolve(%q) = %q, %v", p, got, err)
		}
	}
	for _, tc := range []struct{ path, want string }{
		{outsideLink, "every path must be an existing file inside the media directory"},
		{filepath.Join(media, "missing"), "every path must be an existing file inside the media directory"},
		{"note.txt", "every path must be an existing file inside the media directory"},
		{media, "not a regular file: media"},
	} {
		got, err := in.Resolve("a", []string{tc.path}, false)
		if got != nil || err == nil || err.Error() != tc.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
}
