// Package snapshot keeps point-in-time copies of the bridge's data: it runs
// restic for snapshots of folders and supervises litestream, which
// replicates SQLite databases continuously.
//
// Both binaries are configured only through what the caller passes: the
// binary's path, the repository or replica, and the child's whole
// environment. Nothing of this process's environment reaches a child, so
// a stray RESTIC_REPOSITORY or AWS profile can neither redirect nor leak
// into a run, and secrets are blanked out of every error.
package snapshot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Restic runs restic against one repository.
type Restic struct {
	Binary string // path of the restic executable
	// Repo is restic's -r value: a local folder, "b2:bucket:path" or
	// "rclone:remote:path".
	Repo string
	// Env is the child's whole environment: RESTIC_PASSWORD, the
	// backend's variables (B2_ACCOUNT_ID and B2_ACCOUNT_KEY, or
	// RCLONE_CONFIG_*), PATH, HOME and whatever else restic needs.
	Env []string
	// Options are extra -o options, such as "rclone.program=/path/to/rclone".
	Options []string
	// CacheDir is restic's --cache-dir; "" runs with --no-cache.
	CacheDir string
	// Secrets are extra strings to blank out of errors, on top of the
	// values of Env's secret variables (see secretNameParts).
	Secrets []string
	// WaitDelay bounds how long a cancelled restic may take to exit after
	// it is told to before it is killed (default 10s).
	WaitDelay time.Duration
}

// The errors restic's exit codes stand for. A *ResticError matches the one
// its exit code maps to with errors.Is.
var (
	ErrRepoMissing   = errors.New("snapshot: no restic repository there")      // exit 10
	ErrWrongPassword = errors.New("snapshot: wrong restic password")           // exit 12
	ErrLocked        = errors.New("snapshot: the restic repository is locked") // exit 11
	// ErrIncomplete is a backup that made its snapshot but could not read
	// some of the files (exit 3): the snapshot exists and lacks them.
	ErrIncomplete = errors.New("snapshot: some files could not be read")
)

// restic's exit codes (restic's documentation, "Exit status codes").
const (
	resticExitIncomplete    = 3
	resticExitRepoMissing   = 10
	resticExitLocked        = 11
	resticExitWrongPassword = 12
)

// ResticError is a failed restic run.
type ResticError struct {
	Op       string // the restic command, such as "backup"
	ExitCode int    // -1 when restic did not exit by itself
	Stderr   string // the tail of restic's error output, secrets blanked
	Err      error
}

func (e *ResticError) Error() string {
	msg := fmt.Sprintf("snapshot: restic %s: ", e.Op)
	if s := e.sentinel(); s != nil {
		msg += strings.TrimPrefix(s.Error(), "snapshot: ") + ": "
	}
	msg += e.Err.Error()
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// Unwrap returns the sentinel error the exit code stands for, if any, and
// the error of the run, so that errors.Is finds both.
func (e *ResticError) Unwrap() []error {
	if s := e.sentinel(); s != nil {
		return []error{s, e.Err}
	}
	return []error{e.Err}
}

func (e *ResticError) sentinel() error {
	switch e.ExitCode {
	case resticExitRepoMissing:
		return ErrRepoMissing
	case resticExitLocked:
		return ErrLocked
	case resticExitWrongPassword:
		return ErrWrongPassword
	case resticExitIncomplete:
		// Other commands do not use 3; only a backup is incomplete.
		if e.Op == "backup" {
			return ErrIncomplete
		}
	}
	return nil
}

// run is one restic invocation: the command, its arguments after the
// global flags, and the environment and options it runs with.
type run struct {
	op      string
	args    []string
	env     []string
	options []string
	secrets []string
}

// newRun prepares op in r's repository with r's environment.
func (r *Restic) newRun(op string, args ...string) *run {
	return &run{op: op, args: args, env: r.Env, options: r.Options, secrets: r.secrets()}
}

func (r *Restic) secrets() []string {
	return secretValues(r.Env, append(slices.Clone(r.Secrets), urlPassword(r.Repo))...)
}

// command builds the restic process for ru.
func (r *Restic) command(ctx context.Context, ru *run) (*exec.Cmd, error) {
	if r.Binary == "" {
		return nil, errors.New("snapshot: no restic binary configured")
	}
	if r.Repo == "" {
		return nil, errors.New("snapshot: no restic repository configured")
	}
	full := []string{"-r", r.Repo}
	if r.CacheDir == "" {
		full = append(full, "--no-cache")
	} else {
		full = append(full, "--cache-dir", r.CacheDir)
	}
	for _, o := range ru.options {
		full = append(full, "-o", o)
	}
	full = append(full, ru.op)
	full = append(full, ru.args...)
	cmd := exec.CommandContext(ctx, r.Binary, full...)
	cmd.Env = append([]string{}, ru.env...)
	// restic stops cleanly on an interrupt: it removes its lock and, in
	// a backup, keeps what it uploaded for the next run.
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	cmd.WaitDelay = r.WaitDelay
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = 10 * time.Second
	}
	return cmd, nil
}

