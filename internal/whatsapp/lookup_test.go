package whatsapp

import (
	"context"
	"errors"
	"maps"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

// TestOnWhatsAppNeedsAConnection: without a session the lookup says it is
// not connected, rather than guessing.
func TestOnWhatsAppNeedsAConnection(t *testing.T) {
	t.Parallel()
	wa := &Client{}
	if _, err := wa.OnWhatsApp(context.Background(), []string{"15551234567"}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("OnWhatsApp without a session: %v", err)
	}
}

// TestOnWhatsAppAnswers: WhatsApp's answers go by the number each was
// asked for, without its +.
func TestOnWhatsAppAnswers(t *testing.T) {
	t.Parallel()
	got := onWhatsAppAnswers([]types.IsOnWhatsAppResponse{
		{Query: "+15551234567", IsIn: true},
		{Query: "+15559876543", IsIn: false},
		{PhoneNumber: types.NewJID("15550001111", types.DefaultUserServer), IsIn: true}, // personal-data:ignore (a fictional 555 number)
		{},
	})
	want := map[string]bool{"15551234567": true, "15559876543": false, "15550001111": true} // personal-data:ignore (a fictional 555 number)
	if !maps.Equal(got, want) {
		t.Errorf("answers %v, want %v", got, want)
	}
}
