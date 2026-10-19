package main

import (
	"context"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/pipeline"
	"github.com/Icewater1337/fridolin/internal/whatsapp"

	"testing"

	"time"
)

// The debouncer merges a burst in the order the bridge got it, so a photo
// and the question about it reach the brain as one message, the photo
// first.
func TestPhotoThenQuestionMergePhotoFirst(t *testing.T) {
	dl := &fakeDownloader{body: "JPEG", hold: make(chan struct{})}
	tw := newTestWhatsApp(t, func(opts *whatsapp.Options) { opts.Downloader = dl })
	merged := make(chan brain.Inbound, 1)
	deb := pipeline.NewDebouncer(func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 0 }, func(m brain.Inbound) { merged <- m })
	tw.handler = func(_ context.Context, m brain.Inbound) {
		if m.ID == "3EB0PHOTO" {
			time.Sleep(50 * time.Millisecond)
		}
		deb.Add(m)
	}
	tw.feed(dmEvent("3EB0PHOTO", photoMsg("look")), dmEvent("3EB0TEXT", textMsg("what is this?")))
	time.AfterFunc(100*time.Millisecond, func() { close(dl.hold) })
	select {
	case m := <-merged:
		if m.Text != "look\n\nwhat is this?" || len(m.Media) != 1 || len(m.Save().Parts) != 2 || m.Save().Parts[0].ID != "3EB0PHOTO" {
			t.Errorf("merged %q with %v from %d parts, want the photo and its caption first", m.Text, m.Media, len(m.Save().Parts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the brain")
	}
}
