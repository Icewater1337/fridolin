package pipeline

import "testing"

// The owner's commands are taken in any case, and the past tense the
// owner types now and then means the command; other words are none.
func TestOwnerCommandOf(t *testing.T) {
	t.Parallel()
	for word, want := range map[string]string{
		"!approve": "!approve", "!APPROVE": "!approve", "!approved": "!approve", "!Approved": "!approve",
		"!denied": "!deny", "!revoked": "!revoke", "!join": "!join",
		"!approves": "", "approve": "", "!help": "", "": "",
	} {
		if got := ownerCommandOf(word); got != want {
			t.Errorf("ownerCommandOf(%q) = %q, want %q", word, got, want)
		}
	}
}
