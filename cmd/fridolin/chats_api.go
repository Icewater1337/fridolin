package main

import (
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
	"slices"
	"strings"
	"time"
)

type IgnoredChatResponse struct {
	JID       string    `json:"jid"`
	Kind      string    `json:"kind"` // dm or group
	Name      string    `json:"name"`
	Sender    string    `json:"sender"`
	PushName  string    `json:"push_name"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}
type ChatsAPI struct {
	ListIgnored    func() ([]IgnoredChatResponse, error)
	DismissIgnored func(string, bool) error
	AllowChat      func(bool, string, string) error
	UpdateConfig   func(func(*config.Config) error) error
}

func (a *ChatsAPI) Ignored(w http.ResponseWriter, r *http.Request) {
	chats, err := a.ListIgnored()
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, chats)
}

// ChatRequest is what /api/chats/allow and /api/chats/revoke take.
type ChatRequest struct {
	JID    string `json:"jid"`
	Kind   string `json:"kind"`   // group or dm
	Number string `json:"number"` // for dm; the JID's number when empty
	Brain  string `json:"brain"`  // allow, group: brain to route it to ("" = default brain)
}

// sent is the entry as the request writes it, trimmed: the group JID, or
// for a dm the number (the JID when there is none).
func (req ChatRequest) sent() string {
	if req.Kind == "dm" && strings.TrimSpace(req.Number) != "" {
		return strings.TrimSpace(req.Number)
	}
	return strings.TrimSpace(req.JID)
}

// listEntry checks the request and returns the allow list it is about and
// the entry, in the form the list holds.
func (req ChatRequest) listEntry() (dm bool, entry string, err error) {
	switch req.Kind {
	case "group":
		entry, err = config.AllowListGroup(req.sent())
		return false, entry, err
	case "dm":
		entry, err = chat.PhoneNumber(req.sent())
		return true, entry, err
	default:
		return false, "", errors.New("kind must be group or dm")
	}
}

// apiAllowChat allows a group or number and, for a group, assigns it to a
// brain — the dashboard version of copying a JID out of the log.
func (a *ChatsAPI) Allow(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dm, entry, err := req.listEntry()
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err = a.AllowChat(dm, entry, req.Brain)
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// The chat no longer waits for access, also when it was typed in.
	waiting := entry
	if dm {
		waiting = req.JID
		if waiting == "" {
			waiting = chat.DMJID(entry)
		}
	}
	_ = a.DismissIgnored(waiting, true)
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "allowed", "entry": entry})
}

// errNotListed means the entry to revoke is not on its allow list.
var errNotListed = errors.New("not on the allow list")

// apiRevokeChat takes a number off allowed_numbers or a group off
// allowed_groups. A group keeps its place in a brain's routing, so allowing
// it again sends it to the same brain. Nothing on the list to remove is a
// 404, also for an entry the checks refuse, so the dashboard neither
// reports a removal that did not happen nor an error for one that
// happened elsewhere.
func (a *ChatsAPI) Revoke(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// An entry the bridge would refuse may still be on the list as it is
	// stored; only an unknown kind or no entry at all is refused at once.
	dm, entry, invalid := req.listEntry()
	if (req.Kind != "group" && req.Kind != "dm") || req.sent() == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, invalid.Error())
		return
	}
	canon, name := config.AllowListGroup, "allowed_groups"
	if dm {
		canon, name = chat.PhoneNumber, "allowed_numbers"
	}
	var removed []string
	err := a.UpdateConfig(func(c *config.Config) error {
		list := &c.AllowedGroups
		if dm {
			list = &c.AllowedNumbers
		}
		var kept []string
		kept, removed = revokeEntry(*list, req.sent(), entry, invalid == nil, canon)
		if len(removed) == 0 {
			return errNotListed
		}
		*list = kept
		// A group no longer allowed takes its group_members entry along:
		// one left behind would be refused (validateGroupMembers).
		if !dm {
			for _, g := range removed {
				delete(c.GroupMembers, g)
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errNotListed):
		httpapi.WriteErr(w, http.StatusNotFound, fmt.Sprintf("%s is not in %s", req.sent(), name))
	case err != nil:
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "revoked", "removed": removed})
	}
}

// apiGroupMembers sets who in an allowed group reaches its brain, its
// group_members entry: ["*"] for every member, or the numbers given,
// stored as digits like allowed_numbers. members null, or left out,
// removes the entry, so that allowed_numbers decide again; an empty list
// lets nobody in. The list is checked as the config checks it
// (validateGroupMembers), and a group allowed_groups does not list is
// refused.
func (a *ChatsAPI) Members(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID     string    `json:"jid"`
		Members *[]string `json:"members"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	group, err := config.AllowListGroup(req.JID)
	if err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var members []string
	if req.Members != nil {
		entry := map[string][]string{group: *req.Members}
		if err := config.ValidateGroupMembers(entry, []string{group}); err != nil {
			httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// A number typed twice, in two ways perhaps, is kept once.
		members = []string{}
		for _, n := range config.GroupMembersAsNumbers(entry)[group] {
			if !slices.Contains(members, n) {
				members = append(members, n)
			}
		}
	}
	err = a.UpdateConfig(func(c *config.Config) error {
		if req.Members == nil {
			delete(c.GroupMembers, group)
			return nil
		}
		if c.GroupMembers == nil {
			c.GroupMembers = map[string][]string{}
		}
		c.GroupMembers[group] = members
		return nil
	})
	if err != nil {
		httpapi.WriteErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "saved", "members": members})
}

// revokeEntry splits list into what stays and what revoking sent removes.
// The entry as it is stored goes first: the dashboard sends what it shows,
// which may be written by hand in a form the bridge does not take
// ("+15551234567", a JID with a space). Otherwise every entry that means
// entry (sent in the list's own form, when it is valid) goes.
func revokeEntry(list []string, sent, entry string, valid bool, canon func(string) (string, error)) (kept, removed []string) {
	exact := slices.ContainsFunc(list, func(x string) bool { return strings.TrimSpace(x) == sent })
	kept = list[:0:0]
	for _, x := range list {
		var match bool
		if exact {
			match = strings.TrimSpace(x) == sent
		} else if valid {
			c, err := canon(x)
			match = err == nil && c == entry
		}
		if match {
			removed = append(removed, x)
		} else {
			kept = append(kept, x)
		}
	}
	return kept, removed
}

func (a *ChatsAPI) Dismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JID string `json:"jid"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.JID == "" {
		httpapi.WriteErr(w, http.StatusBadRequest, "jid required")
		return
	}
	if err := a.DismissIgnored(req.JID, false); err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}
