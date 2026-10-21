package noderepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
	"github.com/Icewater1337/fridolin/internal/rcrypt"
)

// The outer layer (docs/VAULT.md, "Encryption"): with a cipher, every
// file restic writes is encrypted once more on the core, in rclone
// crypt's format (internal/rcrypt), before it crosses the node channel,
// and decrypted on the way back. The node keeps restic's names, so it
// still checks each path, and the folder opens on any machine with
// rclone's crypt remote over it (filename_encryption off, suffix none)
// and restic's rclone backend. Sizes restic sees are the plaintext's.

// SetCipher turns the outer layer on for this server's requests.
func (s *Server) SetCipher(c *rcrypt.Cipher) { s.cipher = c }

// Layers says what the folder holds: LayersNone (no repository yet),
// LayersRestic (a repository of restic's encryption alone, from before)
// or LayersCrypt (restic inside rclone crypt).
type Layers string

const (
	LayersNone   Layers = "none"
	LayersRestic Layers = "restic"
	LayersCrypt  Layers = "restic+crypt"
)

// Detect reads the start of the repository's config file.
func (s *Server) Detect(ctx context.Context) (Layers, error) {
	var res nodeproto.BackupReadResult
	err := s.caller.Call(ctx, s.node, nodeproto.MethodBackupRead, nodeproto.BackupReadParams{
		BackupFileParams: nodeproto.BackupFileParams{Folder: s.folder, Type: nodeproto.BackupConfig}, Offset: 0, Length: 8}, &res)
	if pe, ok := errors.AsType[*nodeproto.Error](err); ok && pe.Code == nodeproto.CodeNotFound {
		return LayersNone, nil
	}
	if err != nil {
		return "", err
	}
	if string(res.Data) == "RCLONE\x00\x00" {
		return LayersCrypt, nil
	}
	return LayersRestic, nil
}

// plainSize is the size restic sees of a stored file of n bytes.
func (s *Server) plainSize(n int64) (int64, error) {
	if s.cipher == nil {
		return n, nil
	}
	return rcrypt.DecryptedSize(n)
}

// cryptChunk is how much plaintext readCrypt decrypts at a time.
const cryptChunk = 16 * rcrypt.BlockSize

// readCrypt answers a GET with the plaintext of the range restic asked
// for, fetching only the blocks that hold it.
func (s *Server) readCrypt(w http.ResponseWriter, r *http.Request, file nodeproto.BackupFileParams) {
	start, end, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestedRangeNotSatisfiable)
		return
	}
	var st nodeproto.BackupFile
	if err := s.caller.Call(r.Context(), s.node, nodeproto.MethodBackupStat, file, &st); err != nil {
		s.fail(w, err)
		return
	}
	size, err := rcrypt.DecryptedSize(st.Size)
	if err != nil {
		http.Error(w, fmt.Sprintf("%s/%s: %v", file.Type, file.Name, err), http.StatusInternalServerError)
		return
	}
	if start > size {
		http.Error(w, "range past the end", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	stop := size
	if end >= 0 {
		stop = min(stop, end+1)
	}
	readAt := func(off, n int64) ([]byte, error) {
		var out []byte
		for n > 0 {
			var res nodeproto.BackupReadResult
			m := min(n, int64(nodeproto.MaxBackupChunk))
			if err := s.caller.Call(r.Context(), s.node, nodeproto.MethodBackupRead, nodeproto.BackupReadParams{BackupFileParams: file, Offset: off, Length: m}, &res); err != nil {
				return nil, err
			}
			out = append(out, res.Data...)
			if int64(len(res.Data)) < m {
				break
			}
			off, n = off+m, n-m
		}
		return out, nil
	}
	// The first piece before the status, so that a wrong key or a
	// damaged file is an error and not a cut body.
	first, err := s.cipher.DecryptRange(readAt, st.Size, start, min(cryptChunk, stop-start))
	if err != nil {
		http.Error(w, fmt.Sprintf("%s/%s: %v", file.Type, file.Name, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(stop-start, 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Header.Get("Range") != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, max(stop-1, start), size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	off, data := start, first
	for {
		if _, err := w.Write(data); err != nil {
			return
		}
		off += int64(len(data))
		if off >= stop || len(data) == 0 {
			return
		}
		if data, err = s.cipher.DecryptRange(readAt, st.Size, off, min(cryptChunk, stop-off)); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// encryptBody replaces the upload's body with its encrypted form.
func (s *Server) encryptBody(r *http.Request) error {
	enc, err := s.cipher.EncryptReader(r.Body)
	if err != nil {
		return err
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{enc, r.Body}
	return nil
}
