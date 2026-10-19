package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/plaud"
)

func TestRestartRespondsBeforeRequestingStop(t *testing.T) {
	for _, available := range []bool{false, true} {
		w := httptest.NewRecorder()
		called := false
		api := RestartAPI{Available: func() bool { return available }, Restart: func() {
			called = true
			if w.Code != http.StatusAccepted || w.Body.String() != "{\"status\":\"restarting\"}\n" {
				t.Fatalf("restart before response: %d %s", w.Code, w.Body)
			}
		}}
		api.ServeHTTP(w, httptest.NewRequest("POST", "/api/restart", nil))
		if called != available {
			t.Fatalf("available=%v called=%v", available, called)
		}
		if !available && w.Code != http.StatusServiceUnavailable {
			t.Fatal(w.Code)
		}
	}
}

type pairingFake struct {
	pairErr, groupsErr error
	pairs, forgets     int
	groups             []chat.Group
}

func (p *pairingFake) State() chat.LinkState { return chat.LinkState{} }
func (p *pairingFake) StartPairing() error   { p.pairs++; return p.pairErr }
func (p *pairingFake) JoinedGroups(context.Context) ([]chat.Group, error) {
	return p.groups, p.groupsErr
}
func (p *pairingFake) ForgetGroups() { p.forgets++ }

func TestPairingHandlersUseCurrentConnection(t *testing.T) {
	p := &pairingFake{}
	api := ConnectionAPI{Connection: func() PairingConnection { return p }, AllowedGroups: func() []string { return nil }, BrainFor: func(string, bool) *brain.Brain { return nil }}
	w := httptest.NewRecorder()
	api.QR(w, httptest.NewRequest("GET", "/qr", nil))
	if w.Code != http.StatusNotFound {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	api.Pair(w, httptest.NewRequest("POST", "/pair", nil))
	if w.Code != http.StatusOK || p.pairs != 1 {
		t.Fatalf("%d pairs=%d", w.Code, p.pairs)
	}
	p = &pairingFake{pairErr: errors.New("already connected"), groupsErr: errors.New("offline")}
	w = httptest.NewRecorder()
	api.Pair(w, httptest.NewRequest("POST", "/pair", nil))
	if w.Code != http.StatusConflict || p.pairs != 1 {
		t.Fatalf("%d pairs=%d", w.Code, p.pairs)
	}
	w = httptest.NewRecorder()
	api.Groups(w, httptest.NewRequest("GET", "/groups?fresh=1", nil))
	if w.Code != http.StatusServiceUnavailable || p.forgets != 1 {
		t.Fatalf("%d forgets=%d", w.Code, p.forgets)
	}
	p.groupsErr = nil
	w = httptest.NewRecorder()
	api.Groups(w, httptest.NewRequest("GET", "/groups", nil))
	if w.Code != http.StatusOK || w.Body.String() != "[]\n" || p.forgets != 1 {
		t.Fatalf("%d %s forgets=%d", w.Code, w.Body, p.forgets)
	}
}

type plaudAppFake struct {
	err     error
	revoked string
}

func (p *plaudAppFake) SaveDeveloper(plaud.Developer) error { return p.err }
func (p *plaudAppFake) BuildApp() error                     { return p.err }
func (p *plaudAppFake) AppFile() string                     { return "" }
func (p *plaudAppFake) RevokeDevice(id string) error        { p.revoked = id; return p.err }

func TestPlaudAppBuildAndRevokeErrors(t *testing.T) {
	for _, tc := range []struct {
		err           error
		build, revoke int
	}{
		{nil, 202, 200}, {plaud.ErrAppBuilding, 409, 500}, {plaud.ErrUnknownDevice, 412, 404}, {errors.New("storage unavailable"), 412, 500},
	} {
		p := &plaudAppFake{err: tc.err}
		api := PlaudAppAPI{Plaud: func() PlaudAppService { return p }}
		w := httptest.NewRecorder()
		api.Build(w, httptest.NewRequest("POST", "/build", nil))
		if w.Code != tc.build {
			t.Fatalf("build %v: %d", tc.err, w.Code)
		}
		r := httptest.NewRequest("DELETE", "/devices/device-id", nil)
		r.SetPathValue("id", "device-id")
		w = httptest.NewRecorder()
		api.RevokeDevice(w, r)
		if w.Code != tc.revoke || p.revoked != "device-id" {
			t.Fatalf("revoke %v: %d %q", tc.err, w.Code, p.revoked)
		}
	}
}
