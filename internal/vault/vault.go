// Package vault is the tiered store of docs/PLAN.md, Workstream D: a hot
// tier on disk with a budget, and a cold tier in object storage that holds
// every file. It keeps the index (vault.db), uploads what changes (Sync,
// fed by Watch), evicts by policy (Evict), brings files back (Get),
// rebuilds its index from the cold tier (Rebuild) and searches paths
// (Search). Scope narrows all of it to a brain's paths.
//
// The invariant: cold is complete. A file leaves the hot tier only when
// its bytes are in the cold tier and their hash is the hot file's hash.
package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/cloud"
)

// Options configure a Vault.
type Options struct {
	// Root is the hot tier's folder; paths in the vault are relative to it.
	Root string
	// DBPath is vault.db. It may lie inside Root (the default layout puts
	// it at <data>/vault.db); Sync never uploads it.
	DBPath string
	// Store is the cold tier.
	Store cloud.ObjectStore
	// Policy places files; the zero value means DefaultPolicy.
	Policy *Policy
	// BudgetBytes is the hot tier's budget; 0 means no budget.
	BudgetBytes int64
	// HeadroomPercent of the budget is kept free: eviction brings hot
	// usage down to BudgetBytes*(100-HeadroomPercent)/100, and Get refuses
	// a hydration that would go above it.
	HeadroomPercent int
	// Now is the clock (default time.Now).
	Now func() time.Time
	// OpenThreadPaths returns the paths open threads refer to; they stay
	// hot. Nil means none.
	OpenThreadPaths func(ctx context.Context) ([]string, error)
	// FreeSpace returns the free bytes of the disk holding dir; when set,
	// Get also refuses a hydration that leaves less than the headroom free.
	FreeSpace func(dir string) (int64, error)
	// Provenance classifies a file Sync finds that the index does not
	// know yet. Nil uses DefaultProvenance.
	Provenance func(rel string) Provenance
	// Ignore leaves more paths out of the vault, on top of the built-in
	// rules (IgnoredName).
	Ignore func(rel string) bool
	// VerifyByDownload makes Evict download the cold copy and compare its
	// hash before it deletes the hot one, instead of trusting the hash
	// recorded at upload together with the object's size.
	VerifyByDownload bool
	// Logger gets the vault's log lines (default slog.Default()).
	Logger *slog.Logger
}

// Vault is an open vault. Its methods are safe for concurrent use.
type Vault struct {
	opts   Options
	policy Policy
	root   *os.Root
	db     *sql.DB
	log    *slog.Logger

	// jobMu serialises the background jobs (Sync, Evict, Rebuild).
	jobMu sync.Mutex
	// mu guards every change to vault.db rows and the files they describe,
	// so that a check and the change it allows are one step.
	mu sync.Mutex
	// hydrating holds the paths Get is downloading, so a second caller
	// waits for the first instead of downloading again.
	hydrating map[string]chan struct{}
}

// manifestKey is the cold-tier object that lists every cold file with its
// hash, so that Rebuild can verify files after the hot tier is lost. The
// folder is reserved: Sync ignores it on the hot side.
const manifestKey = ".fridolin-vault/manifest.json"