func runError(ctx context.Context, ru *run, err error, stderr string) *ResticError {
	e := &ResticError{Op: ru.op, ExitCode: -1, Stderr: redact(stderr, ru.secrets), Err: err}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		e.ExitCode = exit.ExitCode()
	}
	if ctx.Err() != nil {
		e.Err = errors.Join(ctx.Err(), err)
	}
	return e
}

// exec runs ru to completion. Each line restic prints goes to onLine when
// it is set; otherwise the output is returned.
func (r *Restic) exec(ctx context.Context, ru *run, onLine func([]byte)) ([]byte, error) {
	cmd, err := r.command(ctx, ru)
	if err != nil {
		return nil, err
	}
	stderr := &tailBuffer{limit: 4 * stderrLimit}
	cmd.Stderr = stderr
	var stdout bytes.Buffer
	if onLine == nil {
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			return nil, runError(ctx, ru, err, stderr.String())
		}
		return stdout.Bytes(), nil
	}
	// A backup prints a status line every few seconds for as long as it
	// runs; reading them as they come keeps memory flat.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, runError(ctx, ru, err, "")
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		onLine(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		// Drain so restic is not stuck writing to a pipe nobody reads.
		_, _ = io.Copy(io.Discard, pipe)
	}
	if err := cmd.Wait(); err != nil {
		return nil, runError(ctx, ru, err, stderr.String())
	}
	return nil, nil
}

// Exists reports whether there is a repository at r.Repo that r's password
// opens. No repository is (false, nil); a wrong password is an error
// matching ErrWrongPassword.
func (r *Restic) Exists(ctx context.Context) (bool, error) {
	_, err := r.exec(ctx, r.newRun("cat", "config"), nil)
	if errors.Is(err, ErrRepoMissing) {
		return false, nil
	}
	return err == nil, err
}

// Init creates the repository. With chunkerFrom, the new repository takes
// that repository's chunker parameters, so that Copy between the two
// deduplicates instead of storing every file again; chunkerFrom's
// password and backend variables are merged into the run as for Copy.
func (r *Restic) Init(ctx context.Context, chunkerFrom *Restic) error {
	if chunkerFrom == nil {
		_, err := r.exec(ctx, r.newRun("init"), nil)
		return err
	}
	ru, err := r.fromRun(chunkerFrom, "init", "--from-repo", chunkerFrom.Repo, "--copy-chunker-params")
	if err != nil {
		return err
	}
	_, err = r.exec(ctx, ru, nil)
	return err
}

// fromRun prepares op in r's repository that also reads from's.
func (r *Restic) fromRun(from *Restic, op string, args ...string) (*run, error) {
	if from.Repo == "" {
		return nil, errors.New("snapshot: no source restic repository configured")
	}
	env, err := mergeEnv(r.Env, from.Env)
	if err != nil {
		return nil, err
	}
	options, err := mergeOptions(r.Options, from.Options)
	if err != nil {
		return nil, err
	}
	ru := r.newRun(op, args...)
	ru.env = env
	ru.options = options
	ru.secrets = secretValues(nil, append(r.secrets(), from.secrets()...)...)
	return ru, nil
}

// BackupOptions says what a backup saves and how its snapshot is labelled.
type BackupOptions struct {
	Paths    []string // what to save; at least one
	Tags     []string // the snapshot's tags
	Excludes []string // --exclude patterns
	// Host is the snapshot's host name; "" lets restic use the machine's.
	Host string
	// ExcludeCaches skips folders with a CACHEDIR.TAG file.
	ExcludeCaches bool
}

