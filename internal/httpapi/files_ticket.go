package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Tickets let the dashboard load a root's files with plain requests: an
// <img>, <audio> or <video> (which streams with Range requests) and a
// download link send the session's cookie but not its binding header (see
// web/assets/app/api.js), so the API's guard turns them away. A ticket is
// signed by the bridge, names one root and lasts TicketLife; the page asks
// for it through the guarded API (GET /api/files/ticket) and puts it into
// the address of /files/raw. The key is made when the bridge starts, so a
// restart ends every ticket.
type Tickets struct {
	key []byte
	now func() time.Time
}

// TicketLife is how long a ticket opens its root's files.
const TicketLife = 30 * time.Minute

// NewTickets returns tickets signed with a new random key.
func NewTickets() *Tickets {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Tickets{key: key, now: time.Now}
}

func (t *Tickets) sign(root string, exp int64) string {
	m := hmac.New(sha256.New, t.key)
	m.Write([]byte(root + "\x00" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

// Issue returns a ticket for root and when it expires.
func (t *Tickets) Issue(root string) (string, time.Time) {
	exp := t.now().Add(TicketLife).Truncate(time.Second)
	return strconv.FormatInt(exp.Unix(), 10) + "." + t.sign(root, exp.Unix()), exp
}

// Valid reports whether ticket opens root now.
func (t *Tickets) Valid(root, ticket string) bool {
	s, sig, ok := strings.Cut(ticket, ".")
	if !ok {
		return false
	}
	exp, err := strconv.ParseInt(s, 10, 64)
	if err != nil || t.now().Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(t.sign(root, exp)))
}

// FilesTicketResponse is GET /api/files/ticket.
type FilesTicketResponse struct {
	Expires time.Time `json:"expires"`
	Root    string    `json:"root"`
	Ticket  string    `json:"ticket"`
}

func (a *FilesAPI) ticket(w http.ResponseWriter, r *http.Request, s Scope) {
	root, ok := a.root(r.URL.Query().Get("root"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	tok, exp := a.Tickets.Issue(root.ID)
	WriteJSON(w, http.StatusOK, FilesTicketResponse{Root: root.ID, Ticket: tok, Expires: exp.UTC()})
}

// TicketRaw serves GET /files/raw, outside the API's guard: a file of the
// root the request's ticket opens, as /api/files/raw does.
func (a *FilesAPI) TicketRaw(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if a.Tickets == nil || !a.Tickets.Valid(q.Get("root"), q.Get("ticket")) {
		WriteErr(w, http.StatusUnauthorized, "this link to a file has expired; open the file again from the dashboard")
		return
	}
	a.raw(w, r, Owner)
}
