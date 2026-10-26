// Package doctor runs `fridolin doctor`'s checks (docs/DECISIONS.md,
// 2026-10-20): each looks at one thing through an Env of injected
// lookups, so that tests never touch the system, and says ok, missing,
// warn or fail, with the fix. Only a required check that fails fails the
// command. It holds the checks every setup shares (the config, the data
// dir, the agent CLI, the host's tools and secrets, what waits for the
// owner) and the Redactor of --redact; internal/app makes the system's
// Env and puts the shared checks and each feature's own into a Registry.
package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/memindex"
)

// ErrFailed is what doctor ends with when a required check fails; the
// command then exits 1.
var ErrFailed = errors.New("a required check failed")

// The statuses a check ends with. Only a required check fails the
// command; an optional one that is not ready says missing or warn, with
// the fix, until the milestone that needs it makes it required.
const (
	OK      = "ok"
	Missing = "missing"
	Warn    = "warn"
	Fail    = "fail"
)

// Result is what one check found: its status, what it saw, and the
// command that fixes it when the status is not ok.
type Result struct {
	Status string
	Detail string
	Fix    string
}

// Check is one thing doctor looks at. Applies, when set, leaves the
// check out where it does not concern this setup (the Codex CLI in Claude
// mode). Needed names the milestone of docs/PLAN.md that will make an
// optional check required; that milestone flips Required in the same pull
// request as the feature, since every milestone's gate is a doctor check.
type Check struct {
	ID       string
	Title    string
	Required bool
	Needed   string
	Applies  func(env *Env) bool
	Run      func(ctx context.Context, env *Env) Result
}

// Env is what checks ask of the system, as functions so that tests fake
// every one of them: no check calls exec, os or syscall itself.
type Env struct {
	// ConfigGOOS is the platform configs are resolved for:
	// config.ContainerGOOS in the fridolin-core image.
	ConfigGOOS string
	GOOS       string
	Home       string
	// ConfigPath is the config file; Config is it loaded, or nil with
	// ConfigErr saying why it did not load.
	ConfigPath string
	Config     *config.Settings
	ConfigErr  error
	// LoadConfig loads the config file as the bridge would, and puts the
	// PATH in place that it would run the CLIs with; Write calls it before
	// the checks run. nil leaves Config and ConfigErr as they are.
	LoadConfig func(path string) (*config.Settings, error)

	Getenv   func(name string) string
	LookPath func(file string) (string, error)
	// Run runs a program and returns its standard output. A program that
	// ran and exited with a status other than 0 returns an error with an
	// ExitCode() int method, as *exec.ExitError has.
	Run       func(ctx context.Context, name string, args ...string) ([]byte, error)
	Stat      func(path string) (fs.FileInfo, error)
	ReadFile  func(path string) ([]byte, error)
	Glob      func(pattern string) ([]string, error)
	FreeBytes func(path string) (uint64, error)
	// WriteProbe creates a file in dir and removes it again.
	WriteProbe func(dir string) error
	// Dial opens a TCP connection to address and closes it again.
	Dial func(ctx context.Context, address string) error
	// Now is the time the checks judge ages by; nil is time.Now.
	Now func() time.Time
	// MemorySelfTest runs the memory index's provenance gate on a
	// scratch brain (memindex.SelfTest); MemoryStale lists what a brain's
	// index does not hold as it is on disk (memindex.Stale).
	MemorySelfTest func(ctx context.Context) error
	MemoryStale    func(ctx context.Context, b memindex.Brain) ([]string, error)
	// HasHostSecret asks the Windows Credential Manager whether it holds
	// a secret (macOS and Linux are asked through Run and Stat).
	HasHostSecret func(name string) (bool, error)
	// LiveNodes asks the running bridge for its nodes as it knows them
	// now (GET /api/nodes); an error when it does not answer, and the
	// node checks read nodes.json instead. nil never asks.
	LiveNodes func(ctx context.Context, cfg *config.Settings) ([]byte, error)
	// AndroidToolchain says why this machine cannot build the Plaud phone
	// app (no JDK 17+ or Android SDK), nil when it can.
	AndroidToolchain func() error
	// CheckConnection asks OpenAI or ElevenLabs (id) whether the key in
	// the data dir works, and answers what to say when it does.
	CheckConnection func(ctx context.Context, dataDir, id string) (string, error)
}

// Time is the time the checks judge ages by: Now's, else the system's.
func (env *Env) Time() time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

// existingDir returns dir, or the nearest folder above it that exists,
// and whether that is dir itself.
func (env *Env) existingDir(dir string) (string, bool) {
	for d := dir; ; d = filepath.Dir(d) {
		if st, err := env.Stat(d); err == nil && st.IsDir() {
			return d, d == dir
		}
		if d == filepath.Dir(d) {
			return d, false
		}
	}
}