// New opens the vault: it opens (or creates) vault.db and puts back any
// file an interrupted eviction left aside.
func New(opts Options) (*Vault, error) {
	if opts.Root == "" || opts.DBPath == "" || opts.Store == nil {
		return nil, errors.New("vault: Root, DBPath and Store are required")
	}
	if opts.HeadroomPercent < 0 || opts.HeadroomPercent >= 100 {
		return nil, fmt.Errorf("vault: headroom %d%% is not between 0 and 99", opts.HeadroomPercent)
	}
	abs, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	opts.Root = abs
	if opts.DBPath, err = filepath.Abs(opts.DBPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	db, err := openDB(opts.DBPath)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	v := &Vault{opts: opts, root: root, db: db, hydrating: map[string]chan struct{}{}}
	v.policy = DefaultPolicy()
	if opts.Policy != nil {
		v.policy = *opts.Policy
	}
	if v.opts.Now == nil {
		v.opts.Now = time.Now
	}
	v.log = opts.Logger
	if v.log == nil {
		v.log = slog.Default()
	}
	if err := v.recoverEvictions(); err != nil {
		return nil, errors.Join(err, v.Close())
	}
	return v, nil
}

// Close closes vault.db.
func (v *Vault) Close() error {
	return errors.Join(v.db.Close(), v.root.Close())
}

// Root returns the hot tier's folder.
func (v *Vault) Root() string { return v.opts.Root }

func (v *Vault) now() time.Time { return v.opts.Now() }

// Entry describes one path in the vault.
type Entry struct {
	Path         string     `json:"path"`
	IsDir        bool       `json:"is_dir,omitempty"`
	Size         int64      `json:"size"`
	ModTime      time.Time  `json:"mtime"`
	SHA256       string     `json:"sha256,omitempty"`
	Tier         Tier       `json:"tier,omitempty"`
	Class        Class      `json:"class,omitempty"`
	Pinned       bool       `json:"pinned,omitempty"`
	LastAccess   time.Time  `json:"last_access"`
	Provenance   Provenance `json:"provenance,omitempty"`
	HasCompanion bool       `json:"has_companion,omitempty"`
}

func (r row) entry() Entry {
	return Entry{
		Path: r.Path, Size: r.Size, ModTime: time.Unix(0, r.MtimeNS).UTC(), SHA256: r.SHA256,
		Tier: r.Tier, Class: r.Class, Pinned: r.Pinned, LastAccess: fromMS(r.LastAccess),
		Provenance: r.Provenance, HasCompanion: r.HasCompanion,
	}
}

// ErrNotFound is returned (wrapped, together with fs.ErrNotExist) for a
// path the vault does not hold.
var ErrNotFound = errors.New("vault: no such file")

func notFound(rel string) error {
	return fmt.Errorf("%w: %s (%w)", ErrNotFound, rel, fs.ErrNotExist)
}

// CleanPath checks that rel is a clean, relative, slash-separated path —
// no leading slash, no "." or ".." segment, no backslash — and returns
// it. A path that cleaning would change is refused, not rewritten.
func CleanPath(rel string) (string, error) { return cleanStrict(rel) }

func cleanStrict(rel string) (string, error) {
	if err := cloud.ValidKey(rel); err != nil {
		return "", fmt.Errorf("vault: bad path: %w", err)
	}
	return rel, nil
}

// IgnoredName reports whether a file or folder name is never part of the
// vault: the databases and their sidecars (vault.db*, bridge.db*), temp
// files (*.tmp), cold listings (.cold.json) and the reserved folder.
func IgnoredName(name string) bool {
	switch {
	case name == coldListingName, name == ".fridolin-vault":
		return true
	case strings.HasSuffix(name, ".tmp"):
		return true
	case name == "vault.db" || strings.HasPrefix(name, "vault.db-"):
		return true
	case name == "bridge.db" || strings.HasPrefix(name, "bridge.db-"):
		return true
	}
	return false
}

func (v *Vault) ignored(rel string) bool {
	for seg := range strings.SplitSeq(rel, "/") {
		if IgnoredName(seg) {
			return true
		}
	}
	abs := filepath.Join(v.opts.Root, filepath.FromSlash(rel))
	if abs == v.opts.DBPath || strings.HasPrefix(abs, v.opts.DBPath+"-") {
		return true
	}
	return v.opts.Ignore != nil && v.opts.Ignore(rel)
}

// DefaultProvenance: media and inbox files came from outside and are
// untrusted; anything else the vault finds on its own is system.
func DefaultProvenance(rel string) Provenance {
	if underDir(rel, "media") || underDir(rel, "inbox") {
		return ProvenanceUntrusted
	}
	return ProvenanceSystem
}

func (v *Vault) provenance(rel string) Provenance {
	if v.opts.Provenance != nil {
		if p := v.opts.Provenance(rel); p.valid() {
			return p
		}
	}
	return DefaultProvenance(rel)
}

func (v *Vault) abs(rel string) string {
	return filepath.Join(v.opts.Root, filepath.FromSlash(rel))
}

// hashFile returns the SHA-256 of the file at rel in the hot tier.
func (v *Vault) hashFile(rel string) (string, int64, error) {
	f, err := v.root.Open(filepath.FromSlash(rel))
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// lstat stats rel in the hot tier without following a final symlink.
func (v *Vault) lstat(rel string) (fs.FileInfo, error) {
	return v.root.Lstat(filepath.FromSlash(rel))
}

// Stat returns the entry for rel.
func (v *Vault) Stat(ctx context.Context, rel string) (Entry, error) {
	rel, err := cleanStrict(rel)
	if err != nil {
		return Entry{}, err
	}
	r, ok, err := getRow(ctx, v.db, rel)
	if err != nil {
		return Entry{}, err
	}
	if !ok {
		return Entry{}, notFound(rel)
	}
	return r.entry(), nil
}

// Touch records that rel was used now, which keeps it active.
func (v *Vault) Touch(ctx context.Context, rel string) error {
	rel, err := cleanStrict(rel)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := ms(v.now())
	res, err := v.db.ExecContext(ctx, `UPDATE files SET last_access = MAX(last_access, ?), updated_at = ? WHERE path = ?`, now, now, rel)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound(rel)
	}
	return nil
}

// Ls lists the folder dir ("" for the root): its files, hot and cold,
// and its subfolders (IsDir), sorted by name.
func (v *Vault) Ls(ctx context.Context, dir string) ([]Entry, error) {
	where := ``
	var args []any
	if dir != "" {
		var err error
		if dir, err = cleanStrict(strings.TrimSuffix(dir, "/")); err != nil {
			return nil, err
		}
		where = `WHERE path LIKE ? ESCAPE '\'`
		args = append(args, likeEscape(dir)+"/%")
	}
	rows, err := queryRows(ctx, v.db, where+` ORDER BY path`, args...)
	if err != nil {
		return nil, err
	}
	return listing(dir, rows), nil
}

// listing turns the rows below dir into dir's direct children.
func listing(dir string, rows []row) []Entry {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var out []Entry
	dirs := map[string]int{} // subfolder path → index in out
	for _, r := range rows {
		rest, ok := strings.CutPrefix(r.Path, prefix)
		if !ok {
			continue
		}
		if first, _, deeper := strings.Cut(rest, "/"); deeper {
			p := prefix + first
			if i, seen := dirs[p]; seen {
				out[i].Size += r.Size
				continue
			}
			dirs[p] = len(out)
			out = append(out, Entry{Path: p, IsDir: true, Size: r.Size})
			continue
		}
		out = append(out, r.entry())
	}
	return out
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Search returns up to limit entries whose path contains every word of
// query (case-insensitive for ASCII), most recently used first. Full-text
// search over contents is the memory index's job (M7).
func (v *Vault) Search(ctx context.Context, query string, limit int) ([]Entry, error) {
	return v.search(ctx, query, limit, nil)
}

func (v *Vault) search(ctx context.Context, query string, limit int, prefixes []string) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	var conds []string
	var args []any
	for w := range strings.FieldsSeq(query) {
		conds = append(conds, `path LIKE ? ESCAPE '\'`)
		args = append(args, "%"+likeEscape(w)+"%")
	}
	if len(conds) == 0 {
		return nil, nil
	}
	if len(prefixes) > 0 {
		var ors []string
		for _, p := range prefixes {
			ors = append(ors, `path LIKE ? ESCAPE '\'`)
			args = append(args, likeEscape(p)+"/%")
		}
		conds = append(conds, "("+strings.Join(ors, " OR ")+")")
	}
	args = append(args, limit)
	rows, err := queryRows(ctx, v.db, `WHERE `+strings.Join(conds, " AND ")+` ORDER BY last_access DESC, path LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = r.entry()
	}
	return out, nil
}

// Put writes r to rel in the hot tier (atomically), records its
// provenance and uploads it. A failed upload is not an error of Put: the
// file is hot, and the next Sync retries; the entry's tier says which.
func (v *Vault) Put(ctx context.Context, rel string, r io.Reader, prov Provenance) (Entry, error) {
	rel, err := cleanStrict(rel)
	if err != nil {
		return Entry{}, err
	}
	if v.ignored(rel) {
		return Entry{}, fmt.Errorf("vault: %s is not a vault path", rel)
	}
	if !prov.valid() {
		return Entry{}, fmt.Errorf("vault: unknown provenance %q", prov)
	}
	dir := path.Dir(rel)
	if err := v.root.MkdirAll(filepath.FromSlash(dir), 0o700); err != nil {
		return Entry{}, err
	}
	tmp := tempName(rel, "put")
	f, err := v.root.OpenFile(filepath.FromSlash(tmp), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Entry{}, err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		v.mu.Lock()
		err = v.root.Rename(filepath.FromSlash(tmp), filepath.FromSlash(rel))
		v.mu.Unlock()
	}
	if err != nil {
		_ = v.root.Remove(filepath.FromSlash(tmp))
		return Entry{}, err
	}
	if _, err := v.syncPaths(ctx, []string{rel}, map[string]Provenance{rel: prov}); err != nil {
		v.log.Warn("vault: upload after put failed; the next sync retries", "path", rel, "err", err)
	}
	return v.Stat(ctx, rel)
}

// tempName is a hidden temp file beside rel, which the vault ignores.
func tempName(rel, kind string) string {
	dir, name := path.Split(rel)
	return dir + "." + name + ".vault-" + kind + "-" + randomSuffix() + ".tmp"
}

func randomSuffix() string {
	var b [6]byte
	_, _ = io.ReadFull(randReader, b[:])
	return hex.EncodeToString(b[:])
}

// Stats are the index's totals, for a status page.
type Stats struct {
	HotBytes  int64 `json:"hot_bytes"`  // files on disk (tiers hot and both)
	ColdBytes int64 `json:"cold_bytes"` // files in the cold tier (tiers cold and both)
	Hot       int   `json:"hot"`        // on disk only
	Cold      int   `json:"cold"`       // in the cold tier only
	Both      int   `json:"both"`       // on disk and in the cold tier
	// LastUpload is when a file last reached the cold tier; zero for never.
	LastUpload time.Time `json:"last_upload,omitzero"`
}

// Stats sums the index up.
func (v *Vault) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	var last int64
	err := v.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN tier IN ('hot', 'both') THEN size END), 0),
		COALESCE(SUM(CASE WHEN tier IN ('cold', 'both') THEN size END), 0),
		COUNT(CASE WHEN tier = 'hot' THEN 1 END),
		COUNT(CASE WHEN tier = 'cold' THEN 1 END),
		COUNT(CASE WHEN tier = 'both' THEN 1 END),
		COALESCE(MAX(uploaded_at), 0)
		FROM files`).Scan(&s.HotBytes, &s.ColdBytes, &s.Hot, &s.Cold, &s.Both, &last)
	if last > 0 {
		s.LastUpload = fromMS(last)
	}
	return s, err
}

// Ping asks the cold tier for the manifest's object, which says the store
// answers and the credentials work; a store without a manifest yet is
// reachable too.
func (v *Vault) Ping(ctx context.Context) error {
	_, err := v.opts.Store.Stat(ctx, manifestKey)
	if errors.Is(err, cloud.ErrNotFound) {
		return nil
	}
	return err
}
