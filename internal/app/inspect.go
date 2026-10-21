package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/google"
	"github.com/Icewater1337/fridolin/internal/logx"
	"github.com/Icewater1337/fridolin/internal/registry"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// CheckOptions are the check command's flags.
type CheckOptions struct {
	Config     string
	Routes     []string
	SandboxFor string
	// TrySandbox runs a shell in this brain's sandbox, which says what it
	// sees (TrySandbox in sandbox_try.go).
	TrySandbox string
	// TryCommand, with TrySandbox, is a program and its arguments to run
	// there instead, with the environment a brain's run has.
	TryCommand []string
	Redact     bool
}

// ErrBrainsLeftOut is what check ends with when an approved brain cannot
// run: its folder is missing or cannot hold a brain, or it clashes with
// another. The bridge runs the others, and the issue is shown on the
// Brains page, but that was easy to miss: the DM brain left out like that
// used to hand your DMs to the default brain.
var ErrBrainsLeftOut = errors.New("cannot run")

// leftOutBrains returns the ids of the approved brains issues leaves out,
// each once, in their order.
func leftOutBrains(issues []registry.Issue) []string {
	var ids []string
	for _, is := range issues {
		if is.LeavesOut() && !slices.Contains(ids, is.ID) {
			ids = append(ids, is.ID)
		}
	}
	return ids
}

// WriteCheck writes check's report to w. With o.Redact the report, what
// is logged meanwhile and the error that ends it, if one does, are
// redacted: the output then goes into a public bug report as it is.
func WriteCheck(w io.Writer, o CheckOptions) error {
	inputs := ConfigOptions()
	var red *redactor
	if o.Redact {
		red = newRedactor(inputs.Paths.Home)
		red.addChats(o.Routes...)
		// The log entries of loading the config name paths, and the
		// allowed chats it finds odd, as they are written.
		prev := log.Writer()
		log.SetOutput(redactingWriter{prev, red})
		defer log.SetOutput(prev)
	}
	path, _ := ConfigPathFromPaths(o.Config, inputs.Paths)
	if red != nil {
		red.addConfig(path)
	}
	cfg, err := config.LoadExisting(path, inputs)
	if err != nil {
		return red.err(fmt.Errorf("loading config: %w", err))
	}
	if red == nil {
		return CheckReport(w, path, cfg, o)
	}
	red.addChats(cfg.AllowedNumbers...)
	red.addGroups(cfg.AllowedGroups...)
	red.addGroupMembers(cfg.GroupMembers)
	var buf bytes.Buffer
	err = CheckReport(&buf, path, cfg, o)
	if _, werr := io.WriteString(w, red.text(buf.String())); werr != nil && err == nil {
		err = werr
	}
	return red.err(err)
}

