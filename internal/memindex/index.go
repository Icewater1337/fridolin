package memindex

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/store"
)

// Brain is what the index needs to know of one brain.
type Brain struct {
	// ID is the brain's id.
	ID string
	// Dir is the brain's folder, which holds the sources.
	Dir string
	// Index is the index file, in the bridge's data dir (see
	// config.Layout.MemoryIndex), where no brain can write.
	Index string
	// Owners are the numbers whose direct messages are owner
	// (allowed_numbers).
	Owners []string
}

// schemaVersion is the index's layout. An index of another version is
// dropped and built again: it is derived, so nothing is lost.
const schemaVersion = "1"

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sources (
	path TEXT PRIMARY KEY,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	hash TEXT NOT NULL,
	lines INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chunks (
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL,
	anchor TEXT NOT NULL,
	text TEXT NOT NULL,
	provenance TEXT NOT NULL CHECK (provenance IN ('owner', 'agent', 'untrusted', 'system')),
	importance INTEGER NOT NULL DEFAULT 0,
	triggers TEXT NOT NULL DEFAULT '',
	ts TEXT NOT NULL DEFAULT '',
	who TEXT NOT NULL DEFAULT '',
	chat TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS chunks_path ON chunks (path, anchor);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(text, triggers, tokenize = 'unicode61 remove_diacritics 2');
`

// Limits on what the indexer reads of a brain's folder, which the brain
// fills: far more than real notes hold, little enough that a runaway
// brain cannot make every run index gigabytes.
const (
	maxNoteFile       = 1 << 20
	maxTranscriptFile = 4 << 20
	maxFiles          = 5000
)

// locks serializes the bridge's own work on each index file; another
// process (fridolin memory reindex) waits on SQLite's lock instead.
var locks sync.Map // index path → *sync.Mutex

func lockFor(p string) func() {
	mu, _ := locks.LoadOrStore(p, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// index is an open index of one brain.
type index struct {
	b      Brain
	db     *sql.DB
	root   *os.Root
	owners map[string]bool
}

// open opens (creating it) b's index and its folder. create false
// refuses an index that does not exist yet, for a look that must not
// make one.
func open(b Brain, create bool) (*index, error) {
	if b.Index == "" || b.Dir == "" {
		return nil, errors.New("memory index: the brain has no folder or index path")
	}
	if !create {
		if _, err := os.Stat(b.Index); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(b.Index), 0o700); err != nil {
		return nil, fmt.Errorf("memory index: %w", err)
	}
	// 0600 before SQLite makes it: the index holds the brain's history.
	f, err := os.OpenFile(b.Index, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("memory index: %w", err)
	}
	f.Close()
	root, err := os.OpenRoot(b.Dir)
	if err != nil {
		return nil, fmt.Errorf("memory index: the brain's folder: %w", err)
	}
	db, err := sql.Open("sqlite", store.DSN(b.Index))
	if err != nil {
		root.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ix := &index{b: b, db: db, root: root, owners: map[string]bool{}}
	for _, o := range b.Owners {
		if k := chat.IdentityKey(o); k != "" {
			ix.owners[k] = true
		}
	}
	if err := ix.prepare(create); err != nil {
		ix.close()
		return nil, err
	}
	return ix, nil
}

// ErrOutdated is an index built by another layout of the index, or while
// other numbers were the owner's: the next sync builds it afresh.
var ErrOutdated = errors.New("the index was built for other owners or by another version; the next sync builds it afresh")

func (ix *index) close() {
	ix.db.Close()
	ix.root.Close()
}

// ownersKey is what the owner classification depends on: the history is
// classified again when it changes.
func (ix *index) ownersKey() string {
	keys := make([]string, 0, len(ix.owners))
	for k := range ix.owners {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	sum := sha256.Sum256([]byte(ix.b.ID + "\n" + strings.Join(keys, ",")))
	return hex.EncodeToString(sum[:8])
}

// prepare creates the tables, and drops what an index of another layout
// or for other owners holds, so that the next sync builds it afresh; with
// reset false it says so instead (ErrOutdated).
func (ix *index) prepare(reset bool) error {
	if _, err := ix.db.Exec(schema); err != nil {
		return fmt.Errorf("memory index: creating its tables: %w", err)
	}
	want := map[string]string{"schema": schemaVersion, "owners": ix.ownersKey()}
	for k, v := range want {
		var got string
		err := ix.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, k).Scan(&got)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if got != v {
			if !reset {
				return ErrOutdated
			}
			return ix.reset()
		}
	}
	return nil
}

// reset empties the index.
func (ix *index) reset() error {
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM chunks`, `DELETE FROM chunks_fts`, `DELETE FROM sources`, `DELETE FROM meta`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{"schema": schemaVersion, "owners": ix.ownersKey()} {
		if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Stats is what a sync did.
type Stats struct {
	Files   int `json:"files"`   // sources looked at
	Changed int `json:"changed"` // sources indexed again
	Removed int `json:"removed"` // sources gone since
	Chunks  int `json:"chunks"`  // chunks in the index after it
}

// source is one file the index reads, as found on disk.
type source struct {
	rel   string
	kind  string // "note", "transcript" or "history"
	size  int64
	mtime int64
}

// sources lists the brain's source files: its history, the markdown
// files under memories/ and the text files under transcripts/. Only
// regular files: a symlink, which the brain could point anywhere, is left
// out, and so is anything under a folder that is one.
func (ix *index) sources() ([]source, error) {
	var out []source
	if fi, err := ix.root.Lstat(HistoryFile); err == nil && fi.Mode().IsRegular() {
		out = append(out, source{rel: HistoryFile, kind: "history", size: fi.Size(), mtime: fi.ModTime().UnixNano()})
	}
	fsys := ix.root.FS()
	for _, top := range []struct{ dir, kind string }{{MemoriesDir, "note"}, {TranscriptsDir, "transcript"}} {
		n := 0
		err := fs.WalkDir(fsys, top.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == top.dir && errors.Is(err, fs.ErrNotExist) {
					return fs.SkipDir
				}
				return nil // an unreadable folder is left out, not the index
			}
			if d.IsDir() {
				if p != top.dir && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !wanted(top.kind, p) {
				return nil
			}
			if n++; n > maxFiles {
				return fs.SkipAll
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			out = append(out, source{rel: p, kind: top.kind, size: fi.Size(), mtime: fi.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// wanted reports whether the file p is a source of its kind.
func wanted(kind, p string) bool {
	ext := strings.ToLower(path.Ext(p))
	if kind == "note" {
		return ext == ".md"
	}
	return ext == ".md" || ext == ".txt"
}

// known is a source as the index last read it.
type known struct {
	size, mtime int64
	hash        string
	lines       int
}

func (ix *index) known() (map[string]known, error) {
	rows, err := ix.db.Query(`SELECT path, size, mtime, hash, lines FROM sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]known{}
	for rows.Next() {
		var p string
		var k known
		if err := rows.Scan(&p, &k.size, &k.mtime, &k.hash, &k.lines); err != nil {
			return nil, err
		}
		out[p] = k
	}
	return out, rows.Err()
}

// stale lists the sources the index does not hold as they are: new,
// changed or gone. A source counts as unchanged while its size and time
// are.
func (ix *index) stale() ([]string, error) {
	srcs, err := ix.sources()
	if err != nil {
		return nil, err
	}
	have, err := ix.known()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range srcs {
		k, ok := have[s.rel]
		if !ok || k.size != s.size || k.mtime != s.mtime {
			out = append(out, s.rel)
		}
		delete(have, s.rel)
	}
	for p := range have {
		out = append(out, p)
	}
	slices.Sort(out)
	return out, nil
}

// sync brings the index up to date with the folder: it reads again only
// the sources whose size or time changed, and of the history, which only
// grows, only what was appended since, unless the part it read before
// changed (a deleted message rewrites the file).
func (ix *index) sync(ctx context.Context) (Stats, error) {
	var st Stats
	srcs, err := ix.sources()
	if err != nil {
		return st, err
	}
	have, err := ix.known()
	if err != nil {
		return st, err
	}
	st.Files = len(srcs)
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()

	// The history first: notes that cite a message take its class.
	slices.SortStableFunc(srcs, func(a, b source) int {
		return boolInt(a.kind != "history") - boolInt(b.kind != "history")
	})
	for _, s := range srcs {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		k, ok := have[s.rel]
		delete(have, s.rel)
		if ok && k.size == s.size && k.mtime == s.mtime {
			continue
		}
		var changed bool
		if s.kind == "history" {
			changed, err = ix.syncHistory(tx, s, k, ok)
		} else {
			changed, err = ix.syncFile(tx, s, k, ok)
		}
		if err != nil {
			return st, fmt.Errorf("memory index: %s: %w", s.rel, err)
		}
		if changed {
			st.Changed++
		}
	}
	for p := range have {
		if err := dropSource(tx, p); err != nil {
			return st, err
		}
		st.Removed++
	}
	if err := tx.QueryRow(`SELECT count(*) FROM chunks`).Scan(&st.Chunks); err != nil {
		return st, err
	}
	return st, tx.Commit()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// dropSource removes a source and its chunks.
func dropSource(tx *sql.Tx, p string) error {
	if _, err := tx.Exec(`DELETE FROM chunks_fts WHERE rowid IN (SELECT id FROM chunks WHERE path = ?)`, p); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chunks WHERE path = ?`, p); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM sources WHERE path = ?`, p)
	return err
}

func putSource(tx *sql.Tx, p string, size, mtime int64, hash string, lines int) error {
	_, err := tx.Exec(`INSERT INTO sources (path, size, mtime, hash, lines) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, hash = excluded.hash, lines = excluded.lines`,
		p, size, mtime, hash, lines)
	return err
}

func insertChunk(tx *sql.Tx, c chunk) error {
	ts := ""
	if !c.ts.IsZero() {
		ts = c.ts.Format(time.RFC3339)
	}
	triggers := strings.Join(c.triggers, ", ")
	res, err := tx.Exec(`INSERT INTO chunks (path, anchor, text, provenance, importance, triggers, ts, who, chat) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.path, c.anchor, c.text, string(c.prov), c.importance, triggers, ts, c.who, c.chat)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO chunks_fts (rowid, text, triggers) VALUES (?, ?, ?)`, id, c.text, triggers)
	return err
}

// readSource reads a note or transcript, refusing a symlink put in its
// place since it was listed, and one over limit.
func (ix *index) readSource(rel string, limit int64) ([]byte, error) {
	f, err := fsx.OpenInFolder(ix.root, filepath.FromSlash(rel), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d KiB, left out", limit>>10)
	}
	return data, nil
}

// syncFile indexes a note or a transcript again, unless its content is
// what the index has (a touch changes only its time).
func (ix *index) syncFile(tx *sql.Tx, s source, k known, had bool) (bool, error) {
	limit := int64(maxNoteFile)
	if s.kind == "transcript" {
		limit = maxTranscriptFile
	}
	data, err := ix.readSource(s.rel, limit)
	if err != nil {
		// Left out until it can be read; what it held before goes too.
		if had {
			if err := dropSource(tx, s.rel); err != nil {
				return false, err
			}
		}
		return had, putSource(tx, s.rel, s.size, s.mtime, "unreadable", 0)
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if had && k.hash == hash {
		return false, putSource(tx, s.rel, s.size, s.mtime, hash, 0)
	}
	if err := dropSource(tx, s.rel); err != nil {
		return false, err
	}
	mtime := time.Unix(0, s.mtime)
	var chunks []chunk
	if s.kind == "transcript" {
		chunks = chunkTranscript(s.rel, data, mtime)
	} else {
		chunks = chunkMarkdown(s.rel, data, mtime)
		for i := range chunks {
			chunks[i].prov = classifyNote(tx, chunks[i].sources)
		}
	}
	for _, c := range chunks {
		if err := insertChunk(tx, c); err != nil {
			return false, err
		}
	}
	return true, putSource(tx, s.rel, s.size, s.mtime, hash, 0)
}

// classifyNote is the class of a note in memories/ with the sources its
// notes comment names. A note is the brain's own (agent) unless it says
// it came from elsewhere: a URL, "web", "untrusted", a transcript or a
// history line that is not the owner's makes it untrusted. One whose
// sources are all the owner's messages in the history is owner. A source
// the index cannot find changes nothing: the brain can only lower a
// note's class by what it writes, or raise it to owner by citing what the
// owner really said.
func classifyNote(tx *sql.Tx, sources []string) Provenance {
	prov, cited := Agent, false
	for _, s := range sources {
		low := strings.ToLower(s)
		switch {
		case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"),
			low == "web", low == "untrusted", low == "forwarded", low == "attachment",
			strings.HasPrefix(low, TranscriptsDir+"/"):
			return Untrusted
		}
		id, ok := historyAnchor(s)
		if !ok {
			continue
		}
		var p string
		err := tx.QueryRow(`SELECT provenance FROM chunks WHERE path = ? AND anchor = ? LIMIT 1`, HistoryFile, id).Scan(&p)
		switch {
		case err != nil:
		case Provenance(p) == Owner:
			cited = true
		default:
			return Untrusted
		}
	}
	if cited {
		prov = Owner
	}
	return prov
}

// historyAnchor is the line a source names in the history, written
// conversation-history.jsonl#ID or history#ID.
func historyAnchor(s string) (string, bool) {
	file, id, ok := strings.Cut(strings.TrimSpace(s), "#")
	if !ok || id == "" || (file != HistoryFile && file != "history") {
		return "", false
	}
	return id, true
}

// tailWindow is how much of the history before where the index stopped it
// compares, to tell an append from a rewrite.
const tailWindow = 4096

// syncHistory indexes what was appended to the history since the last
// sync, or all of it when what came before changed.
func (ix *index) syncHistory(tx *sql.Tx, s source, k known, had bool) (bool, error) {
	f, err := fsx.OpenInFolder(ix.root, HistoryFile, os.O_RDONLY, 0)
	if err != nil {
		if had {
			return true, dropSource(tx, HistoryFile)
		}
		return false, nil
	}
	defer f.Close()
	from, lines := int64(0), 0
	if had && k.size <= s.size {
		if h, err := tailHash(f, k.size); err == nil && h == k.hash {
			from, lines = k.size, k.lines
		}
	}
	if from == 0 {
		if err := dropSource(tx, HistoryFile); err != nil {
			return false, err
		}
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return false, err
	}
	// Up to the last whole line: one being appended is read next time.
	r := bufio.NewReaderSize(f, 64<<10)
	end := from
	for {
		line, err := readLine(r, maxHistoryLine)
		if errors.Is(err, io.EOF) {
			break // a line without its newline yet
		}
		if err != nil && !errors.Is(err, errLongLine) {
			return false, err
		}
		end += int64(line.n)
		lines++
		if err != nil {
			continue // too long: skipped, as the prompt's reader does
		}
		if c, ok := historyChunk(ix.owners, lines, line.text); ok {
			if err := insertChunk(tx, c); err != nil {
				return false, err
			}
		}
	}
	h, err := tailHash(f, end)
	if err != nil {
		return false, err
	}
	// Recorded as read up to end: the size and time are the file's only
	// once it read all of it, so that a line half written is read next time.
	size, mtime := s.size, s.mtime
	if end != s.size {
		size, mtime = end, 0
	}
	return true, putSource(tx, HistoryFile, size, mtime, h, lines)
}

// maxHistoryLine is the longest history line the index reads, as the
// prompt's reader (history.MaxLine).
const maxHistoryLine = 4 << 20

var errLongLine = errors.New("line too long")

type rawLine struct {
	n    int // its length with its newline, for the offset
	text []byte
}

// readLine reads one line ending in \n. A line longer than limit is read
// past and returned with errLongLine; one without its \n yet returns
// io.EOF.
func readLine(r *bufio.Reader, limit int) (rawLine, error) {
	var buf []byte
	n := 0
	long := false
	for {
		part, err := r.ReadSlice('\n')
		n += len(part)
		if !long {
			if len(buf)+len(part) > limit {
				long, buf = true, nil
			} else {
				buf = append(buf, part...)
			}
		}
		switch {
		case err == nil:
			if long {
				return rawLine{n: n}, errLongLine
			}
			return rawLine{n: n, text: buf}, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return rawLine{}, err
		}
	}
}

// tailHash is the SHA-256 of the tailWindow bytes of f before end, with
// end itself, so that a file rewritten to the same tail but another
// length differs.
func tailHash(f *os.File, end int64) (string, error) {
	start := max(0, end-tailWindow)
	buf := make([]byte, end-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(append(buf, []byte(strconv.FormatInt(end, 10))...))
	return hex.EncodeToString(sum[:]), nil
}
