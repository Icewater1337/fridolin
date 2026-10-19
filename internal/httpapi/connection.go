package httpapi

import (
	"context"
	"encoding/base64"
	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/chat"
	"net/http"
	"rsc.io/qr"
	"sort"
	"strings"
	"time"
)

type PairingConnection interface {
	State() chat.LinkState
	StartPairing() error
	JoinedGroups(context.Context) ([]chat.Group, error)
}
type ConnectionAPI struct {
	Connection    func() PairingConnection
	AllowedGroups func() []string
	BrainFor      func(string, bool) *brain.Brain
	Brain         func(string) *brain.Brain
}

func (a *ConnectionAPI) QR(w http.ResponseWriter, r *http.Request) {
	st := a.Connection().State()
	if st.QRCode == "" {
		WriteErr(w, http.StatusNotFound, "no pairing code right now")
		return
	}
	// rsc.io/qr is what qrterminal draws the terminal's code with. Its PNG
	// is 8 pixels per module with the quiet zone included; the dashboard
	// scales it.
	code, err := qr.Encode(st.QRCode, qr.M)
	if err != nil {
		WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"data_url":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()),
		"created_at": st.QRAt,
	})
}

func (a *ConnectionAPI) Pair(w http.ResponseWriter, r *http.Request) {
	if err := a.Connection().StartPairing(); err != nil {
		WriteErr(w, http.StatusConflict, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "pairing"})
}

// apiGroups answers GET /api/whatsapp/groups with the groups the account
// is in, from the list the WhatsApp client keeps. With ?fresh=1, which the
// Chats page's Refresh button sends, the client asks WhatsApp again first.
func (a *ConnectionAPI) Groups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.URL.Query().Get("fresh") == "1" {
		if f, ok := a.Connection().(interface{ ForgetGroups() }); ok {
			f.ForgetGroups()
		}
	}
	groups, err := a.Connection().JoinedGroups(ctx)
	if err != nil {
		WriteErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	allowedGroups := a.AllowedGroups()
	type row struct {
		chat.Group
		Allowed bool   `json:"allowed"`
		Brain   string `json:"brain"`
	}
	out := []row{}
	allowed := map[string]bool{}
	for _, g := range allowedGroups {
		allowed[g] = true
	}
	for _, g := range groups {
		rw := row{Group: g, Allowed: allowed[g.JID]}
		if br := a.BrainFor(g.JID, true); br != nil && rw.Allowed {
			rw.Brain = br.ID()
		}
		out = append(out, rw)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	WriteJSON(w, http.StatusOK, out)
}

func (a *ConnectionAPI) StopRun(w http.ResponseWriter, r *http.Request) {
	br := a.Brain(r.PathValue("id"))
	if br == nil {
		WriteErr(w, http.StatusNotFound, "no such brain")
		return
	}
	var req struct {
		ClearQueue bool `json:"clear_queue"`
	}
	if err := DecodeOptionalJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]bool{"stopped": br.StopCurrent(req.ClearQueue)})
}
