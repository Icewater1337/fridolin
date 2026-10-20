package manifest

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/Icewater1337/fridolin/internal/jsonx"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
)

// File is the file that turns a folder into a brain.
const File = "brain.json"

// DefaultPromptFile is the prompt file of a brain whose brain.json names
// none, and the one a new brain is scaffolded with.
const DefaultPromptFile = "brain-prompt.md"

// Manifest is a brain's portable description, stored as brain.json in
// the brain's own folder. Copy the folder to another machine (or restore it
// from a backup), add it in the dashboard, and the brain comes back with its
// routing and settings.
type Manifest struct {
	Version int    `json:"version"`
	ID      string `json:"id"`   // stable slug: history sender, sandbox profile and credential file names
	Name    string `json:"name"` // display name

	Routing Routing `json:"routing"`

	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// DisallowedTools overrides the global list; nil inherits it, [] allows all.
	DisallowedTools *[]string `json:"disallowed_tools,omitempty"`

	// PromptFile is the prompt file sent ahead of each message, relative to
	// the folder (default DefaultPromptFile). It must stay inside the folder.
	PromptFile string `json:"prompt_file,omitempty"`

	// ReplyChat is where replies go for messages that did not come from a
	// chat — files handed in through /ingest. Default: derived from routing.
	ReplyChat string `json:"reply_chat,omitempty"`

	// Observe is what the brain does with untagged group messages:
	// ObserveHistory (the default) or ObserveRun, see observe.go.
	Observe string `json:"observe,omitempty"`

	Backup Backup `json:"backup"`

	// Calendar is the Google account this brain signed in with and the
	// calendars it may use. Set by "Connect Google Calendar" in the dashboard.
	Calendar *Calendar `json:"calendar,omitempty"`

	// Role is RoleCore for the one brain that may reach the others
	// through its grants, "" for every other brain (a sub-brain).
	Role string `json:"role,omitempty"`
	// Grants are, on the core brain only, what it may do with each other
	// brain, by that brain's id: GrantDelegate, GrantRead, GrantFiles.
	Grants map[string][]string `json:"grants,omitempty"`
}

// RoleCore marks the core brain (Manifest.Role).
const RoleCore = "core"

// What the core brain may do with a sub-brain (Manifest.Grants).
const (
	// GrantDelegate lets the core hand the sub-brain a bounded task and
	// receive its result.
	GrantDelegate = "delegate"
	// GrantRead lets the core read the sub-brain's memories/**/*.md and
	// skills/**.
	GrantRead = "read"
	// GrantFiles lets the core fetch files from the sub-brain's vault
	// paths, once there is a vault.
	GrantFiles = "files"
)

// validRoles and validGrants are the values brain.json may give role and
// a grant (TestSchemaEnumsAreTheBridgesValues).
var (
	validRoles  = map[string]bool{"": true, RoleCore: true}
	validGrants = map[string]bool{GrantDelegate: true, GrantRead: true, GrantFiles: true}
)

// ValidRole reports whether role is a role brain.json may name.
func ValidRole(role string) bool { return validRoles[role] }

// Roles returns the accepted roles, the unset one included.
func Roles() []string { return slices.Sorted(maps.Keys(validRoles)) }

// GrantKinds returns what a grant may hold, in order.
func GrantKinds() []string { return slices.Sorted(maps.Keys(validGrants)) }

// IsCore reports whether the manifest is the core brain's.
func (m Manifest) IsCore() bool { return m.Role == RoleCore }

// Granted reports whether the manifest grants kind on brain id. Only a
// core brain's grants count; Validate refuses them elsewhere.
func (m Manifest) Granted(id, kind string) bool {
	return m.IsCore() && slices.Contains(m.Grants[id], kind)
}

