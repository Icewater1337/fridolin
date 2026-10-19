package main

import (
	"strings"
	"testing"
)

func TestIdentityKey(t *testing.T) {
	cases := map[string]string{
		"15551234567":                    "15551234567",
		"+1 555 123 4567":                "15551234567",
		"+1 (555) 123-4567":              "15551234567",
		"+1\u00a0555\u00a0123\u00a04567": "15551234567", // no-break spaces
		"15551234567@s.whatsapp.net":     "15551234567",
		bridgeLID + "@lid":               bridgeLID,
		"1 555 123 4567@lid":             "15551234567", // keys as its key does
		"Alex":                           "Alex",
	}
	for in, want := range cases {
		if got := identityKey(in); got != want {
			t.Errorf("identityKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzPhoneNumber: whatever is typed into the dashboard's "Add number"
// box, what reaches allowed_numbers is 6 to 15 digits, all of them from
// the input, and it goes in unchanged a second time.
func FuzzPhoneNumber(f *testing.F) {
	for _, s := range []string{"15551234567", "+1 (555) 123-4567", "15551234567@s.whatsapp.net", "", "+", "1-2", "١٥٥٥١٢٣٤٥٦٧"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		n, err := phoneNumber(in)
		if err != nil {
			return
		}
		if !phoneNumberRE.MatchString(n) {
			t.Fatalf("phoneNumber(%q) = %q, not 6 to 15 digits", in, n)
		}
		digits := strings.Map(func(r rune) rune {
			if '0' <= r && r <= '9' {
				return r
			}
			return -1
		}, in)
		if !strings.Contains(digits, n) {
			t.Errorf("phoneNumber(%q) = %q, digits that are not in the input", in, n)
		}
		if again, err := phoneNumber(n); err != nil || again != n {
			t.Errorf("phoneNumber(%q) = %q, %v; want it unchanged", n, again, err)
		}
	})
}
