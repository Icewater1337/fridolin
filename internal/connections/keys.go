package connections

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The keys, one file a connection in Dir, 0600: openai.json holds the key
// and the optional organization and project, elevenlabs.json the key.
// The dashboard learns only whether one is set and its last characters.

// key is a connection's file.
type key struct {
	APIKey       string `json:"api_key"`
	Organization string `json:"organization"`
	Project      string `json:"project"`
}

// KeyUpdate is what the dashboard saves. A blank APIKey keeps the stored
// key; Organization and Project, OpenAI's only, keep theirs when nil.
type KeyUpdate struct {
	APIKey       string
	Organization *string
	Project      *string
}

// idRE is an OpenAI organization or project id: it goes into a header.
var idRE = regexp.MustCompile(`^[A-Za-z0-9_-]{0,128}$`)

// KeyPath is the file connection id's key is kept in.
func (s *Service) KeyPath(id string) string { return filepath.Join(s.o.Dir, id+".json") }

// loadKey reads connection id's key; ok is false when none is set.
func (s *Service) loadKey(id string) (key, bool) {
	data, err := os.ReadFile(s.KeyPath(id))
	if err != nil {
		return key{}, false
	}
	var k key
	if json.Unmarshal(data, &k) != nil || k.APIKey == "" {
		return key{}, false
	}
	return k, true
}

// Configured reports whether connection id has a key.
func (s *Service) Configured(id string) bool { _, ok := s.loadKey(id); return ok }

// validKey reports whether k can be a key: 8 to 512 printable characters
// of ASCII, without spaces.
func validKey(k string) bool {
	if len(k) < 8 || len(k) > 512 {
		return false
	}
	return !strings.ContainsFunc(k, func(r rune) bool { return r <= ' ' || r > '~' })
}

// SetKey saves connection id's key as u says.
func (s *Service) SetKey(id string, u KeyUpdate) error {
	if !Known(id) {
		return fail(http.StatusNotFound, "no connection %q", id)
	}
	old, had := s.loadKey(id)
	next := old
	if k := strings.TrimSpace(u.APIKey); k != "" {
		if !validKey(k) {
			return fail(http.StatusBadRequest, "that is not a key: it must be 8 to 512 characters, letters, digits and punctuation, no spaces")
		}
		next.APIKey = k
	} else if !had {
		return fail(http.StatusBadRequest, "enter the %s key", Title(id))
	}
	for _, f := range []struct {
		name string
		in   *string
		out  *string
	}{{"organization", u.Organization, &next.Organization}, {"project", u.Project, &next.Project}} {
		if f.in == nil {
			continue
		}
		v := strings.TrimSpace(*f.in)
		if id != OpenAI && v != "" {
			return fail(http.StatusBadRequest, "%s takes no %s", Title(id), f.name)
		}
		if !idRE.MatchString(v) {
			return fail(http.StatusBadRequest, "the %s must be an id such as org-… or proj_…: letters, digits, _ and -", f.name)
		}
		*f.out = v
	}
	var data []byte
	if id == OpenAI {
		data, _ = json.MarshalIndent(next, "", "  ")
	} else {
		data, _ = json.MarshalIndent(map[string]string{"api_key": next.APIKey}, "", "  ")
	}
	return writeAtomic(s.KeyPath(id), append(data, '\n'))
}

// RemoveKey deletes connection id's key. Usage and config stay.
func (s *Service) RemoveKey(id string) error {
	if !Known(id) {
		return fail(http.StatusNotFound, "no connection %q", id)
	}
	if err := os.Remove(s.KeyPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// hint is the end of a key, for people to tell keys apart: its last four
// characters, and nothing for a key too short to spare them.
func hint(k string) string {
	if len(k) < 12 {
		return ""
	}
	return "…" + k[len(k)-4:]
}

// writeAtomic writes data to path, 0600, through a temporary file in
// the same 0700 folder, so that a reader sees the old file or the new.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
