package vault

import (
	"path"
	"regexp"
	"strings"
	"time"
)

// Tier says where a file's bytes are.
type Tier string

const (
	TierHot  Tier = "hot"  // on disk only: not uploaded yet, or changed since
	TierCold Tier = "cold" // in object storage only: evicted
	TierBoth Tier = "both" // on disk, and the same bytes are in object storage
)

// Class is a file's placement class (docs/PLAN.md, Workstream D).
type Class string

const (
	ClassPinned       Class = "pinned"        // always hot
	ClassActive       Class = "active"        // hot while active
	ClassColdEligible Class = "cold-eligible" // may be evicted, least recently used first
)

// Provenance is the trust class of whatever produced a file.
type Provenance string

const (
	ProvenanceOwner     Provenance = "owner"
	ProvenanceAgent     Provenance = "agent"
	ProvenanceUntrusted Provenance = "untrusted"
	ProvenanceSystem    Provenance = "system"
)

func (p Provenance) valid() bool {
	switch p {
	case ProvenanceOwner, ProvenanceAgent, ProvenanceUntrusted, ProvenanceSystem:
		return true
	}
	return false
}

// Reasons a file has its class, as Decision.Reason reports them.
const (
	ReasonPinned        = "pinned"              // matches a pinned pattern
	ReasonAuditCurrent  = "audit-current-month" // audit/ of the current month
	ReasonOpenThread    = "open-thread"         // an open thread refers to it
	ReasonHydrated      = "hydrated"            // a job hydrated it recently
	ReasonRecent        = "recent"              // read or written within the activity window
	ReasonMarkdownFirst = "markdown-first"      // a binary without a companion, younger than cold_after_days
	ReasonTranscribed   = "transcribed-media"   // media/ original whose companion exists
	ReasonOldRunLog     = "old-run-log"         // runs/ log older than the run-log window
	ReasonAuditPast     = "audit-past-month"    // audit/ of an earlier month
	ReasonWorkspace     = "archived-workspace"  // workspaces/ archive
	ReasonInactive      = "inactive"            // nothing keeps it hot
)

// Policy is the placement policy. The zero value is not useful; start from
// DefaultPolicy.
type Policy struct {
	// Pinned are patterns of paths (relative to the hot root, slash-
	// separated) that are always hot. A segment "**" matches any number
	// of segments, including none; other segments are path.Match patterns.
	Pinned []string
	// ActivityDays: a file read or written within this many days is active.
	ActivityDays int
	// ColdAfterDays: markdown-first — a binary without a markdown
	// companion becomes cold-eligible only after this many days unused.
	ColdAfterDays int
	// HydratedHoldDays: a file a job hydrated stays active this long.
	HydratedHoldDays int
	// RunLogDays: run logs (runs/, run/) unused this long are cold-eligible
	// even inside the activity window.
	RunLogDays int
	// Companions lists the paths whose existence makes the binary at rel
	// "described" (a transcript, summary or index entry). Nil uses
	// DefaultCompanions.
	Companions func(rel string) []string
}

// DefaultPolicy is the plan's default placement policy.
func DefaultPolicy() Policy {
	return Policy{
		Pinned: []string{
			"config.json",
			"bridge.db", "bridge.db-*",
			"vault.db", "vault.db-*",
			"secrets/**",
			"**/brain.json",
			"**/brain-prompt.md",
			"**/skills/**",
			"**/memories/**/*.md",
			"**/conversation-history.jsonl",
		},
		ActivityDays:     60,
		ColdAfterDays:    90,
		HydratedHoldDays: 7,
		RunLogDays:       14,
	}
}

// textExtensions are the extensions of files that are their own markdown
// companion: the markdown-first rule applies only to the others.
var textExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".json": true, ".jsonl": true,
	".csv": true, ".tsv": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".xml": true, ".html": true, ".htm": true, ".log": true,
	".srt": true, ".vtt": true, ".ics": true, ".vcf": true, ".sh": true,
	".py": true, ".go": true, ".js": true, ".ts": true, ".css": true,
}

// IsBinary reports whether the markdown-first rule treats rel as a binary:
// anything that is not a known text format.
func IsBinary(rel string) bool {
	return !textExtensions[strings.ToLower(path.Ext(rel))]
}

