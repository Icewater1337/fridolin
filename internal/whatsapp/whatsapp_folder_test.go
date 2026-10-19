package whatsapp

import (
	"context"

	"path/filepath"

	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// A download makes the media folder when it is not there, as after a
// media_dir saved in Settings and before the restart.
func TestDownloadMakesTheMediaFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "media")
	doc := attachment{kind: "document", media: &waE2E.DocumentMessage{}, name: "scan.pdf"}
	path, err := saveAttachment(context.Background(), &fakeDownloader{body: "%PDF-1.7"}, dir, doc, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "scan.pdf") {
		t.Errorf("saved as %q", path)
	}
}
