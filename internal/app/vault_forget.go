package app

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/apperr"
	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/vault"
)

// vaultForgetRecord is what the vault last forgot (vault.Forget): at a
// start, for the folders the import of a core move noted, or for
// fridolin vault forget.
type vaultForgetRecord struct {
	At        time.Time `json:"at,omitzero"`
	Prefix    string    `json:"prefix"`
	Forgotten int       `json:"forgotten"`
	Bytes     int64     `json:"bytes"`
	Orphans   int       `json:"orphans,omitempty"`
	Kept      int       `json:"kept,omitempty"`
	Failed    int       `json:"failed,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// holds reports whether prefix is, contains or lies in one of the hot
// root's vault folders: such a folder is the vault's to keep, not to
// forget.
func (l vaultLayout) holds(prefix string) bool {
	for _, p := range append(append([]string(nil), l.prefixes...), l.files...) {
		if p == prefix || strings.HasPrefix(p, prefix+"/") || strings.HasPrefix(prefix, p+"/") {
			return true
		}
	}
	return false
}

// checkForget refuses a prefix the vault still keeps files of.
func (s *vaultService) checkForget(prefix string) (string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if _, err := vault.CleanPath(prefix); err != nil {
		return "", apperr.New(apperr.Invalid, err.Error()+": name a folder below the data dir, such as brains-folders/Personal")
	}
	if s.currentLayout().holds(prefix) {
		return "", apperr.New(apperr.Invalid, prefix+" is part of the vault today; forget takes only a folder it no longer keeps, such as a brain folder that went home after a core move")
	}
	return prefix, nil
}

// Forget starts the job that forgets prefix (POST /api/vault/forget and
// fridolin vault forget): its rows and cold copies go, what is on disk
// stays.
func (s *vaultService) Forget(prefix string) error {
	prefix, err := s.checkForget(prefix)
	if err != nil {
		return err
	}
	return s.startJob("forget", func(ctx context.Context, run *vaultRun) error {
		_, err := s.forget(ctx, run.v, prefix)
		return err
	})
}

func (s *vaultService) forget(ctx context.Context, v *vault.Vault, prefix string) (vault.ForgetReport, error) {
	rep, err := v.Forget(ctx, prefix)
	r := &vaultForgetRecord{At: time.Now().UTC(), Prefix: prefix, Forgotten: rep.Forgotten, Bytes: rep.Bytes,
		Orphans: rep.Orphans, Kept: len(rep.Kept), Failed: len(rep.Failed)}
	if err != nil {
		r.Error = err.Error()
	} else {
		for p, e := range rep.Failed {
			r.Error = p + ": " + e
			break
		}
	}
	if r.Error != "" {
		s.log.Warn("vault: forgetting a folder", "prefix", prefix, "failed", r.Failed, "err", r.Error)
	}
	if r.Forgotten+r.Orphans > 0 || r.Kept > 0 {
		s.log.Info("vault: forgot a folder", "prefix", prefix, "files", r.Forgotten, "bytes", r.Bytes, "orphans", r.Orphans, "kept_on_disk", r.Kept)
	}
	s.mu.Lock()
	s.status.LastForget = r
	s.mu.Unlock()
	return rep, err
}

// forgetNoted forgets the folders core/vault-forget.json lists, before the
// first sync: what the import of a core move brought home from the data
// dir (docs/VAULT.md, "Forgetting a folder"). A folder done comes off the
// note; one that failed stays on it for the next start. A folder that is
// part of the vault again (a brain lives there now) comes off unforgotten.
func (s *vaultService) forgetNoted(ctx context.Context, v *vault.Vault, cfg *config.Settings) {
	file := filepath.Join(cfg.Boot.Layout().Core(), vault.ForgetNoteName)
	note, err := vault.ReadForgetNote(file)
	if err != nil {
		s.log.Warn("vault: reading what to forget", "err", err)
		return
	}
	if len(note.Folders) == 0 {
		return
	}
	var left []vault.ForgetFolder
	for _, f := range note.Folders {
		if ctx.Err() != nil {
			left = append(left, f)
			continue
		}
		if _, err := s.checkForget(f.Prefix); err != nil {
			s.log.Info("vault: not forgetting a noted folder", "prefix", f.Prefix, "why", err)
			continue
		}
		rep, err := s.forget(ctx, v, f.Prefix)
		if err != nil || len(rep.Failed) > 0 {
			left = append(left, f)
		}
	}
	note.Folders = left
	if err := vault.WriteForgetNote(file, note); err != nil {
		s.log.Warn("vault: updating what to forget", "err", err)
	}
}
