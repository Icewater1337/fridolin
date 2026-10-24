package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Icewater1337/fridolin/internal/config"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"slices"
	"strings"

	"testing"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
)

// Every way the messenger sends refuses Status, broadcast lists and
// channels before it reaches WhatsApp, whatever chat a brain or script
// names.
func TestSendRefusesStatusBroadcastAndChannels(t *testing.T) {
	tw := newTestWhatsApp(t)
	ctx := context.Background()
	sends := map[string]func(jid string) error{
		"SendMessage": func(jid string) error { return tw.SendMessage(ctx, jid, "hello") },
		"SendChunked": func(jid string) error { return tw.SendChunked(ctx, jid, strings.Repeat("hello ", 2000)) },
		"SendPoll": func(jid string) error {
			_, err := tw.SendPoll(ctx, jid, "Lunch?", []string{"Yes", "No"}, 1, func(string) { t.Error("kept a poll that does not go out") })
			return err
		},
	}
	for _, jid := range []string{"status@broadcast", "status:3@broadcast", "1700000000@broadcast", "broadcast", "120363000000000000@newsletter"} {
		for name, send := range sends {
			t.Run(name+" to "+jid, func(t *testing.T) {
				err := func() (err error) {
					defer func() {
						if r := recover(); r != nil {
							err = fmt.Errorf("reached for WhatsApp: %v", r)
						}
					}()
					return send(jid)
				}()
				if !errors.Is(err, outboundmsg.ErrRefusedRecipient) {
					t.Errorf("err = %v, want a refusal", err)
				}
			})
		}
	}
}

func TestCheckRecipient(t *testing.T) {
	cases := []struct {
		jid    string
		refuse bool
	}{
		{"status@broadcast", true},
		{"1700000000@broadcast", true},
		{"120363000000000000@newsletter", true},
		{"15551234567@s.whatsapp.net", false},
		{"15559876543@lid", false},
		{"120363000000000000@g.us", false},
	}
	for _, c := range cases {
		t.Run(c.jid, func(t *testing.T) {
			err := checkRecipient(mustJID(t, c.jid))
			if got := errors.Is(err, outboundmsg.ErrRefusedRecipient); got != c.refuse {
				t.Errorf("refused = %v (%v), want %v", got, err, c.refuse)
			}
		})
	}
}

// FuzzNumberAllowed: allowed_numbers lets a sender DM the bridge only when
// it lists the number: no prefix, no suffix, no other number, however the
// number was written on either side ("+1 555 123 4567" is 15551234567).
// allow_any_dm decides what an empty list means and never widens a list.
func FuzzNumberAllowed(f *testing.F) {
	const n = "15551234567"
	f.Add(n, n, false)
	f.Add(n, "", true)
	f.Add(n, "", false)
	f.Add(n[:len(n)-1], n+",15559876543", true) // a listed number cut short
	f.Add(n+"8", n, true)                       // and one made longer
	f.Add("+"+n, n, false)
	f.Add(n, "+1 555 123 4567", false) // a hand-written entry
	f.Add(n, "1 555 123 456", false)   // that is another number
	f.Add("", ",", false)
	f.Fuzz(func(t *testing.T, number, list string, anyDM bool) {
		var allowed []string
		if list != "" {
			allowed = strings.Split(list, ",")
		}
		cfg := &config.File{AllowedNumbers: allowed, AllowAnyDM: &anyDM}
		// An entry that is not UTF-8 is refused, not mangled into one
		// that lets someone else in.
		if slices.ContainsFunc(allowed, func(a string) bool { return !utf8.ValidString(a) }) {
			if _, err := config.Resolve(cfg, ConfigPaths(), "darwin"); err == nil {
				t.Errorf("allowed_numbers %q: resolved, want an error", allowed)
			}
			return
		}
		got := NumberAllowed(resolvedSettings(t, cfg), number)
		if len(allowed) == 0 {
			if got != anyDM {
				t.Errorf("with no allowed_numbers and allow_any_dm %v, %q allowed: %v", anyDM, number, got)
			}
			return
		}
		// Digits only for what is a phone number, else as written.
		canon := func(s string) string {
			if p, err := chat.PhoneNumber(s); err == nil {
				return p
			}
			return s
		}
		// A sender that is not UTF-8 matches nothing.
		listed := utf8.ValidString(number) && slices.ContainsFunc(allowed, func(a string) bool { return canon(a) == canon(number) })
		if got != listed {
			t.Errorf("allowed_numbers %q: %q allowed: %v, listed: %v", allowed, number, got, listed)
		}
		other := !anyDM
		if NumberAllowed(resolvedSettings(t, &config.File{AllowedNumbers: allowed, AllowAnyDM: &other}), number) != got {
			t.Errorf("allowed_numbers %q: allow_any_dm changes whether %q is allowed", allowed, number)
		}
		for _, a := range allowed {
			p, err := chat.PhoneNumber(a)
			if err != nil {
				continue
			}
			// A sender, as WhatsApp reports one, in every form.
			for _, jid := range []string{p, "+" + p, p + "@s.whatsapp.net", "+" + p + "@s.whatsapp.net"} {
				if !NumberAllowed(resolvedSettings(t, cfg), chat.NormalizeJID(jid)) {
					t.Errorf("allowed_numbers %q: %q is refused", allowed, jid)
				}
			}
			// A number one digit longer is someone else.
			if canon(p+"1") != canon(number) && NumberAllowed(resolvedSettings(t, &config.File{AllowedNumbers: []string{a}, AllowAnyDM: &anyDM}), p+"1") {
				t.Errorf("allowed_numbers [%q]: %q, a longer number, is allowed", a, p+"1")
			}
		}
	})
}