// CheckReport writes what check says of cfg, the config loaded from path.
func CheckReport(w io.Writer, path string, cfg *config.Settings, o CheckOptions) error {
	// Before even the version probe: it runs outside the sandbox, so a
	// Claude planted in a brain's PATH folder must not be selected.
	SetBridgePATH(cfg)
	fmt.Fprintf(w, "Config OK: %s\n", path)
	if cfg.AgentProvider == "codex" {
		st := CodexInfo(cfg.CodexPath, cfg.ChildEnvPassthrough)
		fmt.Fprintf(w, "  codex:     %s (found: %t, %s), model %s, effort %s\n", st.Path, st.Found, st.Version, cfg.CodexModel, cfg.CodexEffort)
	} else {
		fmt.Fprintf(w, "  claude:    %s\n", claudeSummary(cfg.ClaudePath, cfg.ChildEnvPassthrough))
	}
	fmt.Fprintf(w, "  data dir:  %s\n", cfg.DataDir)
	if !cfg.HTTPEnabled || cfg.Dashboard.Disabled {
		fmt.Fprintln(w, "  dashboard: disabled")
	} else {
		fmt.Fprintf(w, "  dashboard: http://%s/\n", cfg.HTTPListen)
	}
	fmt.Fprintf(w, "  DM allow:  %s\n", dmAllowSummary(cfg))
	fmt.Fprintf(w, "  groups:    %v\n", cfg.AllowedGroups)
	for _, g := range cfg.AllowedGroups {
		fmt.Fprintf(w, "  heard in %s: %s\n", g, groupHeardSummary(cfg, g))
	}
	trig, _ := json.Marshal(cfg.Triggers)
	fmt.Fprintf(w, "  triggers:  %s\n", trig)
	// With the same PATH as startup, the brains and profiles match what
	// the bridge would run.
	specs, issues := ResolveBrains(cfg)
	if len(specs) == 0 && len(issues) > 0 {
		fmt.Fprintln(w, "  brains:    none available — see the issues below")
	} else if len(specs) == 0 {
		fmt.Fprintln(w, "  brains:    none yet — add one in the dashboard")
	}
	for _, s := range specs {
		m := s.Manifest
		fmt.Fprintf(w, "  brain %-14s → %s (dms=%v default=%v groups=%v)\n", m.ID, s.Dir, m.Routing.HandlesDMs, m.Routing.Default, m.Routing.Groups)
	}
	for _, is := range issues {
		fmt.Fprintf(w, "  ! %s %s: %s\n", is.Kind, is.Path, is.Detail)
	}

	if len(o.Routes) == 0 && o.SandboxFor == "" && o.TrySandbox == "" {
		return leftOutError(issues)
	}
	routes, workDirs, err := checkRoutes(specs, issues)
	if err != nil {
		return err
	}
	for _, jid := range o.Routes {
		isGroup := chat.IsGroupJID(jid)
		allowed := ""
		if isGroup && !slices.Contains(cfg.AllowedGroups, jid) {
			allowed = " (not in allowed_groups: ignored)"
		}
		if !isGroup && !NumberAllowed(cfg, chat.NormalizeJID(jid)) {
			allowed = " (not in allowed_numbers: ignored)"
		}
		if id := routes.For(jid, isGroup); id != "" {
			fmt.Fprintf(w, "  route %s → %s%s\n", jid, id, allowed)
		} else if id := routes.HeldFor(jid, isGroup); id != "" {
			fmt.Fprintf(w, "  route %s → no brain: %s handles it but cannot run%s\n", jid, id, allowed)
		} else {
			fmt.Fprintf(w, "  route %s → no brain%s\n", jid, allowed)
		}
	}
	if o.SandboxFor != "" {
		if _, ok := workDirs[o.SandboxFor]; !ok {
			return fmt.Errorf("sandbox profile: unknown or unavailable brain %q", o.SandboxFor)
		}
		profiler := NewSandboxProfiler(workDirs, cfg)
		profiler.SetConfigFile(path)
		profile, err := profiler.Profile(o.SandboxFor)
		if err != nil {
			return fmt.Errorf("sandbox profile: %w", err)
		}
		fmt.Fprintln(w)
		fmt.Fprint(w, profile)
	}
	if o.TrySandbox != "" {
		if _, ok := workDirs[o.TrySandbox]; !ok {
			return fmt.Errorf("sandbox: unknown or unavailable brain %q", o.TrySandbox)
		}
		profiler := NewSandboxProfiler(workDirs, cfg)
		profiler.SetConfigFile(path)
		if len(o.TryCommand) > 0 {
			return TrySandboxCommand(w, profiler, cfg, o.TrySandbox, o.TryCommand)
		}
		if err := TrySandbox(w, profiler, cfg, workDirs, o.TrySandbox); err != nil {
			return err
		}
	}
	return leftOutError(issues)
}

// leftOutError is ErrBrainsLeftOut for the brains issues leaves out, or nil.
func leftOutError(issues []registry.Issue) error {
	switch ids := leftOutBrains(issues); len(ids) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("brain %s %w; see the issue above", ids[0], ErrBrainsLeftOut)
	default:
		return fmt.Errorf("brains %s %w; see the issues above", strings.Join(ids, ", "), ErrBrainsLeftOut)
	}
}

