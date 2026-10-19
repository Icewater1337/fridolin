package main

import (
	"context"

	"testing"

	"time"
)

// The debouncer merges a burst in the order the bridge got it, so a photo
// and the question about it reach the brain as one message, the photo
// first.
func TestPhotoThenQuestionMergePhotoFirst(t *testing.T) {
	dl := &fakeDownloader{body: "JPEG", hold: make(chan struct{})}
	tw := newTestWhatsApp(t, func(opts *waOptions) { opts.Downloader = dl })
	merged := make(chan Inbound, 1)
	deb := NewDebouncer(func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 0 }, func(m Inbound) { merged <- m })
	tw.handler = func(_ context.Context, m Inbound) {
		if m.ID == "3EB0PHOTO" {
			time.Sleep(50 * time.Millisecond)
		}
		deb.Add(m)
	}
	tw.feed(dmEvent("3EB0PHOTO", photoMsg("look")), dmEvent("3EB0TEXT", textMsg("what is this?")))
	time.AfterFunc(100*time.Millisecond, func() { close(dl.hold) })
	select {
	case m := <-merged:
		if m.Text != "look\n\nwhat is this?" || len(m.Media) != 1 || len(m.parts) != 2 || m.parts[0].ID != "3EB0PHOTO" {
			t.Errorf("merged %q with %v from %d parts, want the photo and its caption first", m.Text, m.Media, len(m.parts))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("nothing reached the brain")
	}
}