// NoConfig is the result of a check that needs the config when it did
// not load.
var NoConfig = Result{Status: Warn, Detail: "not checked: the config does not load", Fix: "fix the config first (see the config line)"}

// OnHost leaves out a check of the host (its Docker, its Tailscale, the
// secrets in its keychain) when doctor runs inside the core's container,
// which has none of them: the container gets the secrets as variables.
func OnHost(env *Env) bool { return env.ConfigGOOS != config.ContainerGOOS }

// Registry is every check, in the order doctor prints them: the shared
// ones of this package and each feature's own, in the order they were
// added. Two checks may not share an ID, which the report and its JSON
// name them by.
type Registry struct {
	checks []Check
}

// Add appends checks. It panics on an ID the registry already holds: a
// mistake of the program, which every test of the registry finds.
func (r *Registry) Add(checks ...Check) {
	for _, c := range checks {
		if slices.ContainsFunc(r.checks, func(o Check) bool { return o.ID == c.ID }) {
			panic("doctor: two checks with the ID " + c.ID)
		}
		r.checks = append(r.checks, c)
	}
}

// Checks are the registry's checks, in order.
func (r *Registry) Checks() []Check { return slices.Clone(r.checks) }

// checkTimeout bounds each check, so a hung `docker info` or `tailscale
// status` cannot stall the command.
const checkTimeout = 15 * time.Second

// Report is what doctor found, as --json prints it.
type Report struct {
	Config string        `json:"config"`
	OK     bool          `json:"ok"`
	Checks []CheckReport `json:"checks"`
}

// CheckReport is one check's line.
type CheckReport struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Required bool   `json:"required"`
	Needed   string `json:"needed,omitempty"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
}

// Run runs each check that applies to env, in order.
func Run(ctx context.Context, env *Env, checks []Check) Report {
	rep := Report{Config: env.ConfigPath, OK: true, Checks: []CheckReport{}}
	for _, c := range checks {
		if c.Applies != nil && !c.Applies(env) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		res := c.Run(cctx, env)
		cancel()
		if res.Status == OK {
			res.Fix = ""
		}
		if res.Status == Fail && c.Required {
			rep.OK = false
		}
		rep.Checks = append(rep.Checks, CheckReport{
			ID: c.ID, Title: c.Title, Required: c.Required, Needed: c.Needed,
			Status: res.Status, Detail: res.Detail, Fix: res.Fix,
		})
	}
	return rep
}

// WriteText writes the report one line per check, the fix indented below
// a check that is not ok, and a count at the end.
func (r Report) WriteText(w io.Writer) error {
	var b strings.Builder
	counts := map[string]int{}
	for _, c := range r.Checks {
		counts[c.Status]++
		title := c.Title
		if !c.Required && c.Status != OK && c.Needed != "" {
			title += " (needed from " + c.Needed + ")"
		}
		fmt.Fprintf(&b, "%-8s %s: %s\n", c.Status, title, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "         fix: %s\n", c.Fix)
		}
	}
	fmt.Fprintf(&b, "\n%d ok, %d warn, %d missing, %d fail", counts[OK], counts[Warn], counts[Missing], counts[Fail])
	if r.OK {
		b.WriteString("; every required check passes\n")
	} else {
		b.WriteString("; a required check fails\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Options are how Write writes the report.
type Options struct {
	// JSON writes it as JSON, else as text.
	JSON bool
	// Redact masks the report and what is logged meanwhile as check
	// --redact does (Redactor).
	Redact bool
}

// Write loads the config into env (LoadConfig), runs checks on it and
// writes the report to w. It returns ErrFailed when a required check
// fails.
func Write(w io.Writer, o Options, env *Env, checks []Check) error {
	var red *Redactor
	if o.Redact {
		red = NewRedactor(env.Home)
		red.AddConfig(env.ConfigPath)
		prev := log.Writer()
		log.SetOutput(red.Writer(prev))
		defer log.SetOutput(prev)
	}
	if env.LoadConfig != nil {
		if cfg, err := env.LoadConfig(env.ConfigPath); err != nil {
			env.ConfigErr = err
		} else {
			env.Config = cfg
		}
	}
	if env.Config != nil && red != nil {
		red.AddChats(env.Config.AllowedNumbers...)
		red.AddGroups(env.Config.AllowedGroups...)
		red.AddGroupMembers(env.Config.GroupMembers)
	}
	rep := Run(context.Background(), env, checks)
	var buf bytes.Buffer
	if o.JSON {
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return err
		}
	} else if err := rep.WriteText(&buf); err != nil {
		return err
	}
	out := buf.String()
	if red != nil {
		out = red.Text(out)
	}
	if _, err := io.WriteString(w, out); err != nil {
		return err
	}
	if !rep.OK {
		return ErrFailed
	}
	return nil
}
