package doctor

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// Redactor masks what check --redact and doctor --redact must not show:
// phone numbers down to the country code and the last two digits, group
// JIDs and LIDs down to their last two digits, email addresses as
// google.MaskEmail does, and the home folder, written as ~ in every path.
// Two numbers or groups still tell apart, and paths still say where under
// the home folder they are.
type Redactor struct {
	// The configured chats, which are printed without a JID too, each
	// with its masked form; the longest first, since one may hold another.
	chats []redactedChat
	homes []*regexp.Regexp
}

type redactedChat struct{ value, masked string }

// NewRedactor redacts home, as given and with its symlinks resolved, in
// each way a path is written: plain, in a regular expression, and quoted
// in a sandbox profile.
func NewRedactor(home string) *Redactor {
	r := &Redactor{}
	if home == "" || home == string(filepath.Separator) {
		return r // every path would match
	}
	var spellings []string
	for _, h := range []string{home, fsx.Resolve(home)} {
		spellings = append(spellings, h, textx.EscapeSBPL(h), regexp.QuoteMeta(h), textx.EscapeSBPL(regexp.QuoteMeta(h)))
	}
	// The longest first, since one may begin with another; equal ones next
	// to each other, for Compact.
	slices.SortFunc(spellings, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	for _, h := range slices.Compact(spellings) {
		// Only the folder itself, not a sibling whose name begins like it.
		r.homes = append(r.homes, regexp.MustCompile(regexp.QuoteMeta(h)+`([^A-Za-z0-9._-]|$)`))
	}
	return r
}

// AddConfig adds the allowed numbers and groups and group_members of the
// config file at path as they are written, read before it loads: loading
// logs the entries it finds odd, a number with a note after it say, as
// they are. A file that does not parse adds nothing.
func (r *Redactor) AddConfig(path string) {
	var file struct {
		AllowedNumbers []string            `json:"allowed_numbers"`
		AllowedGroups  []string            `json:"allowed_groups"`
		GroupMembers   map[string][]string `json:"group_members"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &file) == nil {
		r.AddChats(file.AllowedNumbers...)
		r.AddGroups(file.AllowedGroups...)
		r.AddGroupMembers(file.GroupMembers)
	}
}

// AddGroupMembers adds the groups of group_members and the numbers each
// lists, which check prints as digits alone.
func (r *Redactor) AddGroupMembers(members map[string][]string) {
	for g, list := range members {
		r.AddGroups(g)
		r.AddChats(list...)
	}
}

// AddChats adds phone numbers and chat JIDs to mask wherever they are
// printed without a JID too, as written and as digits alone.
func (r *Redactor) AddChats(chats ...string) { r.add(false, chats) }

// AddGroups adds group JIDs as AddChats does, also when one is written
// without @g.us.
func (r *Redactor) AddGroups(groups ...string) { r.add(true, groups) }

func (r *Redactor) add(groups bool, chats []string) {
	for _, c := range chats {
		id, server, _ := strings.Cut(strings.TrimSpace(c), "@")
		digits := digitsOf(id)
		if len(digits) < 6 {
			continue
		}
		masked := logx.MaskPhone(digits)
		if groups || server == "g.us" || server == "lid" {
			masked = logx.MaskDigits(digits, 0)
		}
		written := masked
		if strings.HasPrefix(id, "+") {
			written = "+" + masked
		}
		r.chats = append(r.chats, redactedChat{id, written}, redactedChat{digits, masked})
	}
	slices.SortStableFunc(r.chats, func(a, b redactedChat) int { return len(b.value) - len(a.value) })
}

// Text returns s redacted.
func (r *Redactor) Text(s string) string {
	s = logx.RedactIdentifiers(s, google.MaskEmail)
	for _, c := range r.chats {
		s = strings.ReplaceAll(s, c.value, c.masked)
	}
	for _, re := range r.homes {
		s = re.ReplaceAllString(s, "~$1")
	}
	return s
}

// Err returns err redacted, or err itself without a redactor. The
// redacted error still wraps err, so that what check exits with is the
// same with --redact: errors.Is still finds the error it was made from.
func (r *Redactor) Err(err error) error {
	if r == nil || err == nil {
		return err
	}
	return redactedError{r.Text(err.Error()), err}
}

// redactedError is an error whose text is redacted; it unwraps to the
// error it was made from.
type redactedError struct {
	text string
	err  error
}

func (e redactedError) Error() string { return e.text }

func (e redactedError) Unwrap() error { return e.err }

// Writer writes what it is given to w redacted. The log package writes
// each entry whole, so a number is never split between two writes.
func (r *Redactor) Writer(w io.Writer) io.Writer { return redactingWriter{w, r} }

type redactingWriter struct {
	w   io.Writer
	red *Redactor
}

func (rw redactingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.red.Text(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// digitsOf is s without anything but its digits.
func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
