package main

// What the site hands to the bridge (docs/SITE.md, "Handing files to a
// brain" and "Searching a brain's folder").
//
// The live site's sandbox keeps it out of Fridolin's data folder and away
// from the bridge's HTTP port. A module therefore hands work over through a
// folder of the site's own, <data_dir>/site/spool/, one folder per request:
//
//   - ctx.ingest writes files/ and request.json. The bridge checks that the
//     live commit's module lists that brain in capabilities.ingest, moves the
//     files into its media folder and hands them to the brain as /ingest
//     would, then answers in result.json.
//   - ctx.search writes search.json. The bridge checks capabilities.search,
//     runs a read-only Claude agent (Read, Grep, Glob) in that brain's folder
//     under that brain's sandbox, writes its output to stream.jsonl as it
//     comes, and answers in result.json. A cancel file stops it.
//
// The bridge runs outside any sandbox and everything in the spool folder is
// the site's to write. So the bridge reaches it only through an os.Root,
// which refuses a symlink that leads out of the folder, trusts no name, and
// creates what it writes with O_EXCL.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// spoolRequest is a module's request.json (ctx.ingest).
type spoolRequest struct {
	Module string   `json:"module"`
	Brain  string   `json:"brain"`
	Files  []string `json:"files"`
	Text   string   `json:"text"`
	Sender string   `json:"sender"`
}

// spoolSearch is a module's search.json (ctx.search).
type spoolSearch struct {
	Module string `json:"module"`
	Brain  string `json:"brain"`
	Prompt string `json:"prompt"`
}

var spoolEntryRE = regexp.MustCompile(`^[0-9a-f]{24}$`)

const (
	spoolSearchLimit   = 4 // searches at once
	spoolSearchTimeout = 3 * time.Minute
)

func (m *SiteManager) spoolDir() string { return m.dir("spool") }

// watchSpool answers the site's requests until ctx ends.
func (m *SiteManager) watchSpool(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.processSpool(ctx)
		}
	}
}

// processSpool answers every request that has no answer yet and clears out
// what is old: answered requests after ten minutes, unfinished ones after an
// hour.
func (m *SiteManager) processSpool(ctx context.Context) {
	root, err := os.OpenRoot(m.spoolDir())
	if err != nil {
		return
	}
	defer root.Close()
	entries, err := os.ReadDir(m.spoolDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// The site makes these folders; anything that is not one is left alone.
		if !spoolEntryRE.MatchString(name) {
			continue
		}
		if st, err := root.Lstat(name); err != nil || !st.IsDir() {
			continue
		}
		if st, err := root.Lstat(path.Join(name, "result.json")); err == nil {
			if time.Since(st.ModTime()) > 10*time.Minute {
				_ = root.RemoveAll(name)
			}
			continue
		}
		switch {
		case spoolHas(root, name, "request.json"):
			spoolAnswer(root, name, m.takeSpoolEntry(root, name), nil)
		case spoolHas(root, name, "search.json"):
			m.startSpoolSearch(ctx, name)
		case spoolHas(root, name, "device.json"):
			extra, err := m.takeDeviceEntry(root, name)
			spoolAnswer(root, name, err, extra)
		default:
			if st, err := root.Lstat(name); err == nil && time.Since(st.ModTime()) > time.Hour {
				_ = root.RemoveAll(name)
			}
		}
	}
}

func spoolHas(root *os.Root, entry, file string) bool {
	_, err := root.Lstat(path.Join(entry, file))
	return err == nil
}

// spoolRead reads a regular file of at most n bytes in the spool.
func spoolRead(root *os.Root, name string, n int64) ([]byte, error) {
	st, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > n {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", path.Base(name), n)
	}
	return root.ReadFile(name)
}