// claudeSummary is the CLI brains run and its version, with a warning when
// it is missing or older than the release the bridge was tested with.
func claudeSummary(path string, passthrough []string) string {
	st := ClaudeInfo(path, passthrough)
	switch {
	case !st.Found:
		return path + " — not found, so brains cannot run"
	case st.Version == "":
		return st.Path + " (`--version` gave no answer)"
	}
	s := st.Path + " (" + st.Version + ")"
	if claudecli.VersionWarning(st) != "" {
		s += " — older than " + claudecli.TestedClaudeVersion + ", the oldest release tested; update it (claude update)"
	}
	return s
}

// dmAllowSummary says who may DM the bridge. A bare empty list would read
// as "nobody" even when allow_any_dm lets everyone in.
func dmAllowSummary(cfg *config.Settings) string {
	switch {
	case cfg.AnyoneCanDM():
		return "anyone (allow_any_dm is true and allowed_numbers is empty)"
	case len(cfg.AllowedNumbers) == 0:
		return "nobody (allowed_numbers is empty)"
	}
	return fmt.Sprint(cfg.AllowedNumbers)
}

// redactor masks what check --redact must not show: phone numbers down to
// the country code and the last two digits, group JIDs and LIDs down to
// their last two digits, email addresses as GoogleMaskEmail does, and the home
// folder, written as ~ in every path. Two numbers or groups still tell
// apart, and paths still say where under the home folder they are.
type redactor struct {
	// The configured chats, which are printed without a JID too, each
	// with its masked form; the longest first, since one may hold another.
	chats []redactedChat
	homes []*regexp.Regexp
}

type redactedChat struct{ value, masked string }

// newRedactor redacts home, as given and with its symlinks resolved, in
// each way a path is written: plain, in a regular expression, and quoted
// in a sandbox profile.
func newRedactor(home string) *redactor {
	r := &redactor{}
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

// addConfig adds the allowed numbers and groups and group_members of the
// config file at path as they are written, read before it loads: loading
// logs the entries it finds odd, a number with a note after it say, as
// they are. A file that does not parse adds nothing.
func (r *redactor) addConfig(path string) {
	var file struct {
		AllowedNumbers []string            `json:"allowed_numbers"`
		AllowedGroups  []string            `json:"allowed_groups"`
		GroupMembers   map[string][]string `json:"group_members"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &file) == nil {
		r.addChats(file.AllowedNumbers...)
		r.addGroups(file.AllowedGroups...)
		r.addGroupMembers(file.GroupMembers)
	}
}

// addGroupMembers adds the groups of group_members and the numbers each
// lists, which check prints as digits alone.
func (r *redactor) addGroupMembers(members map[string][]string) {
	for g, list := range members {
		r.addGroups(g)
		r.addChats(list...)
	}
}

// addChats adds phone numbers and chat JIDs to mask wherever they are
// printed without a JID too, as written and as digits alone.
func (r *redactor) addChats(chats ...string) { r.add(false, chats) }

// addGroups adds group JIDs as addChats does, also when one is written
// without @g.us.
func (r *redactor) addGroups(groups ...string) { r.add(true, groups) }

func (r *redactor) add(groups bool, chats []string) {
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

// text returns s redacted.
func (r *redactor) text(s string) string {
	s = logx.RedactIdentifiers(s, google.MaskEmail)
	for _, c := range r.chats {
		s = strings.ReplaceAll(s, c.value, c.masked)
	}
	for _, re := range r.homes {
		s = re.ReplaceAllString(s, "~$1")
	}
	return s
}

// err returns err redacted, or err itself without a redactor. The
// redacted error still wraps err, so that what check exits with is the
// same with --redact: errors.Is still finds ErrBrainsLeftOut in it.
func (r *redactor) err(err error) error {
	if r == nil || err == nil {
		return err
	}
	return redactedError{r.text(err.Error()), err}
}

// redactingWriter writes what it is given redacted. The log package writes
// each entry whole, so a number is never split between two writes.
type redactingWriter struct {
	w   io.Writer
	red *redactor
}

func (rw redactingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(rw.w, rw.red.text(string(p))); err != nil {
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