// GrantedTo lists, sorted, the brains the manifest grants kind on, or
// every brain it grants anything on for kind "".
func (m Manifest) GrantedTo(kind string) []string {
	var out []string
	for id, kinds := range m.Grants {
		if m.IsCore() && (kind == "" && len(kinds) > 0 || m.Granted(id, kind)) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// validateGrants checks role and grants on their own: grants only on the
// core brain, never on itself, of known kinds and each kind once. Whether
// the brains they name exist is the registry's to check, against the
// other brains.
func (m *Manifest) validateGrants() error {
	if !ValidRole(m.Role) {
		return fmt.Errorf("brain %q: role %q must be %q or left out", m.ID, m.Role, RoleCore)
	}
	if len(m.Grants) > 0 && !m.IsCore() {
		return fmt.Errorf("brain %q: only the core brain (role %q) may have grants; a sub-brain cannot reach other brains", m.ID, RoleCore)
	}
	for _, id := range slices.Sorted(maps.Keys(m.Grants)) {
		if !ValidID(id) {
			return fmt.Errorf("brain %q: grant for %q, which is not a brain id", m.ID, id)
		}
		if id == m.ID {
			return fmt.Errorf("brain %q: the core brain cannot grant itself anything", m.ID)
		}
		seen := map[string]bool{}
		for _, kind := range m.Grants[id] {
			if !validGrants[kind] {
				return fmt.Errorf("brain %q: grant %q for %q must be one of %s", m.ID, kind, id, strings.Join(GrantKinds(), ", "))
			}
			if seen[kind] {
				return fmt.Errorf("brain %q: grant %q for %q is listed twice", m.ID, kind, id)
			}
			seen[kind] = true
		}
	}
	return nil
}

// Calendar is a brain's Google Calendar access. Google grants access to
// a whole account; the calendar tool keeps the brain to the calendars listed
// here.
type Calendar struct {
	Account   string   `json:"account"`
	Calendars []string `json:"calendars"`
	Default   string   `json:"default"`
}

// Routing decides which chats a brain answers.
type Routing struct {
	HandlesDMs bool     `json:"handles_dms"`
	Default    bool     `json:"default"`
	Groups     []string `json:"groups"`
	// AnswerFiles and AnswerForwards say whether, in the brain's groups, a
	// file (a photo, document, voice note or video) and a forwarded
	// message address the bridge, which then answers them. Unset is true,
	// as it always was. False makes them chatter the brain reads without
	// answering; a file is then not even downloaded, and only its caption
	// is read.
	AnswerFiles    *bool `json:"answer_files,omitempty"`
	AnswerForwards *bool `json:"answer_forwards,omitempty"`
}

// Backup controls this brain's Google Drive backup.
type Backup struct {
	Enabled bool `json:"enabled"`
	// Exclude lists rclone filter globs, relative to the brain folder:
	// "node_modules/", "*.log", "/big-folder/".
	Exclude []string `json:"exclude"`
}

// DefaultBackupExcludes returns a fresh list for each new brain: rebuildable
// or ephemeral files that only cost upload time.
func DefaultBackupExcludes() []string {
	return []string{"node_modules/", "*.log", ".DS_Store", ".venv/", "__pycache__/"}
}

// Version is the version of brain.json this bridge reads and
// writes. A newer one is refused, see checkManifestVersion.
const Version = 1

// ErrTooNew means a brain.json is of a version newer than this
// bridge reads.
var ErrTooNew = errors.New("a newer Fridolin wrote it, and this one would drop the settings it does not know")

// checkManifestVersion refuses a brain.json of version v when that is newer
// than this bridge's: it may have fields this one does not know, which
// writing it back would drop, or ones that mean something else now.
func checkManifestVersion(v int) error {
	if v > Version {
		return fmt.Errorf("version %d is newer than the version %d this build of Fridolin reads: %w", v, Version, ErrTooNew)
	}
	return nil
}

var brainIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Validate checks the manifest on its own (not against other brains).
func (m *Manifest) Validate() error {
	if !ValidID(m.ID) {
		return fmt.Errorf("brain id %q must be 1–40 lowercase letters, digits, - or _", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("brain %q needs a name", m.ID)
	}
	if !ValidEffort(m.Effort) {
		return fmt.Errorf("brain %q: effort %q must be one of low, medium, high, xhigh, max", m.ID, m.Effort)
	}
	if !ValidObserve(m.Observe) {
		return fmt.Errorf("brain %q: observe %q must be %q or %q", m.ID, m.Observe, ObserveHistory, ObserveRun)
	}
	if m.PromptFile != "" {
		if filepath.IsAbs(m.PromptFile) || strings.HasPrefix(filepath.Clean(m.PromptFile), "..") {
			return fmt.Errorf("brain %q: prompt_file must be a path inside the brain folder", m.ID)
		}
	}
	for _, g := range m.Routing.Groups {
		if !chat.IsGroupJID(g) {
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
	return m.validateGrants()
}

// Normalized returns a copy with defaults filled in, so two manifests that
// mean the same thing compare equal.
func (m Manifest) Normalized() Manifest {
	if m.Version == 0 {
		m.Version = Version
	}
	if m.PromptFile == "" {
		m.PromptFile = DefaultPromptFile
	}
	if m.Observe == "" {
		m.Observe = ObserveHistory
	}
	if m.Routing.Groups == nil {
		m.Routing.Groups = []string{}
	}
	if m.Backup.Exclude == nil {
		m.Backup.Exclude = []string{}
	}
	// Sorted, so that the same grants in another order compare equal; a
	// brain granted nothing is left out. A copy: m shares the map.
	var grants map[string][]string
	for id, kinds := range m.Grants {
		if len(kinds) > 0 {
			if grants == nil {
				grants = map[string][]string{}
			}
			grants[id] = slices.Sorted(slices.Values(kinds))
		}
	}
	m.Grants = grants
	return m
}

// Equal reports whether two manifests describe the same brain.
func (m Manifest) Equal(o Manifest) bool {
	a, _ := json.Marshal(m.Normalized())
	b, _ := json.Marshal(o.Normalized())
	return string(a) == string(b)
}

// Read loads <dir>/brain.json. It returns os.ErrNotExist (wrapped)
// when the folder is not a brain yet. The brain may write the file, and
// the bridge reads it outside the sandbox (for the drift check, too), so
// it goes as fsx.ReadBrainFile reads: nothing outside the folder, no symlink,
// only a regular file, so that a FIFO cannot hang a reload.
func Read(dir string) (*Manifest, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, File), err)
	}
	defer root.Close()
	return ReadRoot(root)
}

// ReadRoot keeps inspection and transaction reads on their pinned folder.
func ReadRoot(root *os.Root) (*Manifest, error) {
	path := filepath.Join(root.Name(), File)
	data, err := fsx.ReadBrainFileRoot(root, File)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	warnUnknownManifestKeys(path, data)
	return m, nil
}

// Decode decodes the contents of a brain.json, with defaults filled
// in. It does not validate them, but refuses a newer version's.
func Decode(data []byte) (*Manifest, error) {
	m := &Manifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, JSONError(err)
	}
	if err := checkManifestVersion(m.Version); err != nil {
		return nil, err
	}
	n := m.Normalized()
	return &n, nil
}

// loggedManifestKeys holds, for each brain.json, the unknown keys last
// logged for it: the dashboard reads every brain.json each time it asks
// for the brains, and must not log the same ones each time.
var loggedManifestKeys sync.Map // path → the keys, joined

// warnUnknownManifestKeys logs the keys data, the brain.json at path, has
// but no field takes, by their path, such as routing.handles_dm. They are
// left out where the bridge uses the manifest, and dropped from the file
// the next time the dashboard writes it.
func warnUnknownManifestKeys(path string, data []byte) {
	keys := jsonx.UnknownKeys(data, reflect.TypeFor[Manifest](), "")
	joined := strings.Join(keys, " ")
	if prev, ok := loggedManifestKeys.Swap(path, joined); ok && prev == joined || !ok && joined == "" {
		return
	}
	if len(keys) > 0 {
		log.Printf("brain %s: ignoring unknown fields %v (an edit in the dashboard drops them)", path, keys)
	}
}

// Encode returns the contents Write writes for m, or why m
// is not a valid manifest.
func Encode(m *Manifest) ([]byte, error) {
	n := m.Normalized()
	if err := n.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ErrExists means a brain.json is already there and overwrite was off.
var ErrExists = errors.New("brain.json already exists")

// Write writes <dir>/brain.json. Without overwrite it refuses to
// replace an existing file.
func Write(dir string, m *Manifest, overwrite bool) error {
	data, err := Encode(m)
	if err != nil {
		return err
	}
	return writeManifestData(dir, data, overwrite)
}

// writeManifestData writes data, a brain.json Encode made, as
// Write does. It refuses to replace a brain.json of a newer
// version, whose fields this bridge would drop.
func writeManifestData(dir string, data []byte, overwrite bool) error {
	root, err := OpenRoot(dir, overwrite)
	if err != nil {
		return err
	}
	defer root.Close()
	file, _, err := WriteDataRoot(root, data, overwrite)
	if file != nil {
		file.Close()
	}
	return err
}

// OpenRoot pins the manifest folder. Overwriting historically creates a
// missing folder; exclusive creation requires the caller to have created it.
func OpenRoot(dir string, overwrite bool) (*os.Root, error) {
	if overwrite {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return os.OpenRoot(dir)
}

// WriteDataRoot reads and publishes through one pinned folder handle.
// The returned file retains the published inode until commit or rollback, so
// removing and replacing it cannot recycle its identity while we own it.
func WriteDataRoot(root *os.Root, data []byte, overwrite bool) (*os.File, fs.FileInfo, error) {
	path := filepath.Join(root.Name(), File)
	if prev, err := fsx.ReadBrainFileRoot(root, File); overwrite && err == nil {
		var v struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(prev, &v) == nil {
			if err := checkManifestVersion(v.Version); err != nil {
				return nil, nil, fmt.Errorf("%s: %w, so it is left as it is", path, err)
			}
		}
	}
	var f *os.File
	var name string
	for {
		name = ".brain.json." + rand.Text() + ".tmp"
		var err error
		f, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		break
	}
	defer root.Remove(name)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return nil, nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	// Keep a read handle before closing the writer. A failed write/sync/close
	// must leave no partial manifest at the published name.
	retained, err := fsx.OpenInFolder(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	published := false
	defer func() {
		if !published {
			retained.Close()
		}
	}()
	retainedInfo, err := retained.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(info, retainedInfo) {
		return nil, nil, fmt.Errorf("%s changed while being written", path)
	}
	if err := f.Close(); err != nil {
		return nil, nil, err
	}
	if overwrite {
		err = root.Rename(name, File)
	} else {
		// Link publishes a complete file without replacing a raced-in one.
		err = root.Link(name, File)
		if errors.Is(err, fs.ErrExist) {
			err = ErrExists
		}
	}
	if err != nil {
		return nil, nil, err
	}
	published = true
	return retained, info, nil
}

// Slugify turns a folder or display name into a brain id candidate.
func Slugify(s string) string {
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

// Spec is a brain ready to run.
type Spec struct {
	Manifest Manifest
	Dir      string // absolute, symlinks resolved
	// Path is the folder as configured, ~ expanded. The sandbox profiles
	// cover it as well as Dir, which the brain runs in: a symlink on the
	// way to the folder, such as ~/assistant on another volume, is one a
	// brain could swap for its own and so move another brain's folder.
	Path string
	// PromptFile is the prompt file relative to Dir. The bridge reads it
	// through the brain's open folder, which stops a brain from pointing it
	// at ~/.ssh with a symlink; checking the path here would not, as the
	// brain can plant one at any time after.
	PromptFile string
	// Entry is the spec's index in brain_dirs, which its issues carry.
	Entry int `json:"-"`
}

// validEfforts are the values brain_effort and a brain's effort take, ""
// being the CLI's default. Like the other sets of values a setting takes
// (validGitConfigs, validQueueModes, validTriggerMatches, validObserve,
// waLevels), it is what the schemas in docs/schemas list for the setting,
// which TestSchemaEnumsAreTheBridgesValues checks.
var validEfforts = map[string]bool{"": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// What a brain does with untagged group messages, the ones nobody
// addressed to it (Inbound.Silent): Manifest.Observe. Either way the
// message goes into the brain's history first, and nothing is ever sent
// back for it.
const (
	// ObserveHistory, the default, keeps them in the history only. The
	// next run in that chat reads them there, which is all most brains
	// need, and chatter costs no run and holds up nobody's request.
	ObserveHistory = "history"
	// ObserveRun also gives each one an observe-only run, for a brain that
	// keeps notes on what it reads. Each costs a full Claude run, with
	// tools, on text anyone in the group may have written.
	ObserveRun = "run"
)

// validObserve are the values brain.json may give observe.
var validObserve = map[string]bool{"": true, ObserveHistory: true, ObserveRun: true}

// ValidID reports whether id is the stable slug accepted for a brain.
func ValidID(id string) bool { return brainIDRe.MatchString(id) }

// ValidEffort reports whether effort is an accepted CLI effort setting.
func ValidEffort(effort string) bool { return validEfforts[effort] }

// Efforts returns the accepted settings, including the unset value, in order.
func Efforts() []string { return slices.Sorted(maps.Keys(validEfforts)) }

// ValidObserve reports whether mode is a supported untagged-message policy.
func ValidObserve(mode string) bool { return validObserve[mode] }

// ObserveModes returns the accepted settings, including the unset value.
func ObserveModes() []string { return slices.Sorted(maps.Keys(validObserve)) }
