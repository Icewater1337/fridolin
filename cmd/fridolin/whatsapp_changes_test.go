package main

import (
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"

	"google.golang.org/protobuf/proto"
)

// A view-once photo is not downloaded or kept, and the sender is told.
func TestViewOnceIsNotOpened(t *testing.T) {
	tw := newTestWhatsApp(t)
	dl := &fakeDownloader{body: "jpeg"}
	tw.media = dl
	evt := dmEvent("3EB0ONCE", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("just for you")}})
	evt.IsViewOnce = true
	tw.handle(t, evt)
	select {
	case n := <-tw.notices:
		if !strings.Contains(n, "view-once") {
			t.Errorf("notice %q, want one about view-once", n)
		}
	case <-time.After(2 * time.Second):
		t.Error("the sender was not told")
	}
	if m, ok := tw.next(100 * time.Millisecond); ok {
		t.Errorf("handed to a brain: %+v", m)
	}
	if dl.callCount() != 0 {
		t.Error("the photo was downloaded")
	}
}

// A location shared in a DM reaches the brain as a line it can read.
func TestLocationInADMReachesTheBrain(t *testing.T) {
	tw := newTestWhatsApp(t)
	tw.handle(t, dmEvent("3EB0LOC", &waE2E.Message{LocationMessage: &waE2E.LocationMessage{
		DegreesLatitude: proto.Float64(47.3769), DegreesLongitude: proto.Float64(8.5417),
	}}))
	m, ok := tw.next(2 * time.Second)
	if !ok || m.Text != "[location: 47.376900, 8.541700]" || m.ID != "3EB0LOC" {
		t.Errorf("got %+v (delivered %v), want the location as text", m, ok)
	}
}
