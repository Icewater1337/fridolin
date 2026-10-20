package vault

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ErrOutOfScope is returned for a path outside a Scope's prefixes.
var ErrOutOfScope = errors.New("vault: path outside this brain's vault paths")

// Scope is a brain's view of the vault: only paths below its prefixes
// exist for it. The MCP tools vault_ls, vault_get, vault_put and
// vault_search go through a Scope.
type Scope struct {
	v        *Vault
	prefixes []string
}

// Scoped returns the view limited to the given folder prefixes (vault
// paths such as "brains/family" or "documents/family"). With no prefixes
// the scope sees nothing.
func (v *Vault) Scoped(prefixes []string) (*Scope, error) {
	s := &Scope{v: v}
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if _, err := cleanStrict(p); err != nil {
			return nil, fmt.Errorf("vault: scope prefix %q: %w", p, err)
		}
		s.prefixes = append(s.prefixes, p)
	}
	sort.Strings(s.prefixes)
	return s, nil
}

// Prefixes returns the scope's folders.
func (s *Scope) Prefixes() []string { return append([]string(nil), s.prefixes...) }

// Allowed reports whether rel is a clean path inside one of the prefixes
// (the prefix folder itself included).
func (s *Scope) Allowed(rel string) bool {
	if _, err := cleanStrict(rel); err != nil {
		return false
	}
	for _, p := range s.prefixes {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func (s *Scope) check(rel string) error {
	if !s.Allowed(rel) {
		return fmt.Errorf("%w: %s", ErrOutOfScope, rel)
	}
	return nil
}

// Ls lists dir. Inside a prefix it is Vault.Ls; on the way to one (such
// as "" for a scope of "brains/family") it shows only the folders that
// lead to the scope's prefixes.
func (s *Scope) Ls(ctx context.Context, dir string) ([]Entry, error) {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || !s.Allowed(dir) {
		var out []Entry
		seen := map[string]bool{}
		for _, p := range s.prefixes {
			rest, ok := p, dir == ""
			if !ok {
				rest, ok = strings.CutPrefix(p, dir+"/")
			}
			if !ok {
				continue
			}
			first, _, _ := strings.Cut(rest, "/")
			child := first
			if dir != "" {
				child = dir + "/" + first
			}
			if !seen[child] {
				seen[child] = true
				out = append(out, Entry{Path: child, IsDir: true})
			}
		}
		if out == nil {
			return nil, fmt.Errorf("%w: %s", ErrOutOfScope, dir)
		}
		return out, nil
	}
	return s.v.Ls(ctx, dir)
}

// Stat is Vault.Stat inside the scope.
func (s *Scope) Stat(ctx context.Context, rel string) (Entry, error) {
	if err := s.check(rel); err != nil {
		return Entry{}, err
	}
	return s.v.Stat(ctx, rel)
}

// Get is Vault.Get inside the scope.
func (s *Scope) Get(ctx context.Context, rel string, opts GetOptions) (string, error) {
	if err := s.check(rel); err != nil {
		return "", err
	}
	return s.v.Get(ctx, rel, opts)
}

// Put is Vault.Put inside the scope.
func (s *Scope) Put(ctx context.Context, rel string, r io.Reader, prov Provenance) (Entry, error) {
	if err := s.check(rel); err != nil {
		return Entry{}, err
	}
	return s.v.Put(ctx, rel, r, prov)
}

// Search is Vault.Search over the scope's paths only.
func (s *Scope) Search(ctx context.Context, query string, limit int) ([]Entry, error) {
	if len(s.prefixes) == 0 {
		return nil, nil
	}
	return s.v.search(ctx, query, limit, s.prefixes)
}