// spoolAnswer writes result.json: {ok:true, …extra} or {ok:false, error}.
func spoolAnswer(root *os.Root, entry string, err error, extra map[string]any) {
	answer := map[string]any{"ok": true}
	for k, v := range extra {
		answer[k] = v
	}
	if err != nil {
		log.Printf("[site] refused a request from the site: %v", err)
		answer = map[string]any{"ok": false, "error": err.Error()}
	}
	data, _ := json.Marshal(answer)
	part := path.Join(entry, "result.json."+randomToken(6))
	f, ferr := root.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if ferr == nil {
		_, ferr = f.Write(data)
		if cerr := f.Close(); ferr == nil {
			ferr = cerr
		}
	}
	if ferr == nil {
		ferr = root.Rename(part, path.Join(entry, "result.json"))
	}
	if ferr != nil {
		log.Printf("[site] could not answer %s: %v", entry, ferr)
		_ = root.Remove(part)
	}
}

// liveCaps is the live commit's capabilities of a module, or false.
func (m *SiteManager) liveCaps(module string) (siteModuleCaps, error) {
	s := m.Settings()
	if s.Approved == "" {
		return siteModuleCaps{}, errors.New("no commit of the site is live")
	}
	for _, mod := range readModuleCaps(m.modulesDirOf(s.Approved), homeDir()) {
		if mod.ID == module {
			return mod, nil
		}
	}
	return siteModuleCaps{}, fmt.Errorf("module %q is not live", module)
}

// ── ctx.ingest ────────────────────────────────────────────────────────────

// takeSpoolEntry checks one request and hands its files to the brain.
func (m *SiteManager) takeSpoolEntry(root *os.Root, entry string) error {
	data, err := spoolRead(root, path.Join(entry, "request.json"), 256*1024)
	if err != nil {
		return fmt.Errorf("reading the request: %w", err)
	}
	var req spoolRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return errors.New("the request is not valid JSON")
	}
	// The site's word on which module asked is checked against the live manifests.
	mod, err := m.liveCaps(req.Module)
	if err != nil {
		return err
	}
	if !slices.Contains(mod.Ingest, req.Brain) {
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
	// Check every file before taking any.
	for _, name := range req.Files {
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return fmt.Errorf("%q is not a plain file name", name)
		}
		st, err := root.Lstat(path.Join(entry, "files", name))
		if err != nil || !st.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", name)
		}
	}
	var paths []string
	for _, name := range req.Files {
		dst, err := spoolTake(root, path.Join(entry, "files", name), media)
		if err != nil {
			return fmt.Errorf("taking %s: %w", name, err)
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

// spoolTake copies a spool file into the media folder, as a new file, and
// removes it from the spool. A copy, not a rename: the source is opened
// through the root, so a symlink cannot make it a file from elsewhere.
func spoolTake(root *os.Root, name, media string) (string, error) {
	src, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if st, err := src.Stat(); err != nil || !st.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	dst := uniqueMediaPath(media, path.Base(name))
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dst)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", err
	}
	_ = root.Remove(name)
	return dst, nil
}

// uniqueMediaPath is name in media, with a time prefix if that is taken.
func uniqueMediaPath(media, name string) string {
	p := filepath.Join(media, name)
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return p
	}
	return filepath.Join(media, fmt.Sprintf("%d_%s", time.Now().UnixNano(), name))
}

// ── phones ────────────────────────────────────────────────────────────────

// spoolDevice is a phone's request the site checked and passed on
// (device.json), with an upload's transcript in body.txt beside it.
type spoolDevice struct {
	Op        string `json:"op"`
	Assertion string `json:"assertion"`
}

// takeDeviceEntry hands a phone's request to the bridge, which checks the
// phone's signature itself (plaud_app.go).
func (m *SiteManager) takeDeviceEntry(root *os.Root, entry string) (map[string]any, error) {
	data, err := spoolRead(root, path.Join(entry, "device.json"), 16*1024)
	if err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}
	var req spoolDevice
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, errors.New("the request is not valid JSON")
	}
	var body []byte
	if spoolHas(root, entry, "body.txt") {
		if body, err = spoolRead(root, path.Join(entry, "body.txt"), plaudTranscriptMax); err != nil {
			return nil, err
		}
	}
	if m.hooks.device == nil {
		return nil, errors.New("this Fridolin takes no requests from phones")
	}
	return m.hooks.device(req.Op, req.Assertion, body)
}

// ── ctx.search ────────────────────────────────────────────────────────────