// BackupSummary is what restic reports at the end of a backup.
type BackupSummary struct {
	SnapshotID          string `json:"snapshot_id"`
	FilesNew            int    `json:"files_new"`
	FilesChanged        int    `json:"files_changed"`
	FilesUnmodified     int    `json:"files_unmodified"`
	TotalFilesProcessed int    `json:"total_files_processed"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	DataAdded           int64  `json:"data_added"`
}

// Backup saves o.Paths as a new snapshot. When restic could not read some
// files it still makes the snapshot: Backup then returns its summary and
// an error matching ErrIncomplete.
func (r *Restic) Backup(ctx context.Context, o BackupOptions) (BackupSummary, error) {
	if len(o.Paths) == 0 {
		return BackupSummary{}, errors.New("snapshot: a backup needs at least one path")
	}
	args := []string{"--json"}
	if o.Host != "" {
		args = append(args, "--host", o.Host)
	}
	for _, t := range o.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range o.Excludes {
		args = append(args, "--exclude", e)
	}
	if o.ExcludeCaches {
		args = append(args, "--exclude-caches")
	}
	// "--" so that a path starting with a dash is not taken for a flag.
	args = append(args, "--")
	args = append(args, o.Paths...)
	var summary BackupSummary
	var found bool
	_, err := r.exec(ctx, r.newRun("backup", args...), func(line []byte) {
		var msg struct {
			MessageType string `json:"message_type"`
		}
		if json.Unmarshal(line, &msg) != nil || msg.MessageType != "summary" {
			return
		}
		if json.Unmarshal(line, &summary) == nil {
			found = true
		}
	})
	if err == nil && !found {
		return summary, errors.New("snapshot: restic backup ended without a summary")
	}
	return summary, err
}

// Snapshot is one snapshot as `restic snapshots --json` lists it.
type Snapshot struct {
	ID       string           `json:"id"`
	ShortID  string           `json:"short_id"`
	Time     time.Time        `json:"time"`
	Paths    []string         `json:"paths"`
	Tags     []string         `json:"tags"`
	Hostname string           `json:"hostname"`
	Summary  *SnapshotSummary `json:"summary,omitempty"`
}

// SnapshotSummary is the part of a backup's summary that restic keeps in
// the snapshot (restic 0.17 and later; nil for older snapshots).
type SnapshotSummary struct {
	TotalFilesProcessed int   `json:"total_files_processed"`
	TotalBytesProcessed int64 `json:"total_bytes_processed"`
}

// tagList is restic's --tag value that matches snapshots with all of tags.
func tagList(tags []string) (string, error) {
	for _, t := range tags {
		if t == "" || strings.Contains(t, ",") {
			return "", fmt.Errorf("snapshot: tag %q is empty or has a comma", t)
		}
	}
	return strings.Join(tags, ","), nil
}

// Snapshots lists the snapshots that have all of tags (all of them for
// none), the oldest first.
func (r *Restic) Snapshots(ctx context.Context, tags []string) ([]Snapshot, error) {
	args := []string{"--json"}
	if len(tags) > 0 {
		list, err := tagList(tags)
		if err != nil {
			return nil, err
		}
		args = append(args, "--tag", list)
	}
	out, err := r.exec(ctx, r.newRun("snapshots", args...), nil)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("snapshot: reading restic's snapshot list: %w", err)
	}
	slices.SortStableFunc(snaps, func(a, b Snapshot) int { return a.Time.Compare(b.Time) })
	return snaps, nil
}

// Retention is how many snapshots Forget keeps: the latest of each of the
// last Hourly hours, Daily days and so on. Zero keeps none for that span.
type Retention struct {
	Hourly, Daily, Weekly, Monthly int
}

// Forget removes the snapshots that keep does not keep, among those with
// all of tags (all snapshots for none), judging each host and set of tags
// on its own. With prune it also deletes the data no snapshot uses any
// more. A Retention that keeps nothing is refused rather than passed on.
func (r *Restic) Forget(ctx context.Context, keep Retention, tags []string, prune bool) error {
	if keep.Hourly < 0 || keep.Daily < 0 || keep.Weekly < 0 || keep.Monthly < 0 {
		return errors.New("snapshot: a retention count is negative")
	}
	if keep == (Retention{}) {
		return errors.New("snapshot: a retention that keeps no snapshot")
	}
	args := []string{"--group-by", "host,tags"}
	for _, k := range []struct {
		flag string
		n    int
	}{{"--keep-hourly", keep.Hourly}, {"--keep-daily", keep.Daily}, {"--keep-weekly", keep.Weekly}, {"--keep-monthly", keep.Monthly}} {
		if k.n > 0 {
			args = append(args, k.flag, strconv.Itoa(k.n))
		}
	}
	if len(tags) > 0 {
		list, err := tagList(tags)
		if err != nil {
			return err
		}
		args = append(args, "--tag", list)
	}
	if prune {
		args = append(args, "--prune")
	}
	_, err := r.exec(ctx, r.newRun("forget", args...), nil)
	return err
}

// Check verifies the repository's structure and, with readDataSubset
// (such as "5%" or "1/10"), reads that part of the stored data back too.
func (r *Restic) Check(ctx context.Context, readDataSubset string) error {
	var args []string
	if readDataSubset != "" {
		args = append(args, "--read-data-subset="+readDataSubset)
	}
	_, err := r.exec(ctx, r.newRun("check", args...), nil)
	return err
}

// Restore writes snapshotID ("latest" works too) into target. target must
// be an existing, empty folder: restoring over files would mix two states
// and a typo could overwrite live data.
func (r *Restic) Restore(ctx context.Context, snapshotID, target string) error {
	if snapshotID == "" || strings.HasPrefix(snapshotID, "-") {
		return fmt.Errorf("snapshot: invalid snapshot id %q", snapshotID)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("snapshot: restore target: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("snapshot: restore target %s is not empty", target)
	}
	_, err = r.exec(ctx, r.newRun("restore", snapshotID, "--target", target), nil)
	return err
}

// Copy copies from's snapshots that r's repository does not have yet into
// it. The run reads both repositories, so it gets r's environment merged
// with from's (see mergeEnv): from's password as RESTIC_FROM_PASSWORD, and
// from's backend variables, which must not clash with r's.
func (r *Restic) Copy(ctx context.Context, from *Restic) error {
	ru, err := r.fromRun(from, "copy", "--from-repo", from.Repo)
	if err != nil {
		return err
	}
	_, err = r.exec(ctx, ru, nil)
	return err
}

// Unlock removes stale locks, those of restic runs that ended without
// removing theirs. It leaves the locks of runs that are still going.
func (r *Restic) Unlock(ctx context.Context) error {
	_, err := r.exec(ctx, r.newRun("unlock"), nil)
	return err
}
