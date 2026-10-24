package manifest

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/jsonx"
)

// BrainDir is the brain's folder for the definitions Fridolin manages
// (docs/BRAINS.md, "The .brain folder"): one file per thing, each
// approved by the owner on its own, so that a brain or the owner can add
// one without touching brain.json, and a review shows just that file.
// Scheduled agents are the first to live there.
const BrainDir = ".brain"

// ScheduleDir is where a brain's scheduled agents live, one file each:
// .brain/schedules/<name>.json, a Schedule.
const ScheduleDir = BrainDir + "/schedules"

// ScheduleFile is the file of scheduled agent name, relative to the brain
// folder (with forward slashes on every system).
func ScheduleFile(name string) string { return ScheduleDir + "/" + name + ".json" }

// Normalized returns a copy with defaults dropped, so two schedules that
// mean the same thing compare equal: enabled true is unset, and an empty
// map or list is none.
func (s Schedule) Normalized() Schedule {
	if s.Enabled != nil && *s.Enabled {
		s.Enabled = nil
	}
	if len(s.Vars) == 0 {
		s.Vars = nil
	}
	if len(s.Preapproved) == 0 {
		s.Preapproved = nil
	}
	s.FromMemory = false
	return s
}

// Equal reports whether two schedules describe the same agent.
func (s Schedule) Equal(o Schedule) bool {
	a, _ := json.Marshal(s.Normalized())
	b, _ := json.Marshal(o.Normalized())
	return bytes.Equal(a, b)
}

// EncodeSchedule returns the contents of s's file, or why s is not a
// valid schedule.
func EncodeSchedule(s Schedule) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return encodeScheduleAnyway(s), nil
}

// encodeScheduleAnyway is the file of s, valid or not: an inline entry
// moves into its file as it is, and is then refused there.
func encodeScheduleAnyway(s Schedule) []byte {
	data, _ := json.MarshalIndent(s.Normalized(), "", "  ")
	return append(data, '\n')
}

// DecodeSchedule reads the file of agent name. The file's name is the
// agent's: a "name" in it must be the same, and may be left out.
func DecodeSchedule(name string, data []byte) (Schedule, error) {
	s, _, err := decodeSchedule(name, data)
	return s, err
}

// decodeSchedule is DecodeSchedule, and says whether the file could be
// read at all: one that is valid JSON of a schedule may still be refused.
func decodeSchedule(name string, data []byte) (s Schedule, readable bool, err error) {
	if err := json.Unmarshal(data, &s); err != nil {
		return s, false, JSONError(err)
	}
	if s.Name == "" {
		s.Name = name
	}
	if s.Name != name {
		return s, false, fmt.Errorf("its name is %q, but the file is %s: the file's name is the agent's", s.Name, ScheduleFile(name))
	}
	if keys := jsonx.UnknownKeys(data, reflect.TypeFor[Schedule](), ""); len(keys) > 0 {
		return s, true, fmt.Errorf("unknown fields %s", strings.Join(keys, ", "))
	}
	return s, true, s.Validate()
}

// DiskSchedule is one scheduled agent as a brain's folder holds it: its
// file in .brain/schedules, or an entry of brain.json's schedules.
type DiskSchedule struct {
	Name string `json:"name"`
	// Source is where it is: its file, or brain.json for an entry there.
	Source string `json:"source"`
	// Schedule is what it says; nil when it cannot be read at all.
	Schedule *Schedule `json:"schedule,omitempty"`
	// Err says why it cannot run as it is, "" when it is valid.
	Err string `json:"error,omitempty"`
}

