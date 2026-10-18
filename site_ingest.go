package main

// Files the site hands to brains (docs/SITE.md, "Handing files to a brain").
//
// The live site's sandbox keeps it out of Fridolin's data folder and away from
// the bridge's HTTP port, so a module cannot drop files into the media folder
// or call /ingest. ctx.ingest instead puts the files and a request.json into a
// folder of its own under <data_dir>/site/spool/. The bridge looks there every
// second, checks that the module is live and lists that brain in
// capabilities.ingest, moves the files into its media folder and hands them to
// the brain as /ingest would. It answers in result.json next to the request.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// spoolRequest is a module's request.json.
type spoolRequest struct {
	Module string   `json:"module"`
	Brain  string   `json:"brain"`
	Files  []string `json:"files"`
	Text   string   `json:"text"`
	Sender string   `json:"sender"`
}

var spoolEntryRE = regexp.MustCompile(`^[0-9a-f]{24}$`)

func (m *SiteManager) spoolDir() string { return m.dir("spool") }

// watchSpool handles the site's requests until ctx ends.
func (m *SiteManager) watchSpool(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.processSpool()
		}
	}
}

// processSpool answers every request that has no answer yet, and clears
// out what is old: answered requests after ten minutes, unfinished ones
// after an hour.
func (m *SiteManager) processSpool() {
	entries, err := os.ReadDir(m.spoolDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(m.spoolDir(), e.Name())
		// The site writes these folders; anything that is not one is left alone.
		if !e.IsDir() || !spoolEntryRE.MatchString(e.Name()) || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		result := filepath.Join(dir, "result.json")
		if st, err := os.Lstat(result); err == nil {
			if time.Since(st.ModTime()) > 10*time.Minute {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, "request.json")); err != nil {
			if st, err := os.Lstat(dir); err == nil && time.Since(st.ModTime()) > time.Hour {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		answer := map[string]any{"ok": true}
		if err := m.takeSpoolEntry(dir); err != nil {
			log.Printf("[site] refused files from the site: %v", err)
			answer = map[string]any{"ok": false, "error": err.Error()}
		}
		data, _ := json.Marshal(answer)
		if err := writeFileAtomic(result, data, 0o600, false); err != nil {
			log.Printf("[site] could not answer %s: %v", dir, err)
		}
	}
}

// takeSpoolEntry checks one request and hands its files to the brain.
func (m *SiteManager) takeSpoolEntry(dir string) error {
	data, err := readAtMostFile(filepath.Join(dir, "request.json"), 256*1024)
	if err != nil {
		return fmt.Errorf("reading the request: %w", err)
	}
	var req spoolRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return errors.New("the request is not valid JSON")
	}
	s := m.Settings()
	if s.Approved == "" {
		return errors.New("no commit of the site is live")
	}
	// Only what the approved commit's module declares: the site's own word
	// on which module asked is checked against the live manifests.
	var allowed []string
	found := false
	for _, mod := range readModuleCaps(m.modulesDirOf(s.Approved), homeDir()) {
		if mod.ID == req.Module {
			allowed, found = mod.Ingest, true
		}
	}
	if !found {
		return fmt.Errorf("module %q is not live", req.Module)
	}
	if !slices.Contains(allowed, req.Brain) {
		return fmt.Errorf("module %q does not list brain %q in capabilities.ingest", req.Module, req.Brain)
	}
	if len(req.Files) == 0 && strings.TrimSpace(req.Text) == "" {
		return errors.New("no files and no text")
	}
	if m.hooks.ingest == nil || m.hooks.mediaDir == nil {
		return errors.New("this Fridolin cannot hand files to brains")
	}
	media := m.hooks.mediaDir()
	if err := os.MkdirAll(media, 0o700); err != nil {
		return err
	}
	// Check every file before moving any.
	var srcs []string
	for _, name := range req.Files {
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return fmt.Errorf("%q is not a plain file name", name)
		}
		src := filepath.Join(dir, "files", name)
		st, err := os.Lstat(src)
		if err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
		srcs = append(srcs, src)
	}
	var paths []string
	for _, src := range srcs {
		dst := uniqueMediaPath(media, filepath.Base(src))
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("moving %s into the media folder: %w", filepath.Base(src), err)
		}
		paths = append(paths, dst)
	}
	sender := req.Sender
	if sender == "" {
		sender = "site"
	}
	text := req.Text
	if text == "" {
		text = "New files were uploaded on the site."
	}
	return m.hooks.ingest(req.Brain, sender, text, paths)
}

// uniqueMediaPath is name in media, with a time prefix if that is taken.
func uniqueMediaPath(media, name string) string {
	p := filepath.Join(media, name)
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	return filepath.Join(media, fmt.Sprintf("%d_%s", time.Now().UnixNano(), name))
}

// readAtMostFile reads a file of at most n bytes, refusing a symlink.
func readAtMostFile(path string, n int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > n {
		return nil, fmt.Errorf("larger than %d bytes", n)
	}
	return os.ReadFile(path)
}
