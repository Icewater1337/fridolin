package brain

import (
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/history"
)

// storedInbound is an Inbound as the inbound queue keeps it: all of it
// but Done, which a message from WhatsApp does not have.
type SavedInbound struct {
	JID     string         `json:"jid"`
	ChatAlt string         `json:"chat_alt,omitempty"`
	Sender  string         `json:"sender"`
	Text    string         `json:"text,omitempty"`
	Media   []string       `json:"media,omitempty"`
	Brain   string         `json:"brain,omitempty"`
	Quoted  *chat.Quote    `json:"quoted,omitempty"`
	Silent  bool           `json:"silent,omitempty"`
	Source  string         `json:"source"`
	At      time.Time      `json:"at"`
	ID      string         `json:"id,omitempty"`
	Logged  *history.Entry `json:"logged,omitempty"`
	Parts   []SavedInbound `json:"parts,omitempty"`
}

func (m Inbound) Save() SavedInbound {
	s := SavedInbound{
		JID: m.JID, ChatAlt: m.ChatAlt, Sender: m.Sender, Text: m.Text, Media: m.Media, Brain: m.Brain,
		Quoted: m.Quoted, Silent: m.Silent, Source: m.Source, At: m.At, ID: m.ID,
	}
	if m.logged != (history.Entry{}) {
		logged := m.logged
		s.Logged = &logged
	}
	for _, p := range m.parts {
		s.Parts = append(s.Parts, p.Save())
	}
	return s
}

func (s SavedInbound) Restore() Inbound {
	m := Inbound{Inbound: chat.Inbound{JID: s.JID, ChatAlt: s.ChatAlt, Sender: s.Sender, Text: s.Text, Media: s.Media, Brain: s.Brain,
		Quoted: s.Quoted, Silent: s.Silent, Source: s.Source, At: s.At, ID: s.ID},
	}
	if s.Logged != nil {
		m.logged = *s.Logged
	}
	for _, p := range s.Parts {
		m.parts = append(m.parts, p.Restore())
	}
	return m
}