// ReadDiskSchedules reads the scheduled agents in the brain folder dir:
// the files in .brain/schedules and (compat: brain.json before
// .brain) the entries of its schedules, which a file of the same name
// replaces. The files are read as the bridge reads a brain's files: no
// symlink, nothing outside the folder (fsx.OpenInFolder).
func ReadDiskSchedules(dir string) ([]DiskSchedule, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	out, err := readScheduleFiles(root)
	if err != nil {
		return nil, err
	}
	m, err := ReadRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, s := range m.Schedules {
		if i := slices.IndexFunc(out, func(d DiskSchedule) bool { return d.Name == s.Name }); i >= 0 {
			continue // the file replaces it
		}
		d := DiskSchedule{Name: s.Name, Source: File, Schedule: &s}
		if err := s.Validate(); err != nil {
			d.Err = err.Error()
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b DiskSchedule) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// readScheduleFiles reads root's .brain/schedules/*.json, sorted by name.
// One that is not an agent's file (another extension, a hidden file, a
// folder) is left alone; one whose name is no agent's is listed with its
// error.
func readScheduleFiles(root *os.Root) ([]DiskSchedule, error) {
	fi, err := root.Lstat(ScheduleDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err == nil && !fi.IsDir() {
		err = errors.New("not a folder")
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ScheduleDir, err)
	}
	f, err := root.Open(ScheduleDir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ScheduleDir, err)
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ScheduleDir, err)
	}
	var out []DiskSchedule
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(e.Name(), ".") || e.IsDir() {
			continue
		}
		rel := ScheduleDir + "/" + e.Name()
		d := DiskSchedule{Name: name, Source: rel}
		if len(out) >= MaxSchedules {
			d.Err = fmt.Sprintf("a brain has at most %d scheduled agents", MaxSchedules)
			out = append(out, d)
			continue
		}
		if !ValidScheduleName(name) {
			d.Err = fmt.Sprintf("the file name %q is no agent's name: 1–40 lowercase letters, digits or -, then .json", e.Name())
			out = append(out, d)
			continue
		}
		data, err := fsx.ReadBrainFileRoot(root, rel)
		if err != nil {
			d.Err = err.Error()
			out = append(out, d)
			continue
		}
		s, readable, err := decodeSchedule(name, data)
		if err != nil {
			d.Err = err.Error()
		}
		if !readable {
			out = append(out, d)
			continue
		}
		d.Schedule = &s
		out = append(out, d)
	}
	return out, nil
}

// Review states of a scheduled agent on disk against the approved one
// (ScheduleReview.Status).
const (
	// ReviewNew is an agent on disk the owner has not approved yet.
	ReviewNew = "new"
	// ReviewChanged is an approved agent whose file changed since.
	ReviewChanged = "changed"
	// ReviewRemoved is an approved agent whose file is gone.
	ReviewRemoved = "removed"
	// ReviewInvalid is an agent on disk that cannot run as it is: the
	// approved one, if there is one, runs on.
	ReviewInvalid = "invalid"
)