// startSpoolSearch runs a search once: the started marker, made with O_EXCL,
// keeps a second look at the folder from starting it again.
func (m *SiteManager) startSpoolSearch(ctx context.Context, entry string) {
	root, err := os.OpenRoot(m.spoolDir())
	if err != nil {
		return
	}
	f, err := root.OpenFile(path.Join(entry, "started"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		root.Close()
		return
	}
	f.Close()
	go func() {
		defer root.Close()
		started := time.Now()
		err := m.runSpoolSearch(ctx, root, entry)
		spoolAnswer(root, entry, err, map[string]any{"duration_ms": time.Since(started).Milliseconds()})
	}()
}

func (m *SiteManager) runSpoolSearch(ctx context.Context, root *os.Root, entry string) error {
	data, err := spoolRead(root, path.Join(entry, "search.json"), 64*1024)
	if err != nil {
		return fmt.Errorf("reading the search: %w", err)
	}
	var req spoolSearch
	if err := json.Unmarshal(data, &req); err != nil {
		return errors.New("the search is not valid JSON")
	}
	mod, err := m.liveCaps(req.Module)
	if err != nil {
		return err
	}
	if !slices.Contains(mod.Search, req.Brain) {
		return fmt.Errorf("module %q does not list brain %q in capabilities.search", req.Module, req.Brain)
	}
	if strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 20000 {
		return errors.New("the prompt must be 1 to 20000 characters")
	}
	if m.hooks.search == nil {
		return errors.New("this Fridolin cannot run searches")
	}
	select {
	case m.searchSlots <- struct{}{}:
		defer func() { <-m.searchSlots }()
	default:
		return errors.New("too many searches at once; try again in a moment")
	}
	out, err := root.OpenFile(path.Join(entry, "stream.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	ctx, cancel := context.WithTimeout(ctx, spoolSearchTimeout)
	defer cancel()
	// The site asks to stop by writing a cancel file.
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if spoolHas(root, entry, "cancel") {
					cancel()
					return
				}
			}
		}
	}()
	err = m.hooks.search(ctx, req.Brain, req.Prompt, out)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the search took longer than %v", spoolSearchTimeout)
	}
	if ctx.Err() != nil {
		return errors.New("stopped")
	}
	return err
}

// runBrainSearch is the bridge's search hook: a read-only Claude agent in
// the brain's folder, under the brain's sandbox, with the brain's login.
func (b *Bridge) runBrainSearch(ctx context.Context, brainID, prompt string, out io.Writer) error {
	br := b.brainMgr.Get(brainID)
	if br == nil {
		return fmt.Errorf("no brain %q", brainID)
	}
	cfg := runtimeConfig(b.cfgs)
	// Only the brain folder's own setup, as for a brain's runs, and tools
	// that only read.
	args := claudeArgList(true, excludeInstructionsAbove(br.Dir()), "--tools", "Read,Grep,Glob")
	bin := cfg.ClaudePath
	env := childEnv(claudeChild, cfg.ChildEnvPassthrough)
	if sp := b.brainMgr.profiler; sp != nil && sp.Enabled() {
		// The brain's own Claude Code config dir, TMPDIR and caches, as for
		// its runs (see invoke): it may write nowhere else.
		state, err := prepareBrainState(absPath(cfg.DataDir), cfg.MediaDir, brainID, cfg.InheritClaudeUserConfig)
		if err != nil {
			return fmt.Errorf("the brain's sandbox: %w", err)
		}
		sbBin, sbArgs, err := sp.Command(brainID, bin, args)
		if err != nil {
			return fmt.Errorf("the brain's sandbox: %w", err)
		}
		bin, args = sbBin, sbArgs
		env = append(append(env, state...), "DISABLE_AUTOUPDATER=1")
	}
	proc, err := startClaude(ctx, claudeRun{binary: bin, args: args, dir: br.Dir(), env: env, prompt: prompt, stdout: out})
	if err == nil {
		err = proc.wait()
	}
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case proc == nil:
		return fmt.Errorf("claude: %w", err)
	}
	return fmt.Errorf("claude: %w: %s", err, textx.Tail(proc.stderrTail(), 400))
}
