package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Icewater1337/fridolin/internal/audit"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/files"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

// The Files page and the wiki (docs/FILES.md): the folders the bridge is
// linked to, as the dashboard and the site read them.

func (b *App) newFilesAPI() *httpapi.FilesAPI {
	return &httpapi.FilesAPI{Roots: b.fileRoots, Wiki: b.wikis, Nodes: nodeFiles{b}, Audit: b.auditFileView, Tickets: b.fileTickets}
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
			out = append(out, files.BrainRoot(br.ID(), label, br.Dir(), on, exclude, deny))
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

// auditFileView writes a view of a file or page to the audit log.
func (b *App) auditFileView(v httpapi.FileAudit) {
	detail := v.Action + " " + v.Root
	if v.Path != "" {
		detail += " " + v.Path
	}
	b.audit.Append(audit.Entry{Kind: audit.FileView, Brain: v.Brain, By: v.By, Detail: detail, Outcome: v.Outcome})
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
