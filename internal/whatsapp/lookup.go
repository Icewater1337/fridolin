package whatsapp

import (
	"context"
	"errors"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// ErrNotConnected is a lookup that needs WhatsApp while the client is not
// logged in.
var ErrNotConnected = errors.New("not connected to WhatsApp")

// OnWhatsApp reports, for each of numbers (digits with the country code,
// as allowed_numbers holds them), whether it has a WhatsApp account: what
// WhatsApp's own app asks when a contact is opened. A number WhatsApp
// does not answer for is left out of the map. whatsmeow keeps the LIDs
// the answer names, as it does for anyone the bridge writes to.
func (wa *Client) OnWhatsApp(ctx context.Context, numbers []string) (map[string]bool, error) {
	cli := wa.cl()
	if cli == nil || !cli.IsLoggedIn() {
		return nil, ErrNotConnected
	}
	if len(numbers) == 0 {
		return map[string]bool{}, nil
	}
	phones := make([]string, len(numbers))
	for i, n := range numbers {
		phones[i] = "+" + n
	}
	res, err := cli.IsOnWhatsApp(ctx, phones)
	if err != nil && len(res) == 0 {
		return nil, err
	}
	// An error with answers is whatsmeow failing to keep the LIDs, which
	// the answers do not need.
	return onWhatsAppAnswers(res), nil
}

// onWhatsAppAnswers is WhatsApp's answers by the number each was asked
// for.
func onWhatsAppAnswers(res []types.IsOnWhatsAppResponse) map[string]bool {
	out := make(map[string]bool, len(res))
	for _, r := range res {
		n := strings.TrimPrefix(r.Query, "+")
		if n == "" {
			n = r.PhoneNumber.User
		}
		if n != "" {
			out[n] = out[n] || r.IsIn
		}
	}
	return out
}
