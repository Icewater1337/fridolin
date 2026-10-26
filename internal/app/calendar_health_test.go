package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// useHealthGoogle is useGoogleServer with the owner told of broken grants,
// as the running bridge has it.
func useHealthGoogle(t *testing.T, tb *testBridge) *map[string]string {
	t.Helper()
	srv, got := fakeGoogle(t)
	o := googleTestOptions(googleOptionsFor(tb.cfgs), srv)
	o.OnBroken = tb.calendarGrantBroken
	tb.google = google.New(o)
	if w := tb.do("PUT", "/api/google/client", map[string]string{"json": fmt.Sprintf(testClientJSON, srv.URL)}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return got
}

// calendarHealthOf is the health the brain's card shows.
func calendarHealthOf(t *testing.T, tb *testBridge, id string, list bool) google.GrantHealth {
	t.Helper()
	path := "/api/brains/" + id + "/calendar"
	if !list {
		path += "?list=0"
	}
	var st struct {
		Health *google.GrantHealth `json:"health"`
	}
	_ = json.Unmarshal(tb.do("GET", path, nil).Body.Bytes(), &st)
	if st.Health == nil {
		t.Fatalf("%s: no health", path)
	}
	return *st.Health
}

// ownerNotices are the calendar notices the owner got.
func ownerNotices(tb *testBridge) []string {
	var out []string
	for _, s := range tb.sentTo("15551234567@s.whatsapp.net") {
		if strings.Contains(s, "Google Calendar stopped working") {
			out = append(out, s)
		}
	}
	return out
}

func waitNotices(t *testing.T, tb *testBridge, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := ownerNotices(tb)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestCalendarGrantHealth: the brain's card says when its calendar last
// worked and that Google revoked the grant; the Overview's list and
// doctor say so too, and the owner hears it once, by the brain's name,
// until a new sign-in mends it.
func TestCalendarGrantHealth(t *testing.T) {
	t.Parallel()
	tb := newTestBridge(t, "")
	useHealthGoogle(t, tb)
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "Family"})

	connectBrain(t, tb, "a")
	h := calendarHealthOf(t, tb, "a", false)
	if h.State != google.GrantValid || h.LastOK == nil || time.Since(*h.LastOK) > time.Minute {
		t.Fatalf("after the sign-in: %+v", h)
	}

	// Google revokes the grant; the next look finds it.
	expiredCalendarToken(t, tb, "REVOKED")
	if h := calendarHealthOf(t, tb, "a", true); h.State != google.GrantRevoked || h.Since == nil || !strings.Contains(h.Detail, "no longer accepts") {
		t.Fatalf("revoked: %+v", h)
	}
	_ = calendarHealthOf(t, tb, "a", true) // a second look tells nobody again
	waitNotices(t, tb, 1)
	time.Sleep(100 * time.Millisecond) // a second notice would be out by now
	if notices := ownerNotices(tb); len(notices) != 1 || !strings.Contains(notices[0], "Family") || strings.Contains(notices[0], " a ") {
		t.Fatalf("owner notices = %q", notices)
	}

	var list struct {
		Brains []struct {
			ID, Name, State string
		} `json:"brains"`
	}
	_ = json.Unmarshal(tb.do("GET", "/api/calendar/health", nil).Body.Bytes(), &list)
	if len(list.Brains) != 1 || list.Brains[0].Name != "Family" || list.Brains[0].State != google.GrantRevoked {
		t.Errorf("health list = %+v", list)
	}

	// doctor reads the same record.
	env := &doctor.Env{Config: tb.cfgs.Get(), ReadFile: os.ReadFile}
	if r := checkCalendarGrants(context.Background(), env); r.Status != doctor.Warn || !strings.Contains(r.Detail, "a (") || !strings.Contains(r.Fix, "Reconnect") {
		t.Errorf("doctor = %+v", r)
	}

	// Reconnect mends it, and a later break is told again.
	connectBrain(t, tb, "a")
	if h := calendarHealthOf(t, tb, "a", false); h.State != google.GrantValid || h.Since != nil {
		t.Errorf("after reconnecting: %+v", h)
	}
	if r := checkCalendarGrants(context.Background(), env); r.Status != doctor.OK {
		t.Errorf("doctor after reconnecting = %+v", r)
	}
	expiredCalendarToken(t, tb, "REVOKED")
	tb.google.CheckGrants(context.Background(), []string{"a"})
	if got := waitNotices(t, tb, 2); len(got) != 2 {
		t.Errorf("a second break: notices %q", got)
	}

	// Disconnect forgets it.
	if w := tb.do("DELETE", "/api/brains/a/calendar?local_only=1", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = json.Unmarshal(tb.do("GET", "/api/calendar/health", nil).Body.Bytes(), &list)
	if len(list.Brains) != 0 {
		t.Errorf("after disconnecting: %+v", list)
	}
}

// TestCalendarGrantHealthIgnoresOutages: Google being down says nothing
// about the grant, and a client Google refuses needs a new consent.
func TestCalendarGrantHealthIgnoresOutages(t *testing.T) {
	t.Parallel()
	tb := newTestBridge(t, "")
	useHealthGoogle(t, tb)
	tb.addFolderBrain(t, manifest.Manifest{ID: "a", Name: "A"})
	connectBrain(t, tb, "a")

	expiredCalendarToken(t, tb, "GOOGLE-DOWN")
	tb.google.CheckGrants(context.Background(), []string{"a", "not-connected"})
	if h := calendarHealthOf(t, tb, "a", false); h.State != google.GrantValid {
		t.Errorf("an outage changed the grant: %+v", h)
	}
	expiredCalendarToken(t, tb, "CLIENT-GONE")
	tb.google.CheckGrants(context.Background(), []string{"a"})
	if h := calendarHealthOf(t, tb, "a", false); h.State != google.GrantReconsent || !strings.Contains(h.Detail, "Settings → Google sign-in") {
		t.Errorf("a refused client: %+v", h)
	}
	// The record lasts a restart of the bridge.
	tb.google = google.New(googleOptionsFor(tb.cfgs))
	if h := calendarHealthOf(t, tb, "a", false); h.State != google.GrantReconsent {
		t.Errorf("after a restart: %+v", h)
	}
}
