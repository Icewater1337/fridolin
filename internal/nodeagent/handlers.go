package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// effective is what the node may serve now: what it declares itself, as
// far as the core approved it. Both sides check; this side is the one
// that guards the machine.
func (a *Agent) effective() (nodeproto.Capabilities, *nodeproto.Error) {
	st := a.State()
	if st.Device != nodeproto.DeviceApproved {
		return nodeproto.Capabilities{}, nodeproto.Errorf(nodeproto.CodeDenied, "this device is not approved (%s)", st.Device)
	}
	return nodeproto.Intersect(a.declared(), st.Approved), nil
}

// handle serves one request.
func (a *Agent) handle(ctx context.Context, req nodeproto.Request) nodeproto.Response {
	result, perr := a.call(ctx, req)
	if perr != nil {
		a.logf("node: refused or failed %s: %v", req.Method, perr)
		return nodeproto.Response{Error: perr}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nodeproto.Response{Error: nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)}
	}
	return nodeproto.Response{Result: raw}
}

func (a *Agent) call(ctx context.Context, req nodeproto.Request) (any, *nodeproto.Error) {
	if !slices.Contains(nodeproto.Methods, req.Method) {
		return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", req.Method)
	}
	eff, perr := a.effective()
	if perr != nil {
		return nil, perr
	}
	if !eff.Allows(req.Method) {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%s is not approved on this node", req.Method)
	}
	if req.Method == nodeproto.MethodExecPrograms {
		return a.execProgramsCall(req.Params)
	}
	if slices.Contains(nodeproto.FrideMethods, req.Method) {
		return a.frideCall(ctx, req.Method, req.Params)
	}
	if slices.Contains(nodeproto.AndroidMethods, req.Method) {
		return a.androidCall(ctx, req.Method, req.Params)
	}
	if slices.Contains(nodeproto.BackupMethods, req.Method) {
		return a.backupCall(eff, req.Method, req.Params)
	}
	if slices.Contains(nodeproto.UpdateMethods, req.Method) {
		return a.updateCall(ctx, req.Method, req.Params)
	}
	if slices.Contains(nodeproto.CoreHostMethods, req.Method) {
		return a.coreHostCall(ctx, req.Method, req.Params)
	}
	if slices.Contains(nodeproto.AgentMethods, req.Method) || slices.Contains(nodeproto.TerminalMethods, req.Method) {
		return a.workstationCall(ctx, eff, req.Method, req.Params)
	}
	decode := func(v any) *nodeproto.Error {
		if len(req.Params) == 0 {
			return nil
		}
		if err := json.Unmarshal(req.Params, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", req.Method, err)
		}
		return nil
	}
	switch req.Method {
	case nodeproto.MethodExec:
		var p nodeproto.ExecParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.exec(ctx, eff, p)
	case nodeproto.MethodFSList:
		var p nodeproto.PathParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.list(eff, p.Path)
	case nodeproto.MethodFSRead:
		var p nodeproto.FSReadParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.read(eff, p)
	case nodeproto.MethodFSWrite:
		var p nodeproto.FSWriteParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.write(eff, p)
	case nodeproto.MethodHostInfo:
		return a.hostInfo(), nil
	case nodeproto.MethodNotify:
		var p nodeproto.NotifyParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.notify(ctx, p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", req.Method)
}

// cappedBuffer keeps the first max bytes written to it.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room < len(p) {
		b.truncated = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (a *Agent) exec(ctx context.Context, eff nodeproto.Capabilities, p nodeproto.ExecParams) (any, *nodeproto.Error) {
	if err := eff.PermitsExec(p.Argv); err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeDenied, "%v", err)
	}
	if p.Dir != "" && !filepath.IsAbs(p.Dir) {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "dir %q is not absolute", p.Dir)
	}
	// A program only the core added is checked again: it may have moved
	// or become a symlink since.
	real, perr := a.corePath(eff, p.Argv)
	if perr != nil {
		return nil, perr
	}
	timeout := time.Duration(eff.Exec.Timeout()) * time.Second
	if p.TimeoutSeconds > 0 && time.Duration(p.TimeoutSeconds)*time.Second < timeout {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Running what the core asks is this capability's purpose: PermitsExec
	// above holds argv to the allowlist the owner approved (exact program
	// names, per-argument patterns, never a shell). exec.Command looks a
	// bare name up in the PATH, and refuses one found relative to the
	// working directory.
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	if real != "" {
		// The file just checked, not what the path may point to by now.
		cmd.Path = real
	}
	cmd.Dir = p.Dir
	stdout := &cappedBuffer{max: eff.Exec.OutputCap()}
	stderr := &cappedBuffer{max: eff.Exec.OutputCap()}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	platform.IsolateProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Run()
	res := nodeproto.ExecResult{
		Stdout: stdout.buf.String(), Stderr: stderr.buf.String(),
		Truncated:  stdout.truncated || stderr.truncated,
		TimedOut:   errors.Is(ctx.Err(), context.DeadlineExceeded),
		DurationMS: time.Since(start).Milliseconds(),
	}
	a.logf("node: ran %s (%d arguments) in %v", p.Argv[0], len(p.Argv)-1, time.Since(start).Round(time.Millisecond))
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		if res.TimedOut {
			res.ExitCode = -1
			return res, nil
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "%s is not installed on this node", p.Argv[0])
		}
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "running %s: %v", p.Argv[0], err)
	}
	return res, nil
}

