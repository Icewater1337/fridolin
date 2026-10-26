package doctor

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactorText(t *testing.T) {
	// Made-up numbers outside the placeholders' country carry
	// personal-data:ignore.
	r := NewRedactor("/srv/first.last")
	r.AddChats("15551234567", "+15559876543", "15559876543@s.whatsapp.net")
	r.AddGroups("120363000000000001@g.us", "120363000000000009")
	cases := []struct{ in, want string }{
		{"15551234567@s.whatsapp.net", "1…67@s.whatsapp.net"},
		{"15551234567:12@s.whatsapp.net", "1…67@s.whatsapp.net"},           // a device's JID
		{"491701234567@s.whatsapp.net", "49…67@s.whatsapp.net"},            // a two-digit country code; personal-data:ignore
		{"3530000000099@s.whatsapp.net", "353…99@s.whatsapp.net"},          // a three-digit one; personal-data:ignore
		{"120363000000000001@g.us", "…01@g.us"},                            // a group
		{"15551234567-1600000000@g.us", "…00@g.us"},                        // a group named by its creator's number; personal-data:ignore
		{strings.Repeat("7", 15) + "@lid", "…77@lid"},                      // a LID
		{"[15551234567 15559876543]", "[1…67 1…43]"},                       // numbers configured or routed
		{`"120363000000000009" (digits@g.us)`, `"…09" (digits@g.us)`},      // a group written without @g.us
		{"+15559876543 and +4917012345678", "+1…43 and +49…78"},            // international format; personal-data:ignore
		{"you@example.com", "y…@example.com"},                              // as GoogleMaskEmail
		{"/srv/first.last/.fridolin", "~/.fridolin"},                       // a path
		{`(subpath "/srv/first.last")`, `(subpath "~")`},                   // the folder itself
		{`(regex "^/srv/first\\.last/x(/.*)?$")`, `(regex "^~/x(/.*)?$")`}, // in a sandbox profile's regular expression
		{"/srv/first.lastname/x", "/srv/first.lastname/x"},                 // another folder
		{"127.0.0.1:8765 and 2.1.400", "127.0.0.1:8765 and 2.1.400"},       // what is no number
		{"12345@g.us", "…@g.us"},                                           // too short to keep any digit
		{"allowed_groups (digits@g.us)", "allowed_groups (digits@g.us)"},   // no JID, and no address either
		{"brain-120363000000000005@g.us", "brain-…05@g.us"},                // a JID right after a name
		{"main.120363000000000005@g.us", "main.…05@g.us"},                  // or a word
		{"x15550001111@s.whatsapp.net", "x1…11@s.whatsapp.net"},            // a number's; personal-data:ignore
		{"x" + strings.Repeat("6", 15) + "@lid", "x…66@lid"},               // a LID's
	}
	for _, c := range cases {
		if got := r.Text(c.in); got != c.want {
			t.Errorf("Text(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := NewRedactor("/").Text("/usr/bin"); got != "/usr/bin" {
		t.Errorf("a home of / redacted %q", got)
	}
}

// TestRedactorConfig: the chats of a config file are masked as written,
// read before it loads; a short number is no chat, and a file that does
// not parse adds nothing.
func TestRedactorConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"allowed_numbers": ["+1 555 123 4567 (cell)", "1234"], "allowed_groups": ["120363000000000001@g.us"], "group_members": {"120363000000000002@g.us": ["15559876543"]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRedactor("")
	r.AddConfig(path)
	for _, s := range []string{"+1 555 123 4567 (cell)", "120363000000000001", "120363000000000002", "15559876543"} {
		if got := r.Text(s); strings.Contains(got, s) {
			t.Errorf("%q, from the config file, is redacted to %q", s, got)
		}
	}
	if got := r.Text("1234"); got != "1234" {
		t.Errorf("a short number is redacted to %q", got)
	}

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = NewRedactor("")
	r.AddConfig(path)
	r.AddConfig(filepath.Join(dir, "missing.json"))
	if len(r.chats) != 0 {
		t.Errorf("a config that does not parse added %+v", r.chats)
	}
}

// TestRedactorErrAndWriter: a redacted error still wraps the one it was
// made from, no redactor leaves an error as it is, and the writer passes
// on what it is given redacted, or the error of the writer below.
func TestRedactorErrAndWriter(t *testing.T) {
	r := NewRedactor("/home/you")
	base := errors.New("cannot read /home/you/x")
	err := r.Err(base)
	if err.Error() != "cannot read ~/x" || !errors.Is(err, base) {
		t.Errorf("redacted error %q, wraps it: %t", err, errors.Is(err, base))
	}
	var none *Redactor
	if none.Err(base) != base || r.Err(nil) != nil {
		t.Error("Err changed an error it was not to redact")
	}

	var buf bytes.Buffer
	n, werr := r.Writer(&buf).Write([]byte("wrote /home/you/y\n"))
	if werr != nil || n != len("wrote /home/you/y\n") || buf.String() != "wrote ~/y\n" {
		t.Errorf("Write: %d %v %q", n, werr, buf.String())
	}
	if _, werr := r.Writer(failingWriter{}).Write([]byte("x")); werr == nil {
		t.Error("the writer below failed, and Write did not say so")
	}
}
