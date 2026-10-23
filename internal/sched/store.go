package sched

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// The status of a run (Run.Status).
const (
	StatusRunning = "running"
	// StatusOK is a run that ended well and whose result was handled as
	// its report setting says.
	StatusOK = "ok"
	// StatusNothing is a run that found nothing worth reporting.
	StatusNothing = "nothing"
	StatusFailed  = "failed"
	// StatusStopped is a run stopped by the owner or a restart; one that
	// hit its timeout is StatusFailed, with the error saying so.
	StatusStopped = "stopped"
	// StatusDropped is a run that never started: its brain was stopped or
	// removed, or the queue refused it.
	StatusDropped = "dropped"
	// StatusRefused is a run the policy did not start (a lockdown).
	StatusRefused = "refused"
	// StatusSkipped is a slot missed while the bridge was down that the
	// agent's missed setting left out.
	StatusSkipped = "skipped"
	// StatusInterrupted is a run the bridge was in the middle of when it
	// went down, found as it started again.
	StatusInterrupted = "interrupted"
)

// What started a run (Run.Trigger).
const (
	TriggerSchedule = "schedule"
	TriggerMissed   = "missed"
	TriggerManual   = "manual"
)

// The kinds of run (Run.Kind).
const (
	KindBrain   = "brain"
	KindCommand = "command"
)

// Run is one run of a scheduled agent, as its history keeps it.
type Run struct {
	ID      string `json:"id"`
	Brain   string `json:"brain"`
	Agent   string `json:"agent"`
	Kind    string `json:"kind"`
	Trigger string `json:"trigger"`
	// By is who asked for a manual run: dashboard or cli.
	By         string    `json:"by,omitempty"`
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended,omitzero"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Status     string    `json:"status"`
	// Result is the run's answer, or the end of a program's output,
	// shortened.
	Result string `json:"result,omitempty"`
	// Error says why it failed, without the CLI's stderr (that is in its
	// log).
	Error string `json:"error,omitempty"`
	// Sent says the result (or the failure) went to SentTo.
	Sent   bool   `json:"sent,omitempty"`
	SentTo string `json:"sent_to,omitempty"`
	// What the run cost, as the CLI reported it: Claude reports dollars
	// and tokens, Codex tokens.
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	// ExitCode is a program's exit code.
	ExitCode *int `json:"exit_code,omitempty"`
	// Node is the node a program ran on.
	Node string `json:"node,omitempty"`
	// LogBytes is the size of the run's log.
	LogBytes int64 `json:"log_bytes,omitempty"`
}

// AgentState is what the store keeps of an agent between runs.
type AgentState struct {
	// Last is the scheduled time of the last slot the agent took or
	// skipped, from which its next is counted (Decide).
	Last time.Time `json:"last,omitzero"`
	// Paused keeps it from running on its schedule until it is resumed.
	Paused   bool      `json:"paused,omitempty"`
	PausedAt time.Time `json:"paused_at,omitzero"`
	// LastRun is its latest run, a running one included.
	LastRun *Run `json:"last_run,omitempty"`
}

type stateFile struct {
	Version int                    `json:"version"`
	Agents  map[string]*AgentState `json:"agents"`
}

// Store keeps the agents' state, run history and run logs under one
// folder of the data dir (config.Layout.Schedules):
//
//	state.json                      every agent's AgentState, by brain/agent
//	<brain>/<agent>/runs.jsonl      its runs, oldest first, a line each
//	<brain>/<agent>/logs/<run>.log  each run's log
//
// Only the bridge writes it; brains never see it.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore keeps the agents' records in dir, which it creates when it
// first writes.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir is the store's folder.
func (s *Store) Dir() string { return s.dir }

