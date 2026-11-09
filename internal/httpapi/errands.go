package httpapi

import (
	"context"
	"net/http"
	"time"
)

// ErrandsAPI serves the Errands page (docs/BRAINS.md, "Errands"): every
// brain's errands with their conversations, and the people Fridolin
// knows. Stop, Unblock and SetPerson do what the brain's errand_stop, the
// owner's !unblock and errand_contact do.
type ErrandsAPI struct {
	// List is GET /api/errands.
	List func(context.Context) (ErrandsResponse, error)
	// Get is GET /api/errands/{id}; an apperr.NotFound for none.
	Get func(ctx context.Context, id string) (ErrandDetail, error)
	// Stop ends an errand that has not ended, without a word to the
	// person; an apperr.Conflict for one that has.
	Stop func(ctx context.Context, id string) error
	// Unblock lets Fridolin hear a person again; an apperr.NotFound for
	// a number nobody blocked.
	Unblock func(ctx context.Context, number string) error
	// SetPerson sets how Fridolin deals with someone it knows; an
	// apperr.NotFound for someone it does not, an apperr.Conflict for
	// someone blocked, an apperr.Invalid for a mode or brief it does not
	// take.
	SetPerson func(ctx context.Context, number string, ways PersonWays) (ErrandPerson, error)
}

// ErrandView is one errand of GET /api/errands.
type ErrandView struct {
	ID    string `json:"id"`
	Brain string `json:"brain"`
	// Name is what the brain called the person.
	Name string `json:"name"`
	// Number is theirs, digits with the country code, as the Chats page
	// shows the numbers in allowed_numbers; Masked is it as the audit log
	// writes it.
	Number string `json:"number"`
	Masked string `json:"masked"`
	// State is asking, open, done, aborted, declined, expired or stopped;
	// Live is true for the first two.
	State string `json:"state"`
	Live  bool   `json:"live"`
	// Sent counts Fridolin's messages.
	Sent     int        `json:"sent"`
	Created  time.Time  `json:"created"`
	Deadline time.Time  `json:"deadline"`
	Closed   *time.Time `json:"closed,omitempty"`
	// Outcome is the summary, or why it ended.
	Outcome string `json:"outcome"`
}

// ErrandPerson is someone Fridolin wrote to in an errand.
type ErrandPerson struct {
	Number string `json:"number"`
	Masked string `json:"masked"`
	Name   string `json:"name"`
	// Brain is the brain of the last errand with them.
	Brain string `json:"brain"`
	// State is known, or blocked until the owner unblocks them, for
	// Reason.
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Mode is how Fridolin takes what they write while no errand is open:
	// converse (it talks with them) or forward (to the owner).
	Mode string `json:"mode"`
	// Brief is what Fridolin may always tell them about the owner.
	Brief string `json:"brief"`
	// Calendar is the most errands with them may do with the owner's
	// calendar, "" for what brain.json allows.
	Calendar string `json:"calendar"`
	// Warned is when Fridolin warned them, within the month a second try
	// blocks them.
	Warned *time.Time `json:"warned,omitempty"`
	// Since is when they last changed.
	Since time.Time `json:"since"`
}

// PersonWays is PUT /api/errand-contacts/{number}: how Fridolin deals
// with someone it knows.
type PersonWays struct {
	Mode     string `json:"mode"`
	Brief    string `json:"brief"`
	Calendar string `json:"calendar"`
}

// ErrandsResponse is GET /api/errands: the errands that have not ended,
// then the last ended ones, newest first; and the people Fridolin knows,
// the latest changed first.
type ErrandsResponse struct {
	Errands []ErrandView   `json:"errands"`
	People  []ErrandPerson `json:"people"`
}

// ErrandLine is one message of an errand's conversation: from you
// (Fridolin), them (the person) or owner (the owner's word, through
// errand_answer). What they wrote is theirs, to show as text only.
type ErrandLine struct {
	From string    `json:"from"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// ErrandDetail is GET /api/errands/{id}: the errand with what the brain
// gave it and its conversation.
type ErrandDetail struct {
	ErrandView
	Goal  string `json:"goal"`
	Brief string `json:"brief"`
	// ByOwner is an errand the owner asked for in their own chat.
	ByOwner bool `json:"by_owner"`
	Days    int  `json:"days"`
	// MaxMessages is how many messages Fridolin writes at most.
	MaxMessages int `json:"max_messages"`
	// Calendar is the most it may do with the owner's calendar, "" for
	// what brain.json and the person allow.
	Calendar string `json:"calendar"`
	// Booking is its booking: asking (the owner is asked to confirm),
	// booking or booked; From and To are its time.
	Booking     string       `json:"booking,omitempty"`
	BookingFrom *time.Time   `json:"booking_from,omitempty"`
	BookingTo   *time.Time   `json:"booking_to,omitempty"`
	Lines       []ErrandLine `json:"lines"`
}

// Register adds the routes.
func (a *ErrandsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/errands", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.List(r.Context())
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("GET /api/errands/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSONIfChanged(w, r, out)
	})
	mux.HandleFunc("POST /api/errands/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Stop(r.Context(), r.PathValue("id")); err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, OperationResponse{Status: "stopped"})
	})
	mux.HandleFunc("POST /api/errand-contacts/{number}/unblock", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Unblock(r.Context(), r.PathValue("number")); err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, OperationResponse{Status: "unblocked"})
	})
	mux.HandleFunc("PUT /api/errand-contacts/{number}", func(w http.ResponseWriter, r *http.Request) {
		var ways PersonWays
		if err := DecodeJSON(r, &ways); err != nil {
			WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := a.SetPerson(r.Context(), r.PathValue("number"), ways)
		if err != nil {
			writeStatusErr(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, out)
	})
}
