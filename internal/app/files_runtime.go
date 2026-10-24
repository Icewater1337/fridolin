package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/sandbox"
	"github.com/Icewater1337/fridolin/internal/textx"
)

// The Files page and the wiki (docs/FILES.md): the folders the bridge is
// linked to, as the dashboard and the site read them.

func (b *App) newFilesAPI() *httpapi.FilesAPI {
	return &httpapi.FilesAPI{
		Roots: b.fileRoots, Wiki: b.wikis, Nodes: nodeFiles{b}, Audit: b.auditFile, Tickets: b.fileTickets,
		Commit: b.commitFiles, Inbox: b.fileInbox, HandOff: b.handOffUpload, History: b.fileHistory,
		SetUploadRule: b.registry.SetUploadFolder,
	}
}

// fileRoots are every running brain's folder and media inbox, and the
// folders approved nodes lend.
func (b *App) fileRoots() []files.Root {
	cfg := b.cfgs.Get()
	deny := filesDeny(b.env, cfg.Home, cfg.DataDir, cfg.DBPath, cfg.Sandbox.DenyPaths, sandboxPolicyOf(cfg).protected(cfg.Home, ownLayout()))
	media := AbsPath(cfg.MediaDir)
	if media == "" && cfg.DataDir != "" {
		media = filepath.Join(AbsPath(cfg.DataDir), "media")
	}
	out := []files.Root{}
	if b.brainMgr != nil {
		brains := b.brainMgr.Brains()
		slices.SortFunc(brains, func(x, y *brainruntime.Brain) int { return strings.Compare(x.ID(), y.ID()) })
		for _, br := range brains {
			m := br.Manifest()
			label := m.Name
			if label == "" {
				label = br.ID()
			}
			// What brain.json on disk leaves out too: whichever hides more.
			exclude := slices.Clone(m.Backup.Exclude)
			on := m.Backup.Enabled
			if disk, err := manifest.Read(br.Dir()); err == nil {
				exclude = append(exclude, disk.Backup.Exclude...)
				on = on && disk.Backup.Enabled
			}
			var uploads []files.Upload
			if m.Files != nil {
				for _, u := range m.Files.Uploads {
					uploads = append(uploads, files.Upload{Folder: u.Folder, ToBrain: u.ToBrain})
				}
			}
			out = append(out, files.BrainRoot(br.ID(), label, br.Dir(), on, exclude, deny).WithUploads(uploads))
			if media != "" {
				if dir := mediaInboxAt(media).Path(br.ID()); fsx.Exists(dir) {
					out = append(out, files.MediaRoot(br.ID(), label, dir, deny))
				}
			}
		}
	}
	if b.nodes != nil {
		views, _ := b.nodes.Views()
		for _, v := range views {
			if v.Device != nodeproto.DeviceApproved || v.Effective.FS == nil {
				continue
			}
			name := v.Name
			if name == "" {
				name = v.ID
			}
			for i, r := range v.Effective.FS.Roots {
				out = append(out, files.NodeRoot(v.ID, name, i, r.Path, r.Write, v.Online))
			}
		}
	}
	return out
}

// filesDeny are the absolute paths no root shows where they lie inside
// it: the bridge's data folder, database and config, the credentials
// folder, sandbox.deny_paths and what the sandbox keeps from brains. Your
// own folders (Documents and the like) are not among them: brains live
// there. env is the bridge's environment, home its home folder.
func filesDeny(env Environment, home, dataDir, dbPath string, denyPaths []string, protected []sandbox.ProtectedPath) []string {
	out := []string{AbsPath(dataDir), AbsPath(dbPath), getenv(env, "FRIDOLIN_SECRETS_DIR")}
	if p := getenv(env, "FRIDOLIN_CONFIG"); p != "" {
		out = append(out, AbsPath(p))
	}
	for _, p := range denyPaths {
		out = append(out, fsx.ExpandHome(p, home))
	}
	for _, p := range protected {
		if !slices.ContainsFunc(sandbox.UserFileFolders(), func(sub string) bool { return p.Path == filepath.Join(home, sub) }) {
			out = append(out, p.Path)
		}
	}
	return slices.DeleteFunc(out, func(p string) bool { return p == "" || !filepath.IsAbs(p) })
}

// nodeFiles reads node roots through the node registry.
type nodeFiles struct{ b *App }

func (n nodeFiles) List(ctx context.Context, node, dir string) ([]files.Entry, error) {
	var out nodeproto.FSListResult
	if err := n.b.nodes.Call(ctx, node, nodeproto.MethodFSList, nodeproto.PathParams{Path: dir}, &out); err != nil {
		return nil, err
	}
	entries := []files.Entry{}
	for _, e := range out.Entries {
		if e.Link {
			continue
		}
		x := files.Entry{Name: e.Name, Dir: e.Dir, ModTime: e.ModTime.UTC()}
		if !e.Dir {
			x.Size, x.Kind = e.Size, files.KindOf(e.Name)
		}
		entries = append(entries, x)
	}
	return entries, nil
}

