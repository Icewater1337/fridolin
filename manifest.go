package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ManifestFile is the file that turns a folder into a brain.
const ManifestFile = "brain.json"

// defaultPromptFile is the prompt file of a brain whose brain.json names
// none, and the one a new brain is scaffolded with.
const defaultPromptFile = "brain-prompt.md"

// BrainManifest is a brain's portable description, stored as brain.json in
// the brain's own folder. Copy the folder to another machine (or restore it
// from a backup), add it in the dashboard, and the brain comes back with its
// routing and settings.
type BrainManifest struct {
	Version int    `json:"version"`
	ID      string `json:"id"`   // stable slug: history sender, sandbox profile and credential file names
	Name    string `json:"name"` // display name

	Routing BrainRouting `json:"routing"`

	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// DisallowedTools overrides the global list; nil inherits it, [] allows all.
	DisallowedTools *[]string `json:"disallowed_tools,omitempty"`

	// PromptFile is the prompt file sent ahead of each message, relative to
	// the folder (default defaultPromptFile). It must stay inside the folder.
	PromptFile string `json:"prompt_file,omitempty"`

	// ReplyChat is where replies go for messages that did not come from a
	// chat — files handed in through /ingest. Default: derived from routing.
	ReplyChat string `json:"reply_chat,omitempty"`

	Backup BrainBackup `json:"backup"`

	// Calendar is the Google account this brain signed in with and the
	// calendars it may use. Set by "Connect Google Calendar" in the dashboard.
	Calendar *BrainCalendar `json:"calendar,omitempty"`
}

// BrainCalendar is a brain's Google Calendar access. Google grants access to
// a whole account; the calendar tool keeps the brain to the calendars listed
// here.
type BrainCalendar struct {
	Account   string   `json:"account"`
	Calendars []string `json:"calendars"`
	Default   string   `json:"default"`
}

// BrainRouting decides which chats a brain answers.
type BrainRouting struct {
	HandlesDMs bool     `json:"handles_dms"`
	Default    bool     `json:"default"`
	Groups     []string `json:"groups"`
}

// BrainBackup controls this brain's Google Drive backup.
type BrainBackup struct {
	Enabled bool `json:"enabled"`
	// Exclude lists rclone filter globs, relative to the brain folder:
	// "node_modules/", "*.log", "/big-folder/".
	Exclude []string `json:"exclude"`
}

// DefaultBackupExcludes are applied to every new brain: rebuildable or
// ephemeral files that only cost upload time.
var DefaultBackupExcludes = []string{"node_modules/", "*.log", ".DS_Store", ".venv/", "__pycache__/"}

const manifestVersion = 1

var brainIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Validate checks the manifest on its own (not against other brains).
func (m *BrainManifest) Validate() error {
	if !brainIDRe.MatchString(m.ID) {
		return fmt.Errorf("brain id %q must be 1–40 lowercase letters, digits, - or _", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("brain %q needs a name", m.ID)
	}
	if !validEfforts[m.Effort] {
		return fmt.Errorf("brain %q: effort %q must be one of low, medium, high, xhigh, max", m.ID, m.Effort)
	}
	if m.PromptFile != "" {
		if filepath.IsAbs(m.PromptFile) || strings.HasPrefix(filepath.Clean(m.PromptFile), "..") {
			return fmt.Errorf("brain %q: prompt_file must be a path inside the brain folder", m.ID)
		}
	}
	for _, g := range m.Routing.Groups {
		if !isGroupJID(g) {
			return fmt.Errorf("brain %q: %q is not a group JID (…@g.us)", m.ID, g)
		}
	}
	if c := m.Calendar; c != nil {
		for _, id := range append(append([]string{}, c.Calendars...), c.Account, c.Default) {
			if strings.ContainsAny(id, "\r\n,") {
				return fmt.Errorf("brain %q: calendar id %q is not valid", m.ID, id)
			}
		}
	}
	for _, x := range m.Backup.Exclude {
		if strings.ContainsAny(x, "\r\n") || strings.HasPrefix(strings.TrimSpace(x), "+") {
			return fmt.Errorf("brain %q: backup exclude %q is not a valid pattern", m.ID, x)
		}
	}
	return nil
}

// normalized returns a copy with defaults filled in, so two manifests that
// mean the same thing compare equal.
func (m BrainManifest) normalized() BrainManifest {
	if m.Version == 0 {
		m.Version = manifestVersion
	}
	if m.PromptFile == "" {
		m.PromptFile = defaultPromptFile
	}
	m.Routing.Groups = nonNil(m.Routing.Groups)
	m.Backup.Exclude = nonNil(m.Backup.Exclude)
	return m
}

// Equal reports whether two manifests describe the same brain.
func (m BrainManifest) Equal(o BrainManifest) bool {
	a, _ := json.Marshal(m.normalized())
	b, _ := json.Marshal(o.normalized())
	return string(a) == string(b)
}

// ReadManifest loads <dir>/brain.json. It returns os.ErrNotExist (wrapped)
// when the folder is not a brain yet. The brain may write the file, and
// the bridge reads it outside the sandbox (for the drift check, too), so
// it goes as readBrainFile reads: nothing outside the folder, no symlink,
// only a regular file, so that a FIFO cannot hang a reload.
func ReadManifest(dir string) (*BrainManifest, error) {
	path := filepath.Join(dir, ManifestFile)
	data, err := readBrainFile(dir, ManifestFile)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, err := decodeManifest(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// decodeManifest decodes the contents of a brain.json, with defaults filled
// in. It does not validate them.
func decodeManifest(data []byte) (*BrainManifest, error) {
	m := &BrainManifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, err
	}
	n := m.normalized()
	return &n, nil
}

// encodeManifest returns the contents WriteManifest writes for m, or why m
// is not a valid manifest.
func encodeManifest(m *BrainManifest) ([]byte, error) {
	n := m.normalized()
	if err := n.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// errManifestExists means a brain.json is already there and overwrite was off.
var errManifestExists = errors.New("brain.json already exists")

// WriteManifest writes <dir>/brain.json. Without overwrite it refuses to
// replace an existing file.
func WriteManifest(dir string, m *BrainManifest, overwrite bool) error {
	data, err := encodeManifest(m)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ManifestFile)
	if !overwrite {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return errManifestExists
		}
		if err != nil {
			return err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	return writeFileAtomic(path, data, 0o600, false)
}

// slugify turns a folder or display name into a brain id candidate.
func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !dash && b.Len() > 0 {
				b.WriteRune('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "brain"
	}
	return out
}