// ScheduleReview is a scheduled agent whose definition on disk differs
// from the one the owner approved, which is what runs until they approve
// this one.
type ScheduleReview struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Source is where it is on disk: its file, or brain.json.
	Source   string    `json:"source"`
	Disk     *Schedule `json:"disk,omitempty"`
	Approved *Schedule `json:"approved,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// ReviewSchedules compares the scheduled agents on disk with approved's
// (its schedules, not the one memory.consolidate stands for) and returns
// those that differ, by name.
func ReviewSchedules(disk []DiskSchedule, approved Manifest) []ScheduleReview {
	var out []ScheduleReview
	onDisk := map[string]bool{}
	for _, d := range disk {
		onDisk[d.Name] = true
		var a *Schedule
		if i := slices.IndexFunc(approved.Schedules, func(s Schedule) bool { return s.Name == d.Name }); i >= 0 {
			s := approved.Schedules[i]
			a = &s
		}
		r := ScheduleReview{Name: d.Name, Source: d.Source, Disk: d.Schedule, Approved: a, Detail: d.Err}
		switch {
		case d.Err != "":
			r.Status = ReviewInvalid
		case a == nil:
			r.Status = ReviewNew
		case !d.Schedule.Equal(*a):
			r.Status = ReviewChanged
		default:
			continue
		}
		out = append(out, r)
	}
	for _, s := range approved.Schedules {
		if !onDisk[s.Name] {
			a := s
			out = append(out, ScheduleReview{Name: s.Name, Status: ReviewRemoved, Source: ScheduleFile(s.Name), Approved: &a})
		}
	}
	slices.SortFunc(out, func(a, b ScheduleReview) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// WithoutSchedules is m without its schedules: what brain.json's own
// approval covers, each scheduled agent being approved on its own.
func (m Manifest) WithoutSchedules() Manifest {
	m.Schedules = nil
	return m
}

// EqualBrain reports whether two manifests describe the same brain, its
// scheduled agents left aside.
func (m Manifest) EqualBrain(o Manifest) bool {
	return m.WithoutSchedules().Equal(o.WithoutSchedules())
}

// InlineScheduleFiles are the files that brain.json's schedules in dir
// move into, by path: each entry that has a valid name and no file yet.
// The bridge writes them, and brain.json without its schedules, whenever
// it writes a brain's brain.json (compat: brain.json held them before
// .brain).
func InlineScheduleFiles(dir string) (map[string][]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	m, err := ReadRoot(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, s := range m.Schedules {
		if !ValidScheduleName(s.Name) {
			continue
		}
		rel := ScheduleFile(s.Name)
		if _, err := root.Lstat(rel); err == nil {
			continue
		}
		out[rel] = encodeScheduleAnyway(s)
	}
	return out, nil
}

// MigrateInlineSchedules moves the entries of brain.json's schedules in
// dir into their files in .brain/schedules and takes them out of
// brain.json, leaving the rest of it as it is, unknown keys and order
// included. An entry whose name is no file's, or whose file holds
// something else, stays. What the folder means does not change, nor
// what is approved: the approved copy of a brain keeps its agents
// wherever they are on disk. It returns the names it moved.
func MigrateInlineSchedules(dir string) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, err := fsx.ReadBrainFileRoot(root, File)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys, raws, err := objectMembers(data)
	if err != nil {
		return nil, err
	}
	i := slices.Index(keys, "schedules")
	if i < 0 {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raws[i], &entries); err != nil {
		return nil, nil // the bridge reports it; nothing to move
	}
	var keep []json.RawMessage
	var moved []string
	for _, raw := range entries {
		var s Schedule
		if json.Unmarshal(raw, &s) != nil || !ValidScheduleName(s.Name) {
			keep = append(keep, raw)
			continue
		}
		rel := ScheduleFile(s.Name)
		if prev, err := fsx.ReadBrainFileRoot(root, rel); err == nil {
			if d, err := DecodeSchedule(s.Name, prev); err == nil && d.Equal(s) {
				moved = append(moved, s.Name) // already there
				continue
			}
			keep = append(keep, raw)
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			keep = append(keep, raw)
			continue
		}
		if err := writeInRoot(root, rel, encodeScheduleAnyway(s)); err != nil {
			return moved, err
		}
		moved = append(moved, s.Name)
	}
	if len(moved) == 0 {
		return nil, nil
	}
	if len(keep) == 0 {
		keys, raws = slices.Delete(keys, i, i+1), slices.Delete(raws, i, i+1)
	} else {
		raws[i], _ = json.Marshal(keep)
	}
	out, err := encodeMembers(keys, raws)
	if err != nil {
		return moved, err
	}
	f, _, err := WriteDataRoot(root, out, true)
	if f != nil {
		f.Close()
	}
	if err != nil {
		return moved, err
	}
	log.Printf("brain %s: moved scheduled agents %v from brain.json into %s", dir, moved, ScheduleDir)
	return moved, nil
}

// objectMembers splits a JSON object into its keys and values, in order.
func objectMembers(data []byte) ([]string, []json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, fmt.Errorf("%s is no JSON object", File)
	}
	var keys []string
	var raws []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, nil, err
		}
		keys, raws = append(keys, k), append(raws, raw)
	}
	return keys, raws, nil
}

// encodeMembers writes an object of keys and values, indented as Encode
// indents brain.json.
func encodeMembers(keys []string, raws []json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		key, _ := json.Marshal(k)
		b.WriteString("\n  ")
		b.Write(key)
		b.WriteString(": ")
		var v bytes.Buffer
		if err := json.Indent(&v, raws[i], "  ", "  "); err != nil {
			return nil, err
		}
		b.Write(v.Bytes())
	}
	if len(keys) > 0 {
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// writeInRoot writes rel in root at once, through a temporary file
// beside it, making its folder when it is not there.
func writeInRoot(root *os.Root, rel string, data []byte) error {
	if err := root.MkdirAll(path.Dir(rel), 0o700); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(rel), "."+path.Base(rel)+"."+rand.Text()+".tmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, rel)
}

// WriteInFolder writes rel, relative to the brain folder root, at once,
// making its folder when it is not there: what the bridge does with a
// scheduled agent's file.
func WriteInFolder(root *os.Root, rel string, data []byte) error { return writeInRoot(root, rel, data) }
