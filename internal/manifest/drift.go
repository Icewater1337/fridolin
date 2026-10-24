package manifest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
)

// Drift is how a brain's folder on disk differs from the manifest the
// owner approved: its brain.json and its scheduled agents' files. The
// bridge runs the approved one; Drift says what an edit on disk would
// change once approved, so that whoever reads brain.json is told it is
// not what counts yet.
type Drift struct {
	// BrainJSON is set when brain.json, its scheduled agents aside,
	// differs from the approved manifest.
	BrainJSON bool
	// Unreadable says why brain.json on disk cannot be read ("" when it
	// can, or when the folder does not exist).
	Unreadable string
	// ExcludeAdded are the patterns brain.json's backup.exclude and
	// backup.node_only list and the approved manifest does not: what an
	// approval would keep home too. ExcludeRemoved the other way round.
	ExcludeAdded, ExcludeRemoved []string
	// EnabledOff and CloudOff say that brain.json turns backup.enabled,
	// or backup.targets.cloud, off while the approved manifest has it on;
	// EnabledOn and CloudOn the other way round.
	EnabledOff, EnabledOn, CloudOff, CloudOn bool
	// Schedules are the scheduled agents whose file on disk differs
	// from the approved one: new, changed, removed or invalid.
	Schedules []string
}

// DriftOf compares the brain folder dir with approved. A folder that
// does not exist has no drift.
func DriftOf(dir string, approved Manifest) Drift {
	var d Drift
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return d
	}
	a := approved.Normalized()
	disk, err := Read(dir)
	if err != nil {
		d.Unreadable = err.Error()
	} else {
		n := disk.Normalized()
		d.BrainJSON = !n.EqualBrain(a)
		d.ExcludeAdded = missingFrom(n.Backup.CloudExcludes(), a.Backup.CloudExcludes())
		d.ExcludeRemoved = missingFrom(a.Backup.CloudExcludes(), n.Backup.CloudExcludes())
		d.EnabledOff = a.Backup.Enabled && !n.Backup.Enabled
		d.EnabledOn = !a.Backup.Enabled && n.Backup.Enabled
		d.CloudOff = a.Backup.Cloud() && !n.Backup.Cloud() && !d.EnabledOff
		d.CloudOn = !a.Backup.Cloud() && n.Backup.Cloud() && !d.EnabledOn
	}
	if scheds, err := ReadDiskSchedules(dir); err == nil {
		for _, r := range ReviewSchedules(scheds, a) {
			d.Schedules = append(d.Schedules, r.Name)
		}
	}
	return d
}

// missingFrom are the entries of a that b does not have, once each.
func missingFrom(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// Any reports whether there is drift at all.
func (d Drift) Any() bool {
	return d.BrainJSON || d.Unreadable != "" || len(d.Schedules) > 0
}

// Backup reports whether the drift touches the backup settings.
func (d Drift) Backup() bool {
	return len(d.ExcludeAdded) > 0 || len(d.ExcludeRemoved) > 0 || d.EnabledOff || d.EnabledOn || d.CloudOff || d.CloudOn
}

// KeepsMoreHome reports whether brain.json on disk would leave out of
// the backups, and out of a core export, something the approved manifest
// takes: it excludes a pattern the approved one does not, or turns the
// backup or its cloud target off. The owner then probably means
// something to stay on this machine. (One that cannot be read keeps its
// whole folder out of both until it is fixed.)
func (d Drift) KeepsMoreHome() bool {
	return len(d.ExcludeAdded) > 0 || d.EnabledOff || d.CloudOff
}

// Describe says what differs, in a clause or a few joined by "; ", ""
// when nothing does.
func (d Drift) Describe() string {
	if d.Unreadable != "" {
		return "brain.json on disk cannot be read (" + d.Unreadable + ")"
	}
	var parts []string
	if len(d.ExcludeAdded) > 0 {
		parts = append(parts, "excludes "+strings.Join(d.ExcludeAdded, ", ")+" (backup.exclude), which the approved one does not")
	}
	if len(d.ExcludeRemoved) > 0 {
		parts = append(parts, "no longer excludes "+strings.Join(d.ExcludeRemoved, ", ")+" (backup.exclude), which the approved one does")
	}
	switch {
	case d.EnabledOff:
		parts = append(parts, "turns backup.enabled off, which the approved one has on")
	case d.EnabledOn:
		parts = append(parts, "turns backup.enabled on, which the approved one has off")
	}
	switch {
	case d.CloudOff:
		parts = append(parts, "turns backup.targets.cloud off, which the approved one has on")
	case d.CloudOn:
		parts = append(parts, "turns backup.targets.cloud on, which the approved one has off")
	}
	var out []string
	switch {
	case len(parts) > 0:
		out = append(out, "brain.json on disk differs from the manifest you approved: it "+strings.Join(parts, "; it "))
	case d.BrainJSON:
		out = append(out, "brain.json on disk differs from the manifest you approved (not in its backup settings)")
	}
	if len(d.Schedules) > 0 {
		out = append(out, fmt.Sprintf("scheduled agents waiting for approval in .brain/schedules: %s", strings.Join(d.Schedules, ", ")))
	}
	return strings.Join(out, "; ")
}
