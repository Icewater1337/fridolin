package nodes

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

func init() {
	// A chunk of 8 MiB over a slow link, and a walk of a large repository
	// for its size, take longer than an ordinary call.
	slowCalls[nodeproto.MethodBackupRead] = 5 * time.Minute
	slowCalls[nodeproto.MethodBackupWrite] = 5 * time.Minute
	slowCalls[nodeproto.MethodBackupInfo] = 10 * time.Minute
	slowCalls[nodeproto.MethodBackupList] = 5 * time.Minute
}

// ApproveBackupFolder adds folder to what node id's backup approval lets
// the core keep repositories in: the owner picked it for a backup target.
// The node must have its backup approved, and folder must lie under the
// places it lends for backups.
func (r *Registry) ApproveBackupFolder(id, folder string) (*Node, error) {
	if err := nodeproto.CheckAbsPath(folder); err != nil {
		return nil, fmt.Errorf("%w folder: %v", ErrInvalidInput, err)
	}
	return r.change(id, func(n *Node, _ time.Time) error {
		switch {
		case n.Device == nodeproto.DeviceRevoked:
			return ErrRevoked
		case n.Device != nodeproto.DeviceApproved:
			return ErrNotApproved
		case n.Approved.Backup == nil || n.Effective().Backup == nil:
			return fmt.Errorf("%w: node %s offers no backups, or they are not approved yet: approve what it offers first", ErrInvalidInput, n.ID)
		case !n.Effective().AllowsBrowse(folder):
			return fmt.Errorf("%w: %s is not under a place node %s lends for backups (%v)", ErrInvalidInput, folder, n.ID, n.Effective().Backup.Roots)
		}
		if !n.Approved.AllowsBackupFolder(folder) {
			n.Approved.Backup.Folders = append(n.Approved.Backup.Folders, folder)
		}
		return nil
	})
}

// RemoveBackupFolder takes folder out of node id's backup approval: its
// target was removed. The repository stays on the node.
func (r *Registry) RemoveBackupFolder(id, folder string) (*Node, error) {
	return r.change(id, func(n *Node, _ time.Time) error {
		if b := n.Approved.Backup; b != nil {
			b.Folders = slices.DeleteFunc(b.Folders, func(f string) bool { return nodeproto.SamePath(f, folder) })
		}
		return nil
	})
}

// ApproveBackupFolder approves folder on node id for backups and tells it.
func (s *Service) ApproveBackupFolder(id, folder string) (*Node, error) {
	n, err := s.reg.ApproveBackupFolder(id, folder)
	if err == nil {
		s.logf("nodes: %s (%s) may keep backups in %s", n.Name, n.ID, folder)
		s.pushState(n)
	}
	return n, err
}

// RemoveBackupFolder withdraws folder on node id and tells it.
func (s *Service) RemoveBackupFolder(id, folder string) (*Node, error) {
	n, err := s.reg.RemoveBackupFolder(id, folder)
	if err == nil {
		s.logf("nodes: %s (%s) keeps no backups in %s any more", n.Name, n.ID, folder)
		s.pushState(n)
	}
	return n, err
}

// Roots lists node id's drives and volumes.
func (s *Service) Roots(ctx context.Context, id string) (nodeproto.FSRootsResult, error) {
	var out nodeproto.FSRootsResult
	return out, s.Call(ctx, id, nodeproto.MethodFSRoots, struct{}{}, &out)
}

// Browse lists the folders of a folder on node id.
func (s *Service) Browse(ctx context.Context, id string, p nodeproto.BrowseParams) (nodeproto.BrowseResult, error) {
	var out nodeproto.BrowseResult
	return out, s.Call(ctx, id, nodeproto.MethodFSBrowse, p, &out)
}

// BackupInfo reads the state of a backup folder on node id.
func (s *Service) BackupInfo(ctx context.Context, id, folder string) (nodeproto.BackupInfo, error) {
	var out nodeproto.BackupInfo
	return out, s.Call(ctx, id, nodeproto.MethodBackupInfo, nodeproto.BackupFolderParams{Folder: folder}, &out)
}
