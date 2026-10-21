// Package noderepo lets restic on the core keep a repository in a folder
// on a node (docs/VAULT.md, "Node targets"). It serves restic's REST
// backend protocol on a loopback port of the core, for one restic run,
// and carries each request over the node channel as the node's backup.*
// calls (nodeproto/backup.go). restic encrypts on the core: what crosses
// the channel and lands on the node is restic's ciphertext, by restic's
// own names, and the repository's password never leaves the core.
//
// Why not restic on the node, or rest-server there: either would put
// restic on every node, and the password on the node for each run, and
// a second way into the node besides its one connection to the core.
// This way the node needs nothing but the agent, and the folder on its
// disk is an ordinary local restic repository that restic opens directly
// on any machine (docs/VAULT.md, "Restoring from a node target").
package noderepo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// Caller makes a call on a node: nodes.Service.Call.
type Caller interface {
	Call(ctx context.Context, id, method string, params, out any) error
}

// Server is the REST backend of one repository, a folder on a node.
type Server struct {
	caller       Caller
	node, folder string
	user, pass   string
	ln           net.Listener
	srv          *http.Server
}

// Start listens on a loopback port of its own, with a user and password
// made for this start, and serves the repository in folder on node.
func Start(caller Caller, node, folder string) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{caller: caller, node: node, folder: folder, user: "fridolin", pass: randomHex(24), ln: ln}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Repo is restic's -r value.
func (s *Server) Repo() string { return "rest:http://" + s.ln.Addr().String() + "/" }

// Env is what restic needs to sign in, for its environment: never on its
// command line, where a process list would show it.
func (s *Server) Env() []string {
	return []string{"RESTIC_REST_USERNAME=" + s.user, "RESTIC_REST_PASSWORD=" + s.pass}
}

// Secret is the server's password, to blank out of errors.
func (s *Server) Secret() string { return s.pass }

// Close stops the server.
func (s *Server) Close() error { return s.srv.Close() }

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// v2 is the media type of restic's REST protocol v2, whose listings carry
// sizes.
const v2 = "application/vnd.x.restic.rest.v2"

func (s *Server) authorized(r *http.Request) bool {
	u, p, ok := r.BasicAuth()
	return ok && subtle.ConstantTimeCompare([]byte(u), []byte(s.user)) == 1 && subtle.ConstantTimeCompare([]byte(p), []byte(s.pass)) == 1
}

// ServeHTTP answers restic: / (create), /config, /<type>/ and
// /<type>/<name>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="fridolin"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		if r.Method != http.MethodPost || r.URL.Query().Get("create") != "true" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.fail(w, s.caller.Call(ctx, s.node, nodeproto.MethodBackupCreate, nodeproto.BackupFolderParams{Folder: s.folder}, nil))
		return
	}
	typ, name, _ := strings.Cut(p, "/")
	if typ == nodeproto.BackupConfig {
		if name != "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	} else if name == "" {
		if !strings.HasSuffix(p, "/") || r.Method != http.MethodGet || !slices.Contains(nodeproto.BackupTypes, typ) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.list(w, r, typ)
		return
	}
	if err := nodeproto.CheckBackupFile(typ, name); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	file := nodeproto.BackupFileParams{Folder: s.folder, Type: typ, Name: name}
	switch r.Method {
	case http.MethodHead:
		var st nodeproto.BackupFile
		if err := s.caller.Call(ctx, s.node, nodeproto.MethodBackupStat, file, &st); err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size, 10))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		s.read(w, r, file)
	case http.MethodPost:
		s.write(w, r, file)
	case http.MethodDelete:
		s.fail(w, s.caller.Call(ctx, s.node, nodeproto.MethodBackupDelete, file, nil))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// fail answers a call's error the way restic reads it: 404 for a file
// that is not there, which restic tells apart from a failure.
func (s *Server) fail(w http.ResponseWriter, err error) {
	if err == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	status := http.StatusInternalServerError
	if pe, ok := errors.AsType[*nodeproto.Error](err); ok {
		switch pe.Code {
		case nodeproto.CodeNotFound:
			status = http.StatusNotFound
		case nodeproto.CodeDenied, nodeproto.CodeOutsideRoot:
			status = http.StatusForbidden
		case nodeproto.CodeBadRequest:
			status = http.StatusBadRequest
		case nodeproto.CodeUnavailable, nodeproto.CodeTimeout:
			status = http.StatusServiceUnavailable
		}
	}
	http.Error(w, err.Error(), status)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, typ string) {
	var res nodeproto.BackupListResult
	if err := s.caller.Call(r.Context(), s.node, nodeproto.MethodBackupList, nodeproto.BackupFileParams{Folder: s.folder, Type: typ}, &res); err != nil {
		s.fail(w, err)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), v2) {
		w.Header().Set("Content-Type", v2)
		_ = json.NewEncoder(w).Encode(res.Files)
		return
	}
	names := make([]string, 0, len(res.Files))
	for _, f := range res.Files {
		names = append(names, f.Name)
	}
	w.Header().Set("Content-Type", "application/vnd.x.restic.rest.v1")
	_ = json.NewEncoder(w).Encode(names)
}