var (
	slugRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	runIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{6}$`)
)

// ErrNotFound is a run or log the store does not have.
var ErrNotFound = errors.New("no such run")

// Key is an agent's key in the state: brain/agent.
func Key(brain, agent string) string { return brain + "/" + agent }

func checkNames(brain, agent string) error {
	if !slugRe.MatchString(brain) || !slugRe.MatchString(agent) {
		return fmt.Errorf("brain %q and agent %q must be slugs", brain, agent)
	}
	return nil
}

// NewRunID is a new run's id: when it started, sortable, and a random
// tail.
func NewRunID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// ValidRunID reports whether id is a run id NewRunID makes.
func ValidRunID(id string) bool { return runIDRe.MatchString(id) }

func (s *Store) readState() (stateFile, error) {
	st := stateFile{Version: 1, Agents: map[string]*AgentState{}}
	data, err := os.ReadFile(filepath.Join(s.dir, "state.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", filepath.Join(s.dir, "state.json"), err)
	}
	if st.Agents == nil {
		st.Agents = map[string]*AgentState{}
	}
	return st, nil
}

func (s *Store) writeState(st stateFile) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	st.Version = 1
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(filepath.Join(s.dir, "state.json"), append(data, '\n'), 0o600, false)
}

// States returns every agent's state, by Key.
func (s *Store) States() (map[string]AgentState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	out := make(map[string]AgentState, len(st.Agents))
	for k, a := range st.Agents {
		if a != nil {
			out[k] = *a
		}
	}
	return out, err
}

// Update lets fn change the state of agent brain/agent, and writes it
// when fn reports a change.
func (s *Store) Update(brain, agent string, fn func(*AgentState) bool) error {
	if err := checkNames(brain, agent); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	if err != nil {
		return err
	}
	key := Key(brain, agent)
	a := st.Agents[key]
	if a == nil {
		a = &AgentState{}
	}
	if !fn(a) {
		return nil
	}
	st.Agents[key] = a
	return s.writeState(st)
}

// Forget drops the state of the agents keep says no to, those removed
// from their brains: their history and logs stay until they age out.
func (s *Store) Forget(keep func(key string) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	if err != nil {
		return err
	}
	changed := false
	for k := range st.Agents {
		if !keep(k) {
			delete(st.Agents, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.writeState(st)
}

// Interrupted ends the runs the state says are running, which no bridge
// runs any more as one starts: each becomes StatusInterrupted, in the
// state and in its history. It returns them.
func (s *Store) Interrupted(now time.Time) ([]Run, error) {
	s.mu.Lock()
	st, err := s.readState()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	var out []Run
	for _, a := range st.Agents {
		if a != nil && a.LastRun != nil && a.LastRun.Status == StatusRunning {
			a.LastRun.Status, a.LastRun.Ended = StatusInterrupted, now
			a.LastRun.Error = "the bridge stopped while it ran"
			out = append(out, *a.LastRun)
		}
	}
	if len(out) > 0 {
		err = s.writeState(st)
	}
	s.mu.Unlock()
	for _, r := range out {
		if aerr := s.appendHistory(r); aerr != nil && err == nil {
			err = aerr
		}
	}
	return out, err
}

func (s *Store) agentDir(brain, agent string) string { return filepath.Join(s.dir, brain, agent) }

// Begin records a run that starts: the agent's state names it as its
// last run.
func (s *Store) Begin(r Run) error {
	r.Status = StatusRunning
	return s.Update(r.Brain, r.Agent, func(a *AgentState) bool {
		a.LastRun = &r
		return true
	})
}

// Finish records a run that ended: in its history, and as its agent's
// last run.
func (s *Store) Finish(r Run) error {
	if err := checkNames(r.Brain, r.Agent); err != nil {
		return err
	}
	if fi, err := os.Stat(s.logPath(r.Brain, r.Agent, r.ID)); err == nil {
		r.LogBytes = fi.Size()
	}
	if err := s.appendHistory(r); err != nil {
		return err
	}
	return s.Update(r.Brain, r.Agent, func(a *AgentState) bool {
		if a.LastRun == nil || a.LastRun.ID == r.ID || !a.LastRun.Started.After(r.Started) {
			a.LastRun = &r
		}
		return true
	})
}

func (s *Store) appendHistory(r Run) error {
	dir := s.agentDir(r.Brain, r.Agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(dir, "runs.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(data, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// maxHistoryRead bounds what Runs reads of a history: keep_runs keeps it
// far smaller.
const maxHistoryRead = 32 << 20

// Runs returns agent brain/agent's runs, newest first, at most limit (0
// for all). A line that does not read is left out.
func (s *Store) Runs(brain, agent string, limit int) ([]Run, error) {
	if err := checkNames(brain, agent); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readRuns(brain, agent, limit)
}

func (s *Store) readRuns(brain, agent string, limit int) ([]Run, error) {
	f, err := os.Open(filepath.Join(s.agentDir(brain, agent), "runs.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxHistoryRead))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	out := []Run{}
	for sc.Scan() {
		var r Run
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.ID != "" {
			out = append(out, r)
		}
	}
	slices.Reverse(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, sc.Err()
}

// Run returns run id of agent brain/agent.
func (s *Store) Run(brain, agent, id string) (Run, error) {
	runs, err := s.Runs(brain, agent, 0)
	if err != nil {
		return Run{}, err
	}
	for _, r := range runs {
		if r.ID == id {
			return r, nil
		}
	}
	return Run{}, ErrNotFound
}

func (s *Store) logPath(brain, agent, id string) string {
	return filepath.Join(s.agentDir(brain, agent), "logs", id+".log")
}

// OpenLog creates run id's log, which keeps at most max bytes.
func (s *Store) OpenLog(brain, agent, id string, max int64) (*Log, error) {
	if err := checkNames(brain, agent); err != nil {
		return nil, err
	}
	if !ValidRunID(id) {
		return nil, fmt.Errorf("run id %q", id)
	}
	dir := filepath.Join(s.agentDir(brain, agent), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Log{f: f, max: max}, nil
}

// ReadLog returns run id's log, its last max bytes when it is longer
// (with cut true).
func (s *Store) ReadLog(brain, agent, id string, max int64) (text string, cut bool, err error) {
	if err := checkNames(brain, agent); err != nil {
		return "", false, err
	}
	if !ValidRunID(id) {
		return "", false, ErrNotFound
	}
	f, err := os.Open(s.logPath(brain, agent, id))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if fi.Size() > max {
		if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
			return "", false, err
		}
		cut = true
	}
	data, err := io.ReadAll(io.LimitReader(f, max))
	return string(data), cut, err
}

// Log is one run's log: lines, each with the time, cut once it reaches
// its size.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	max     int64
	written int64
	cut     bool
}

// Line appends one line (several, when it holds newlines).
func (l *Log) Line(text string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil || l.cut {
		return
	}
	line := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), strings.TrimRight(text, "\n"))
	if l.written+int64(len(line)) > l.max {
		l.cut = true
		line = fmt.Sprintf("[%s] … the log stops here: it reached its limit of %d KiB (schedules.max_log_kb)\n", time.Now().Format("15:04:05"), l.max>>10)
	}
	n, _ := l.f.WriteString(line)
	l.written += int64(n)
}

// Close closes the log; later lines are dropped.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// RunFile is one run on disk, for the housekeeping rule that keeps the
// history within its limits.
type RunFile struct {
	Brain, Agent, ID string
	Started          time.Time
	Bytes            int64
}

// Listing lists every agent's runs, from the histories in the store,
// and the logs no history names (a run in progress, or one whose line
// was lost), by agent.
func (s *Store) Listing() (map[string][]RunFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]RunFile{}
	brains, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, b := range brains {
		if !b.IsDir() || !slugRe.MatchString(b.Name()) {
			continue
		}
		agents, err := os.ReadDir(filepath.Join(s.dir, b.Name()))
		if err != nil {
			return nil, err
		}
		for _, a := range agents {
			if !a.IsDir() || !slugRe.MatchString(a.Name()) {
				continue
			}
			runs, err := s.readRuns(b.Name(), a.Name(), 0)
			if err != nil {
				return nil, err
			}
			seen := map[string]bool{}
			var files []RunFile
			for _, r := range runs {
				seen[r.ID] = true
				files = append(files, RunFile{Brain: b.Name(), Agent: a.Name(), ID: r.ID, Started: r.Started, Bytes: r.LogBytes})
			}
			logs, _ := os.ReadDir(filepath.Join(s.agentDir(b.Name(), a.Name()), "logs"))
			for _, l := range logs {
				id, ok := strings.CutSuffix(l.Name(), ".log")
				if !ok || seen[id] || !ValidRunID(id) {
					continue
				}
				started, _ := time.Parse("20060102T150405", id[:15])
				fi, _ := l.Info()
				var size int64
				if fi != nil {
					size = fi.Size()
				}
				files = append(files, RunFile{Brain: b.Name(), Agent: a.Name(), ID: id, Started: started, Bytes: size})
			}
			out[Key(b.Name(), a.Name())] = files
		}
	}
	return out, nil
}

// Drop removes runs from their histories and their logs. A running run
// (in the state) is never among them: the caller's rule keeps the newest.
func (s *Store) Drop(runs []RunFile) error {
	byAgent := map[string]map[string]bool{}
	for _, r := range runs {
		if checkNames(r.Brain, r.Agent) != nil || !ValidRunID(r.ID) {
			continue
		}
		k := Key(r.Brain, r.Agent)
		if byAgent[k] == nil {
			byAgent[k] = map[string]bool{}
		}
		byAgent[k][r.ID] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for k, ids := range byAgent {
		brain, agent, _ := strings.Cut(k, "/")
		for id := range ids {
			if err := os.Remove(s.logPath(brain, agent, id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		path := filepath.Join(s.agentDir(brain, agent), "runs.jsonl")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var keep bytes.Buffer
		for line := range bytes.Lines(data) {
			var r struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(line, &r) == nil && ids[r.ID] {
				continue
			}
			keep.Write(line)
		}
		if err := fsx.WriteFileAtomic(path, keep.Bytes(), 0o600, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