// DefaultCompanions are the companions of a binary dir/stem.ext: a
// transcript or summary beside it, as the transcription, media and Plaud
// skills write them (stem.txt, stem.md, stem.ext.md, stem.transcript.md,
// stem.summary.md and the like).
func DefaultCompanions(rel string) []string {
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	return []string{
		stem + ".md", stem + ".txt",
		rel + ".md", rel + ".txt",
		stem + ".transcript.md", stem + ".transcript.txt",
		stem + ".summary.md",
	}
}

func (p Policy) companions(rel string) []string {
	if p.Companions != nil {
		return p.Companions(rel)
	}
	return DefaultCompanions(rel)
}

// Facts are what the policy needs to know about one file.
type Facts struct {
	Path         string
	LastAccess   time.Time
	HydratedAt   time.Time // zero: never hydrated
	HasCompanion bool
	InOpenThread bool
}

// Decision is a file's class and why.
type Decision struct {
	Class  Class
	Pinned bool
	Reason string
}

// monthPrefix matches a path segment that starts with a YYYY-MM month.
var monthPrefix = regexp.MustCompile(`^(\d{4}-\d{2})`)

// auditMonth returns the month (YYYY-MM) of an audit/ path, if any of its
// segments below audit/ starts with one.
func auditMonth(rel string) (string, bool) {
	segs := strings.Split(rel, "/")
	if len(segs) < 2 || segs[0] != "audit" {
		return "", false
	}
	for _, s := range segs[1:] {
		if m := monthPrefix.FindString(s); m != "" {
			return m, true
		}
	}
	return "", false
}

func underDir(rel, dir string) bool {
	return strings.HasPrefix(rel, dir+"/") || strings.Contains(rel, "/"+dir+"/")
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// Classify places one file. The rules, first match wins:
//
//  1. pinned patterns, and audit/ of the current month: pinned;
//  2. referenced by an open thread, or hydrated within HydratedHoldDays:
//     active;
//  3. media originals with a companion, run logs unused for RunLogDays,
//     audit/ of earlier months and workspaces/ archives: cold-eligible,
//     even inside the activity window;
//  4. read or written within ActivityDays: active;
//  5. markdown-first: a binary without a companion unused for less than
//     ColdAfterDays: active;
//  6. everything else: cold-eligible.
func (p Policy) Classify(f Facts, now time.Time) Decision {
	if p.matchPinned(f.Path) {
		return Decision{ClassPinned, true, ReasonPinned}
	}
	month, isAudit := auditMonth(f.Path)
	if isAudit && month == now.Format("2006-01") {
		return Decision{ClassPinned, true, ReasonAuditCurrent}
	}
	if f.InOpenThread {
		return Decision{Class: ClassActive, Reason: ReasonOpenThread}
	}
	if !f.HydratedAt.IsZero() && now.Sub(f.HydratedAt) < days(p.HydratedHoldDays) {
		return Decision{Class: ClassActive, Reason: ReasonHydrated}
	}
	idle := now.Sub(f.LastAccess)
	binary := IsBinary(f.Path)
	switch {
	case underDir(f.Path, "media") && binary && f.HasCompanion:
		return Decision{Class: ClassColdEligible, Reason: ReasonTranscribed}
	case (strings.HasPrefix(f.Path, "runs/") || strings.HasPrefix(f.Path, "run/")) && idle >= days(p.RunLogDays):
		return Decision{Class: ClassColdEligible, Reason: ReasonOldRunLog}
	case isAudit:
		return Decision{Class: ClassColdEligible, Reason: ReasonAuditPast}
	case strings.HasPrefix(f.Path, "workspaces/"):
		return Decision{Class: ClassColdEligible, Reason: ReasonWorkspace}
	}
	if idle < days(p.ActivityDays) {
		return Decision{Class: ClassActive, Reason: ReasonRecent}
	}
	if binary && !f.HasCompanion && idle < days(p.ColdAfterDays) {
		return Decision{Class: ClassActive, Reason: ReasonMarkdownFirst}
	}
	return Decision{Class: ClassColdEligible, Reason: ReasonInactive}
}

func (p Policy) matchPinned(rel string) bool {
	segs := strings.Split(rel, "/")
	for _, pat := range p.Pinned {
		if matchSegments(strings.Split(pat, "/"), segs) {
			return true
		}
	}
	return false
}

// MatchPattern reports whether the slash-separated rel matches pattern, in
// which a "**" segment matches any number of segments.
func MatchPattern(pattern, rel string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