func (n nodeFiles) Read(ctx context.Context, node, file string, max int64) ([]byte, int64, bool, error) {
	var out nodeproto.FSReadResult
	if err := n.b.nodes.Call(ctx, node, nodeproto.MethodFSRead, nodeproto.FSReadParams{Path: file, MaxBytes: max}, &out); err != nil {
		return nil, 0, false, err
	}
	return out.Data, out.Size, out.Truncated, nil
}

func (n nodeFiles) Write(ctx context.Context, node, file string, data []byte) error {
	var out nodeproto.FSWriteResult
	return n.b.nodes.Call(ctx, node, nodeproto.MethodFSWrite, nodeproto.FSWriteParams{Path: file, Data: data}, &out)
}

// auditFile writes a view or a change of a file to the audit log.
func (b *App) auditFile(v httpapi.FileAudit) {
	kind := audit.FileView
	if v.Change {
		kind = audit.FileChange
	}
	detail := v.Action + " " + v.Root
	if v.Path != "" {
		detail += " " + v.Path
	}
	b.audit.Append(audit.Entry{Kind: kind, Brain: v.Brain, By: v.By, Detail: detail, Outcome: v.Outcome})
}

// fileHistory reads the changes to a path of root from the audit log: the
// last year's, newest first, at most 100.
func (b *App) fileHistory(root files.Root, rel string) ([]httpapi.FileChange, error) {
	entries, err := audit.Read(b.audit.Dir, audit.Filter{Kind: audit.FileChange, Brain: root.Brain, Query: " " + root.ID + " ", Months: 12, Limit: 5000})
	if err != nil {
		return nil, err
	}
	out := []httpapi.FileChange{}
	for _, e := range entries {
		c, ok := httpapi.ParseChange(e.Detail, e.Outcome)
		if !ok || !strings.Contains(e.Detail, " "+root.ID+" ") || (c.Path != rel && c.To != rel) || strings.HasPrefix(c.Outcome, "refused") {
			continue
		}
		c.By = e.By
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
			c.At = t
		}
		out = append(out, c)
		if len(out) == 100 {
			break
		}
	}
	return out, nil
}

// handOffUpload hands files uploaded on the Files page to a brain, as /ingest
// does: its reply chat gets the answer.
func (b *App) handOffUpload(brainID, sender, text string, paths []string) error {
	br := b.brainMgr.Get(brainID)
	if br == nil {
		return fmt.Errorf("no brain %q", brainID)
	}
	jid := b.replyChatFor(br, RuntimeConfig(b.cfgs))
	if jid == "" {
		return fmt.Errorf("brain %s has no chat to reply in; set its reply chat", brainID)
	}
	return b.submitHandOff(br, jid, sender, text, paths, nil)
}

// fileInbox is a brain's media inbox, where a file uploaded for it waits.
func (b *App) fileInbox(brainID string) string {
	cfg := b.cfgs.Get()
	media := AbsPath(cfg.MediaDir)
	if media == "" {
		media = filepath.Join(AbsPath(cfg.DataDir), "media")
	}
	return mediaInboxAt(media).Path(brainID)
}

// commitFiles commits paths of a brain folder that is a git repository of
// its own, as the brain would: git runs in the brain's sandbox, where the
// brain's own .git/config, hooks and filters can do nothing the brain
// could not do itself. Hooks and fsmonitor are off all the same.
func (b *App) commitFiles(root files.Root, paths []string, message string) (string, error) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("git is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := RuntimeConfig(b.cfgs)
	git := func(args ...string) (string, error) {
		sub := args[0]
		args = append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgsign=false",
			"-c", "user.name=Fridolin", "-c", "user.email=fridolin@localhost", "-C", root.Path}, args...)
		bin, argv := gitBin, args
		env := childEnv(b.env, anyChild, nil, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0")
		if sp := b.brainMgr.profiler; sp != nil && sp.Enabled() {
			state, err := prepareBrainState(AbsPath(cfg.DataDir), cfg.MediaDir, root.Brain, cfg.InheritClaudeUserConfig)
			if err != nil {
				return "", fmt.Errorf("the brain's sandbox: %w", err)
			}
			if bin, argv, err = sp.Command(root.Brain, gitBin, args); err != nil {
				return "", fmt.Errorf("the brain's sandbox: %w", err)
			}
			env = append(env, state...)
		}
		cmd := exec.CommandContext(ctx, bin, argv...)
		cmd.Dir, cmd.Env = root.Path, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %s", sub, textx.Tail(strings.TrimSpace(string(out)), 300))
		}
		return strings.TrimSpace(string(out)), nil
	}
	pathArgs := append([]string{"--"}, paths...)
	if _, err := git(append([]string{"add", "-A"}, pathArgs...)...); err != nil {
		return "", err
	}
	if _, err := git(append([]string{"commit", "-q", "-m", message}, pathArgs...)...); err != nil {
		return "", err
	}
	return git("rev-parse", "--short", "HEAD")
}
