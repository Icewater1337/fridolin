package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// The backup family (docs/NODES.md, "Backups"): restic repositories the
// core keeps in folders the owner picked, and the read-only listing of
// drives and folder names the dashboard's picker shows. The core runs
// restic and sends the files restic writes, encrypted already; the node
// stores them by restic's own names and never runs restic itself.

// uploadsDir is the folder in a backup folder where a file being uploaded
// waits until its last chunk arrived and its hash checked out.
const uploadsDir = ".fridolin-uploads"

// maxBrowse bounds a folder listing.
const maxBrowse = 5000

// staleUpload is how old a half-uploaded file may grow before it is
// removed: the run that wrote it is long gone.
const staleUpload = 24 * time.Hour

func (a *Agent) backupCall(eff nodeproto.Capabilities, method string, params json.RawMessage) (any, *nodeproto.Error) {
	decode := func(v any) *nodeproto.Error {
		if len(params) == 0 {
			return nil
		}
		if err := json.Unmarshal(params, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	switch method {
	case nodeproto.MethodFSRoots:
		return a.roots(eff), nil
	case nodeproto.MethodFSBrowse:
		var p nodeproto.BrowseParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return browse(eff, p)
	case nodeproto.MethodBackupInfo, nodeproto.MethodBackupCreate:
		var p nodeproto.BackupFolderParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if method == nodeproto.MethodBackupCreate {
			return a.backupCreate(eff, p.Folder)
		}
		return backupInfo(eff, p.Folder)
	case nodeproto.MethodBackupWrite:
		var p nodeproto.BackupWriteParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.backupWrite(eff, p)
	case nodeproto.MethodBackupRead:
		var p nodeproto.BackupReadParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return backupRead(eff, p)
	}
	var p nodeproto.BackupFileParams
	if e := decode(&p); e != nil {
		return nil, e
	}
	switch method {
	case nodeproto.MethodBackupStat:
		return backupStat(eff, p)
	case nodeproto.MethodBackupList:
		return backupList(eff, p)
	case nodeproto.MethodBackupDelete:
		return a.backupDelete(eff, p)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

// roots lists the drives and volumes, or, where the node lends only some
// places for backups, those.
func (a *Agent) roots(eff nodeproto.Capabilities) nodeproto.FSRootsResult {
	out := nodeproto.FSRootsResult{Sep: string(filepath.Separator), Roots: []nodeproto.FSVolume{}}
	var vols []platform.Volume
	if len(eff.Backup.Roots) > 0 {
		for _, r := range eff.Backup.Roots {
			vols = append(vols, platform.Volume{Path: r, Label: filepath.Base(r)})
		}
	} else if a.Volumes != nil {
		vols = a.Volumes()
	} else {
		vols = platform.Volumes()
	}
	for _, v := range vols {
		if st, err := os.Stat(v.Path); err != nil || !st.IsDir() {
			continue
		}
		fv := nodeproto.FSVolume{Path: v.Path, Label: v.Label, Removable: v.Removable}
		if free, err := platform.FreeBytes(v.Path); err == nil {
			fv.Free = free
		}
		out.Roots = append(out.Roots, fv)
	}
	return out
}
