package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/files"
)

// Changing files on the Files page and the wiki (docs/FILES.md, "Changing
// files"): edit a text file, upload, rename or move, delete into the
// folder's trash. The dashboard and a site admin change what they read
// where a folder is writable; a site role given uploads adds files to its
// brains' upload folders. Every change is audited, and committed where the
// folder is a git repository of its own.

// Bounds of a change.
const (
	// UploadPath is the one write that takes multipart, not JSON (CheckWrite).
	UploadPath = "/api/files/upload"
	// MaxUploadBody bounds an upload request: its files in all.
	MaxUploadBody = 1 << 30
	// maxUploadFile bounds one uploaded file; a node's folder takes what
	// a node hands over at once, maxNodeFileOpen.
	maxUploadFile = 200 << 20
	maxUploadN    = 20
	// maxEditText is the most a text file may hold to be edited here: what
	// a view shows of it.
	maxEditText = maxViewText
)

// FileChange is one change for the audit log, or as History reads it.
type FileChange struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Action  string    `json:"action"`
	Path    string    `json:"path"`
	To      string    `json:"to,omitempty"`
	Commit  string    `json:"commit,omitempty"`
	Outcome string    `json:"outcome"`
}

// FileSavedResponse is a file written: its new version and the commit.
type FileSavedResponse struct {
	Root    string    `json:"root"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	SHA     string    `json:"sha"`
	// Commit is the git commit of the change, where the folder is a
	// repository; GitError why there is none although it is.
	Commit   string `json:"commit,omitempty"`
	GitError string `json:"git_error,omitempty"`
}

// FileUploaded is one uploaded file.
type FileUploaded struct {
	Name string `json:"name"`
	// Path is where it is in the folder; empty when it went to the brain.
	Path string `json:"path,omitempty"`
	Size int64  `json:"size"`
}

// FilesUploadResponse is POST files/upload: where the files went
// (files.UploadFolder or files.UploadBrain).
type FilesUploadResponse struct {
	To       string         `json:"to"`
	Files    []FileUploaded `json:"files"`
	Commit   string         `json:"commit,omitempty"`
	GitError string         `json:"git_error,omitempty"`
}

// FileMovedResponse is a move or a delete: where it went.
type FileMovedResponse struct {
	Root     string `json:"root"`
	Path     string `json:"path"`
	Commit   string `json:"commit,omitempty"`
	GitError string `json:"git_error,omitempty"`
}

// FilesHistoryResponse is GET files/history: the changes to a file,
// newest first.
type FilesHistoryResponse struct {
	Changes []FileChange `json:"changes"`
}

// UploadRuleView is the upload folder a folder lies in (brain.json
// files.uploads) and its mode: files.UploadFolder or files.UploadBrain.
type UploadRuleView struct {
	Folder string `json:"folder"`
	Mode   string `json:"mode"`
}

func (a *FilesAPI) audited(s files.Scope, root files.Root, c FileChange, err error) {
	if a.Audit == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "refused: " + err.Error()
	} else if c.Commit != "" {
		outcome = "ok; commit " + c.Commit
	} else if c.Outcome != "" {
		outcome = c.Outcome
	}
	p := c.Path
	if c.To != "" {
		p += " -> " + c.To
	}
	a.Audit(FileAudit{By: s.By, Brain: root.Brain, Root: root.ID, Action: c.Action, Path: p, Outcome: outcome, Change: true})
}

// commit commits paths where root is a git repository of its own. It
// returns the commit, or why there is none.
func (a *FilesAPI) commit(root files.Root, s files.Scope, msg string, paths ...string) (string, string) {
	if a.Commit == nil || !root.IsRepo() {
		return "", ""
	}
	by := strings.TrimPrefix(s.By, "site:")
	hash, err := a.Commit(root, paths, fmt.Sprintf("%s (Fridolin, by %s)", msg, by))
	if err != nil {
		return "", "not committed: " + err.Error()
	}
	return hash, ""
}

// writeChangeErr answers why a change was refused.
func writeChangeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		WriteErr(w, http.StatusNotFound, "no such file or folder")
	case errors.Is(err, files.ErrReadOnly), errors.Is(err, files.ErrProtected):
		WriteErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, files.ErrChanged), errors.Is(err, files.ErrExists):
		WriteErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, files.ErrTooLarge), errors.Is(err, errTooLarge):
		WriteErr(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, errNodeOffline):
		WriteErr(w, http.StatusServiceUnavailable, err.Error())
	default:
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			WriteErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the upload is larger than %d MiB", mb.Limit>>20))
			return
		}
		WriteErr(w, http.StatusBadRequest, err.Error())
	}
}

// textRequest is PUT files/text.
type textRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
	Text string `json:"text"`
	// SHA is the version the edit started from (FileViewResponse.SHA); ""
	// creates a new file.
	SHA string `json:"sha"`
}

func (a *FilesAPI) saveText(w http.ResponseWriter, r *http.Request, s files.Scope) {
	var req textRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	root, ok := a.root(req.Root, s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	rel, err := files.Clean(req.Path)
	action := "edit"
	if req.SHA == "" {
		action = "create"
	}
	if err == nil {
		switch {
		case len(req.Text) > maxEditText:
			err = fmt.Errorf("%w: a text file edited here holds at most %d KiB", files.ErrTooLarge, maxEditText>>10)
		case !utf8.ValidString(req.Text):
			err = errors.New("the text is not UTF-8")
		case files.KindOf(rel) != files.FileText && files.KindOf(rel) != files.FileMarkdown && req.SHA == "":
			err = errors.New("a new file here is Markdown or text: give it a name ending in .md or .txt")
		}
	}
	var fi fs.FileInfo
	if err == nil {
		if root.Kind == files.KindNode {
			fi, err = a.nodeWrite(r.Context(), root, rel, []byte(req.Text), req.SHA, s)
		} else {
			fi, err = root.WriteFile(rel, []byte(req.Text), req.SHA, s)
		}
	}
	c := FileChange{Action: action, Path: rel}
	var gitErr string
	if err == nil {
		c.Commit, gitErr = a.commit(root, s, capitalized(action)+" "+rel, rel)
		c.Outcome = gitOutcome(gitErr)
	}
	a.audited(s, root, c, err)
	if err != nil {
		writeChangeErr(w, err)
		return
	}
	out := FileSavedResponse{Root: root.ID, Path: rel, SHA: files.SHA([]byte(req.Text)), Size: int64(len(req.Text)), Commit: c.Commit, GitError: gitErr}
	if fi != nil {
		out.ModTime = fi.ModTime().UTC()
	}
	WriteJSON(w, http.StatusOK, out)
}

func gitOutcome(gitErr string) string {
	if gitErr == "" {
		return ""
	}
	return "ok; " + gitErr
}

func capitalized(action string) string {
	if action == "" {
		return ""
	}
	return strings.ToUpper(action[:1]) + action[1:]
}

// nodeFileInfo is what a node wrote.
type nodeFileInfo struct {
	name string
	size int64
}

func (n nodeFileInfo) Name() string       { return n.name }
func (n nodeFileInfo) Size() int64        { return n.size }
func (n nodeFileInfo) Mode() fs.FileMode  { return 0o644 }
func (n nodeFileInfo) ModTime() time.Time { return time.Time{} }
func (n nodeFileInfo) IsDir() bool        { return false }
func (n nodeFileInfo) Sys() any           { return nil }

// nodeWrite replaces (or, with sha "", creates) a file of a node's folder.
func (a *FilesAPI) nodeWrite(ctx context.Context, root files.Root, rel string, data []byte, sha string, s files.Scope) (fs.FileInfo, error) {
	if err := root.CanWrite(rel, s); err != nil {
		return nil, err
	}
	if !root.Online {
		return nil, errNodeOffline
	}
	if a.Nodes == nil {
		return nil, errNoRoot
	}
	cur, _, truncated, err := a.Nodes.Read(ctx, root.Node, nodePath(root, rel), maxNodeFileOpen)
	switch {
	case err != nil && sha != "":
		return nil, err
	case err == nil && sha == "":
		return nil, files.ErrExists
	case err == nil && (truncated || files.SHA(cur) != sha):
		return nil, files.ErrChanged
	}
	if err := a.Nodes.Write(ctx, root.Node, nodePath(root, rel), data); err != nil {
		return nil, err
	}
	return nodeFileInfo{name: path.Base(rel), size: int64(len(data))}, nil
}

// moveRequest is POST files/move and files/delete (which takes Path).
type moveRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
	To   string `json:"to"`
}

func (a *FilesAPI) localChange(w http.ResponseWriter, r *http.Request, s files.Scope) (moveRequest, files.Root, bool) {
	var req moveRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return req, files.Root{}, false
	}
	root, ok := a.root(req.Root, s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return req, root, false
	}
	if root.Kind == files.KindNode {
		WriteErr(w, http.StatusBadRequest, "a node's files are renamed, moved and deleted on the node itself")
		return req, root, false
	}
	return req, root, true
}

func (a *FilesAPI) move(w http.ResponseWriter, r *http.Request, s files.Scope) {
	req, root, ok := a.localChange(w, r, s)
	if !ok {
		return
	}
	from, err := files.Clean(req.Path)
	to := req.To
	if err == nil {
		to, err = files.Clean(req.To)
	}
	if err == nil {
		err = root.Move(from, to, s)
	}
	c := FileChange{Action: "move", Path: from, To: to}
	var gitErr string
	if err == nil {
		c.Commit, gitErr = a.commit(root, s, "Move "+from+" to "+to, from, to)
		c.Outcome = gitOutcome(gitErr)
	}
	a.audited(s, root, c, err)
	if err != nil {
		writeChangeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, FileMovedResponse{Root: root.ID, Path: to, Commit: c.Commit, GitError: gitErr})
}

func (a *FilesAPI) remove(w http.ResponseWriter, r *http.Request, s files.Scope) {
	req, root, ok := a.localChange(w, r, s)
	if !ok {
		return
	}
	rel, err := files.Clean(req.Path)
	var at string
	if err == nil {
		at, err = root.Trash(rel, s, a.now())
	}
	c := FileChange{Action: "delete", Path: rel, To: at}
	var gitErr string
	if err == nil {
		c.Commit, gitErr = a.commit(root, s, "Delete "+rel, rel)
		c.Outcome = gitOutcome(gitErr)
	}
	a.audited(s, root, c, err)
	if err != nil {
		writeChangeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, FileMovedResponse{Root: root.ID, Path: at, Commit: c.Commit, GitError: gitErr})
}

func (a *FilesAPI) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// upload takes POST files/upload?root=&path=: multipart, each part named
// "file" one file. They land in the folder, or go to the brain, as
// UploadTo says for the reader.
func (a *FilesAPI) upload(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := r.URL.Query()
	root, ok := a.root(q.Get("root"), s)
	if !ok {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	dir, err := files.Clean(q.Get("path"))
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	to := root.UploadTo(dir, s)
	if to == "" {
		err := errors.New("this folder takes no uploads from you")
		a.audited(s, root, FileChange{Action: "upload", Path: dir}, err)
		WriteErr(w, http.StatusForbidden, err.Error())
		return
	}
	if to == files.UploadBrain && (a.Inbox == nil || a.HandOff == nil) {
		WriteErr(w, http.StatusServiceUnavailable, "this Fridolin cannot hand files to brains")
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		WriteErr(w, http.StatusBadRequest, "send the files as multipart/form-data, each in a part named file")
		return
	}
	var got []FileUploaded
	var handed []string
	undo := func() {
		for _, p := range handed {
			_ = os.Remove(p)
		}
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err == nil && len(got) == maxUploadN {
			err = fmt.Errorf("%w: at most %d files at once", files.ErrTooLarge, maxUploadN)
		}
		if err == nil && (part.FormName() != "file" || part.FileName() == "") {
			part.Close()
			continue
		}
		var up FileUploaded
		if err == nil {
			up, err = a.takePart(r.Context(), root, dir, to, part.FileName(), part, s)
			part.Close()
		}
		if err != nil {
			a.audited(s, root, FileChange{Action: "upload", Path: dir}, err)
			if to == files.UploadBrain {
				undo()
			}
			writeChangeErr(w, err)
			return
		}
		if to == files.UploadBrain {
			handed = append(handed, up.Path)
			up.Path = ""
		}
		got = append(got, up)
	}
	if len(got) == 0 {
		WriteErr(w, http.StatusBadRequest, "no files in the upload")
		return
	}
	out := FilesUploadResponse{To: to, Files: got}
	if to == files.UploadBrain {
		err := a.HandOff(root.Brain, strings.TrimPrefix(s.By, "site:"), handOffText(got, dir, s), handed)
		names := make([]string, len(got))
		for i, f := range got {
			names[i] = f.Name
		}
		a.audited(s, root, FileChange{Action: "upload", Path: dir, Outcome: "ok; handed to the brain: " + strings.Join(names, ", ")}, err)
		if err != nil {
			undo()
			WriteErr(w, http.StatusBadGateway, "the brain did not take the files: "+err.Error())
			return
		}
		WriteJSON(w, http.StatusAccepted, out)
		return
	}
	paths := make([]string, len(got))
	for i, f := range got {
		paths[i] = f.Path
	}
	var gitErr string
	out.Commit, gitErr = a.commit(root, s, "Upload "+strings.Join(paths, ", "), paths...)
	out.GitError = gitErr
	for _, f := range got {
		a.audited(s, root, FileChange{Action: "upload", Path: f.Path, Commit: out.Commit, Outcome: gitOutcome(gitErr)}, nil)
	}
	WriteJSON(w, http.StatusOK, out)
}

// takePart writes one uploaded file where to says.
func (a *FilesAPI) takePart(ctx context.Context, root files.Root, dir, to, name string, part io.Reader, s files.Scope) (FileUploaded, error) {
	name = files.SafeName(name)
	switch {
	case to == files.UploadBrain:
		if root.Hidden(path.Join(dir, name), s) {
			return FileUploaded{}, fmt.Errorf("%s: Fridolin never shows a file of this name, so it cannot be uploaded here", name)
		}
		p, n, err := files.CreateIn(a.Inbox(root.Brain), name, part, maxUploadFile)
		return FileUploaded{Name: path.Base(p), Path: p, Size: n}, err
	case root.Kind == files.KindNode:
		return a.nodeUpload(ctx, root, dir, name, part, s)
	}
	p, n, err := root.Create(dir, name, part, maxUploadFile, s)
	return FileUploaded{Name: path.Base(p), Path: p, Size: n}, err
}

// nodeUpload writes a new file into a node's folder: a name not taken.
func (a *FilesAPI) nodeUpload(ctx context.Context, root files.Root, dir, name string, body io.Reader, s files.Scope) (FileUploaded, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxNodeFileOpen+1))
	if err != nil {
		return FileUploaded{}, err
	}
	if len(data) > maxNodeFileOpen {
		return FileUploaded{}, errTooLarge
	}
	entries, err := a.nodeList(ctx, root, dir, s)
	if err != nil {
		return FileUploaded{}, err
	}
	taken := func(n string) bool {
		return slices.ContainsFunc(entries, func(e files.Entry) bool { return strings.EqualFold(e.Name, n) })
	}
	final := name
	for i := 2; taken(final) && i < 1000; i++ {
		ext := path.Ext(name)
		final = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext)
	}
	rel := path.Join(dir, final)
	if _, err := a.nodeWrite(ctx, root, rel, data, "", s); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return FileUploaded{}, err
	}
	return FileUploaded{Name: final, Path: rel, Size: int64(len(data))}, nil
}

// handOffText is what the brain is told with uploaded files: what it would
// be told of a WhatsApp attachment, and where the uploader put them.
func handOffText(got []FileUploaded, dir string, s files.Scope) string {
	names := make([]string, len(got))
	for i, f := range got {
		names[i] = f.Name
	}
	where := "the dashboard's Files page"
	if strings.HasPrefix(s.By, "site:") {
		where = "the site's Files page, by " + strings.TrimPrefix(s.By, "site:")
	}
	what := "A file was"
	if len(got) > 1 {
		what = fmt.Sprintf("%d files were", len(got))
	}
	text := fmt.Sprintf("%s uploaded on %s: %s. File them as you would a file sent on WhatsApp: put each where it belongs in your folder and note it where you keep track of your documents, without asking.", what, where, strings.Join(names, ", "))
	if dir != "" {
		text += fmt.Sprintf(" The uploader put them into %s/, so prefer that place unless it is clearly wrong.", dir)
	}
	return text
}

// uploadRuleRequest is PUT files/upload-rule.
type uploadRuleRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
	// Mode is files.UploadFolder, files.UploadBrain or "" (no uploads
	// from site roles; you still upload).
	Mode string `json:"mode"`
}

func (a *FilesAPI) setUploadRule(w http.ResponseWriter, r *http.Request, s files.Scope) {
	var req uploadRuleRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	root, ok := a.root(req.Root, s)
	if !ok || root.Kind != files.KindBrain {
		WriteErr(w, http.StatusNotFound, "no such brain folder")
		return
	}
	rel, err := files.Clean(req.Path)
	if err == nil && (rel == "" || root.Hidden(rel, s)) {
		err = errors.New("choose a folder of the brain's folder, not the whole of it or one Fridolin does not show")
	}
	if err == nil {
		_, _, err = root.List(rel, s)
	}
	if err == nil && a.SetUploadRule == nil {
		err = errors.New("this Fridolin cannot change brains")
	}
	if err == nil {
		err = a.SetUploadRule(root.Brain, rel, req.Mode)
	}
	a.audited(s, root, FileChange{Action: "upload-rule " + orNone(req.Mode), Path: rel}, err)
	if err != nil {
		writeChangeErr(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, OperationResponse{Status: "saved"})
}

func orNone(mode string) string {
	if mode == "" {
		return "none"
	}
	return mode
}

func (a *FilesAPI) history(w http.ResponseWriter, r *http.Request, s files.Scope) {
	q := r.URL.Query()
	root, ok := a.root(q.Get("root"), s)
	if !ok || !s.Write {
		WriteErr(w, http.StatusNotFound, errNoRoot.Error())
		return
	}
	rel, err := files.Clean(q.Get("path"))
	if err == nil && root.Hidden(rel, s) {
		err = files.ErrHidden
	}
	if err != nil {
		writeFilesErr(w, err)
		return
	}
	changes := []FileChange{}
	if a.History != nil {
		if changes, err = a.History(root, rel); err != nil {
			WriteErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	WriteJSON(w, http.StatusOK, FilesHistoryResponse{Changes: changes})
}

// ParseChange reads a change back from an audit line's detail ("<action>
// <root> <path>[ -> <to>]") and outcome, for History.
func ParseChange(detail, outcome string) (FileChange, bool) {
	action, rest, ok := strings.Cut(detail, " ")
	if !ok {
		return FileChange{}, false
	}
	_, p, ok := strings.Cut(rest, " ")
	if !ok {
		return FileChange{}, false
	}
	c := FileChange{Action: action, Outcome: outcome}
	c.Path, c.To, _ = strings.Cut(p, " -> ")
	if _, hash, ok := strings.Cut(outcome, "commit "); ok {
		c.Commit = hash
	}
	return c, true
}
