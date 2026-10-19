package httpapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type FilesystemAPI struct {
	Home            func() string
	BrowseHome      func() string
	DataDir         func() string
	FolderPicker    func(string) []string
	RunPicker       func(context.Context, []string) (string, error)
	PickerCancelled error
	PickMu          *sync.Mutex
}

func (a *FilesystemAPI) Caps(w http.ResponseWriter, r *http.Request) {
	home := a.Home()
	picker := ""
	if p := a.FolderPicker(home); p != nil {
		picker = filepath.Base(p[0])
	}
	WriteJSON(w, http.StatusOK, PickerCapabilitiesResponse{Picker: picker, Home: home})
}

func (a *FilesystemAPI) Pick(w http.ResponseWriter, r *http.Request) {
	argv := a.FolderPicker(a.Home())
	if argv == nil {
		WriteErr(w, http.StatusNotImplemented, "no native folder picker on this system — browse or type the path instead")
		return
	}
	if !a.PickMu.TryLock() {
		WriteErr(w, http.StatusConflict, "a folder picker is already open")
		return
	}
	defer a.PickMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	p, err := a.RunPicker(ctx, argv)
	switch {
	case errors.Is(err, a.PickerCancelled):
		WriteJSON(w, http.StatusOK, PickerCancelledResponse{Cancelled: true})
	case err != nil:
		WriteErr(w, http.StatusInternalServerError, fmt.Sprintf("the folder dialog failed (%v) — browse or type the path instead", err))
	default:
		WriteJSON(w, http.StatusOK, PickerPathResponse{Path: p})
	}
}

// apiFSList lists folders inside your home folder, for the in-dashboard
// folder browser. The bridge's data folder is hidden.
func (a *FilesystemAPI) List(w http.ResponseWriter, r *http.Request) {
	home := fsx.Canonical(a.BrowseHome())
	p := r.URL.Query().Get("path")
	if p == "" {
		p = home
	}
	p = fsx.Canonical(fsx.ExpandHome(p, home))
	if !fsx.Within(p, home) {
		WriteErr(w, http.StatusForbidden, "only folders inside your home folder can be browsed")
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	root, err := os.OpenRoot(home)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer root.Close()
	rel, err := filepath.Rel(home, p)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dir, err := root.Open(rel)
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	data := fsx.Canonical(a.DataDir())

	out := []DirectoryEntry{}
	for _, e := range entries {
		if !e.IsDir() && e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") && !showHidden {
			continue
		}
		full := filepath.Join(p, e.Name())
		// Checking the parent is not enough: a child may be a symlink out
		// of home. Resolve it before probing its type or marker files, then
		// use the home root so a subsequent symlink swap cannot escape.
		resolved := fsx.Canonical(full)
		if !fsx.Within(resolved, home) || resolved == data {
			continue
		}
		child, err := filepath.Rel(home, resolved)
		if err != nil {
			continue
		}
		if st, err := root.Stat(child); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, DirectoryEntry{Name: e.Name(), Path: full, IsBrain: fileExistsInRoot(root, filepath.Join(resolved, manifest.File)), IsGit: fileExistsInRoot(root, filepath.Join(resolved, ".git"))})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	parent := filepath.Dir(p)
	if p == home {
		parent = ""
	}
	WriteJSON(w, http.StatusOK, DirectoryResponse{Path: p, Parent: parent, Home: home, Entries: out})
}

// fileExistsInRoot probes metadata only inside root. Resolve first so an
// absolute symlink to another allowed location works too; Root.Stat keeps
// a later symlink replacement from redirecting the probe outside the root.
func fileExistsInRoot(root *os.Root, p string) bool {
	rel, err := filepath.Rel(root.Name(), fsx.Canonical(p))
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return false
	}
	_, err = root.Stat(rel)
	return err == nil
}