// resolve finds the granted root that holds path and opens it: path must
// be absolute and lie under the root as written or as its symlinks
// resolve. The *os.Root then refuses every way out of it, .. and
// symlinks included.
func resolve(eff nodeproto.Capabilities, path string, write bool) (*os.Root, string, *nodeproto.Error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, "", nodeproto.Errorf(nodeproto.CodeBadRequest, "path %q is not absolute", path)
	}
	path = filepath.Clean(path)
	var best *nodeproto.FSRoot
	var rel string
	for i := range eff.FS.Roots {
		r := &eff.FS.Roots[i]
		for _, base := range rootForms(r.Path) {
			if x, ok := within(base, path); ok && (best == nil || len(r.Path) > len(best.Path)) {
				best, rel = r, x
			}
		}
	}
	if best == nil {
		return nil, "", nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s is not under a granted root", path)
	}
	if write && !best.Write {
		return nil, "", nodeproto.Errorf(nodeproto.CodeDenied, "%s is read-only", best.Path)
	}
	root, err := os.OpenRoot(best.Path)
	if err != nil {
		return nil, "", fsError(err, best.Path)
	}
	return root, rel, nil
}

// rootForms are a root as written and as its symlinks resolve (/var and
// /private/var on macOS).
func rootForms(p string) []string {
	p = filepath.Clean(p)
	out := []string{p}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
		out = append(out, r)
	}
	return out
}

// within returns path relative to base when it lies at or below it.
func within(base, path string) (string, bool) {
	rel, err := filepath.Rel(base, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func fsError(err error, path string) *nodeproto.Error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nodeproto.Errorf(nodeproto.CodeNotFound, "%s does not exist", path)
	case strings.Contains(err.Error(), "escapes"):
		return nodeproto.Errorf(nodeproto.CodeOutsideRoot, "%s leads out of its root", path)
	case errors.Is(err, fs.ErrPermission):
		return nodeproto.Errorf(nodeproto.CodeDenied, "%s: permission denied", path)
	}
	return nodeproto.Errorf(nodeproto.CodeFailed, "%s: %v", path, err)
}

// maxEntries bounds a listing.
const maxEntries = 10000

func (a *Agent) list(eff nodeproto.Capabilities, path string) (any, *nodeproto.Error) {
	root, rel, perr := resolve(eff, path, false)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fsError(err, path)
	}
	defer f.Close()
	ents, err := f.ReadDir(maxEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fsError(err, path)
	}
	out := nodeproto.FSListResult{Path: filepath.Clean(path), Entries: []nodeproto.FSEntry{}}
	for _, e := range ents {
		fe := nodeproto.FSEntry{Name: e.Name(), Dir: e.IsDir(), Link: e.Type()&fs.ModeSymlink != 0}
		if info, err := e.Info(); err == nil {
			fe.Size, fe.ModTime = info.Size(), info.ModTime().UTC()
		}
		out.Entries = append(out.Entries, fe)
	}
	slices.SortFunc(out.Entries, func(x, y nodeproto.FSEntry) int { return strings.Compare(x.Name, y.Name) })
	return out, nil
}