// parseRange reads "bytes=a-b" or "bytes=a-": from a, to b inclusive, or
// to the end (end -1).
func parseRange(h string) (start, end int64, err error) {
	if h == "" {
		return 0, -1, nil
	}
	spec, ok := strings.CutPrefix(h, "bytes=")
	a, b, ok2 := strings.Cut(spec, "-")
	if !ok || !ok2 || strings.Contains(spec, ",") {
		return 0, 0, fmt.Errorf("range %q", h)
	}
	if start, err = strconv.ParseInt(a, 10, 64); err != nil || start < 0 {
		return 0, 0, fmt.Errorf("range %q", h)
	}
	if b == "" {
		return start, -1, nil
	}
	if end, err = strconv.ParseInt(b, 10, 64); err != nil || end < start {
		return 0, 0, fmt.Errorf("range %q", h)
	}
	return start, end, nil
}

// read streams the file, or the range restic asked for, in chunks.
func (s *Server) read(w http.ResponseWriter, r *http.Request, file nodeproto.BackupFileParams) {
	start, end, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		return
	}
	chunk := func(off, limit int64) (nodeproto.BackupReadResult, error) {
		var res nodeproto.BackupReadResult
		n := int64(nodeproto.MaxBackupChunk)
		if limit >= 0 {
			n = min(n, limit-off)
		}
		err := s.caller.Call(r.Context(), s.node, nodeproto.MethodBackupRead, nodeproto.BackupReadParams{BackupFileParams: file, Offset: off, Length: n}, &res)
		return res, err
	}
	limit := int64(-1)
	if end >= 0 {
		limit = end + 1
	}
	first, err := chunk(start, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	stop := first.Size // exclusive
	if end >= 0 {
		stop = min(stop, end+1)
	}
	if start > first.Size {
		http.Error(w, "range past the end", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(stop-start, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Header.Get("Range") != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, max(stop-1, start), first.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	off := start
	data := first.Data
	for {
		if int64(len(data)) > stop-off {
			data = data[:stop-off]
		}
		if _, err := w.Write(data); err != nil {
			return
		}
		off += int64(len(data))
		if off >= stop || len(data) == 0 {
			return
		}
		next, err := chunk(off, stop)
		if err != nil {
			// The status is out: cut the body short, which restic sees.
			panic(http.ErrAbortHandler)
		}
		data = next.Data
	}
}

// write sends restic's upload to the node in chunks, the last one with
// the size and hash, which the node checks before the file appears.
func (s *Server) write(w http.ResponseWriter, r *http.Request, file nodeproto.BackupFileParams) {
	upload := randomHex(16)
	h := sha256.New()
	buf := make([]byte, nodeproto.MaxBackupChunk)
	next := make([]byte, nodeproto.MaxBackupChunk)
	readChunk := func(b []byte) (int, bool, error) {
		n, err := io.ReadFull(r.Body, b)
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return n, true, nil
		case err != nil:
			return n, false, err
		}
		return n, false, nil
	}
	n, done, err := readChunk(buf)
	var off int64
	for {
		if err != nil {
			http.Error(w, "reading the upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		// One chunk ahead, so the last one is sent as the last.
		var m int
		var nextDone bool
		if !done {
			m, nextDone, err = readChunk(next)
			if err != nil {
				continue
			}
			if m == 0 && nextDone {
				done = true
			}
		}
		h.Write(buf[:n])
		p := nodeproto.BackupWriteParams{BackupFileParams: file, Upload: upload, Offset: off, Data: buf[:n]}
		if done {
			p.Final, p.Size, p.SHA256 = true, off+int64(n), hex.EncodeToString(h.Sum(nil))
		}
		if err := s.caller.Call(r.Context(), s.node, nodeproto.MethodBackupWrite, p, nil); err != nil {
			s.fail(w, err)
			return
		}
		if done {
			w.WriteHeader(http.StatusOK)
			return
		}
		off += int64(n)
		buf, next = next, buf
		n, done = m, nextDone
	}
}
