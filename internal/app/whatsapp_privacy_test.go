package app

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/logx"
)

func TestMaskChatJID(t *testing.T) {
	cases := map[string]string{
		"120363000000000000@g.us":    "120363000000000000@g.us",
		"15551234567@s.whatsapp.net": logx.MaskPhone("15551234567") + "@s.whatsapp.net",
		"15559876543@lid":            "…43@lid",
	}
	for chat, want := range cases {
		if got := maskChatJID(chat); got != want {
			t.Errorf("maskChatJID(%q) = %q, want %q", chat, got, want)
		}
	}
}