func (a *Agent) read(eff nodeproto.Capabilities, p nodeproto.FSReadParams) (any, *nodeproto.Error) {
	root, rel, perr := resolve(eff, p.Path, false)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, fsError(err, p.Path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fsError(err, p.Path)
	}
	if !st.Mode().IsRegular() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is not a file", p.Path)
	}
	limit := int64(nodeproto.MaxFileBytes)
	if p.MaxBytes > 0 && p.MaxBytes < limit {
		limit = p.MaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fsError(err, p.Path)
	}
	out := nodeproto.FSReadResult{Path: filepath.Clean(p.Path), Size: st.Size(), Data: data}
	if int64(len(data)) > limit {
		out.Data, out.Truncated = data[:limit], true
	}
	return out, nil
}

func (a *Agent) write(eff nodeproto.Capabilities, p nodeproto.FSWriteParams) (any, *nodeproto.Error) {
	if len(p.Data) > nodeproto.MaxFileBytes {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "at most %d bytes", nodeproto.MaxFileBytes)
	}
	root, rel, perr := resolve(eff, p.Path, true)
	if perr != nil {
		return nil, perr
	}
	defer root.Close()
	if rel == "." {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is the root itself", p.Path)
	}
	dir := filepath.Dir(rel)
	if p.Mkdirs && dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return nil, fsError(err, p.Path)
		}
	}
	if st, err := root.Lstat(rel); err == nil && st.IsDir() {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "%s is a folder", p.Path)
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.fridolin-tmp", filepath.Base(rel), time.Now().UnixNano()))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fsError(err, p.Path)
	}
	_, werr := f.Write(p.Data)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, rel)
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return nil, fsError(werr, p.Path)
	}
	a.logf("node: wrote %d bytes to %s", len(p.Data), p.Path)
	return nodeproto.FSWriteResult{Path: filepath.Clean(p.Path), Size: int64(len(p.Data))}, nil
}

// stats are the host's free resources.
func (a *Agent) stats() nodeproto.HostStats {
	s := nodeproto.HostStats{CPUs: runtime.NumCPU()}
	if total, avail, err := platform.Memory(); err == nil {
		s.MemTotal, s.MemAvailable = total, avail
	}
	if free, err := platform.FreeBytes(a.Dir); err == nil {
		s.DiskFree = free
	}
	if pct, ok := platform.Battery(); ok {
		s.Battery = &pct
	}
	return s
}

func (a *Agent) hostInfo() nodeproto.HostInfo {
	name, _ := os.Hostname()
	return nodeproto.HostInfo{Hostname: name, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: a.Version, HostStats: a.stats()}
}

// clean keeps a notification's text printable and short.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}

func (a *Agent) notify(ctx context.Context, p nodeproto.NotifyParams) (any, *nodeproto.Error) {
	title, msg := clean(p.Title, 128), clean(p.Message, 1024)
	if title == "" {
		title = "Fridolin"
	}
	notifier := a.Notifier
	if notifier == nil {
		notifier = systemNotify(a.logf)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := notifier(ctx, title, msg)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "showing the notification: %v", err)
	}
	return res, nil
}

// systemNotify shows a notification the OS's way: osascript on macOS,
// notify-send on Linux, a toast through Windows PowerShell on Windows
// (notify_windows.go).
func systemNotify(logf func(string, ...any)) func(context.Context, string, string) (nodeproto.NotifyResult, error) {
	return func(ctx context.Context, title, msg string) (nodeproto.NotifyResult, error) {
		switch runtime.GOOS {
		case "darwin":
			// The text goes in as arguments, never into the script.
			err := exec.CommandContext(ctx, "osascript",
				"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run",
				title, msg).Run()
			return nodeproto.NotifyResult{Shown: err == nil, Method: "osascript"}, err
		case "linux":
			err := exec.CommandContext(ctx, "notify-send", "--", title, msg).Run()
			if errors.Is(err, exec.ErrNotFound) {
				logf("node: notification (notify-send is not installed): %s: %s", title, msg)
				return nodeproto.NotifyResult{Method: "log", Reason: "notify-send is not installed"}, nil
			}
			return nodeproto.NotifyResult{Shown: err == nil, Method: "notify-send"}, err
		case "windows":
			return windowsToast(ctx, title, msg, logf)
		}
		logf("node: notification: %s: %s", title, msg)
		return nodeproto.NotifyResult{Method: "log"}, nil
	}
}
