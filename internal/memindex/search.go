package memindex

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Hit is one chunk a search found.
type Hit struct {
	// Path is the source file, relative to the brain's folder, and Anchor
	// where in it: a heading's slug in a note, the message id (or L and
	// the line number) in the history, p and the paragraph in a transcript.
	Path       string     `json:"path"`
	Anchor     string     `json:"anchor"`
	Text       string     `json:"text"`
	Provenance Provenance `json:"provenance"`
	Importance int        `json:"importance,omitempty"`
	Triggers   []string   `json:"triggers,omitempty"`
	// TS is when it was said, for a history line, the day of a daily
	// note, or when the file changed last.
	TS time.Time `json:"ts,omitzero"`
	// Who wrote a history line, and in which chat.
	Who  string `json:"from,omitempty"`
	Chat string `json:"chat,omitempty"`
	// Score is the rank, lower is better (FTS5's BM25, weighted by
	// importance).
	Score float64 `json:"score"`
}

// Ref is the hit's source anchor, path#anchor.
func (h Hit) Ref() string { return h.Path + "#" + h.Anchor }

// SearchOptions narrow a search.
type SearchOptions struct {
	// Limit is the most hits (default 10, at most 100).
	Limit int
	// Provenances keeps only hits of these classes; none keeps all.
	Provenances []Provenance
	// Exclude leaves out the hits it returns true for, before Limit
	// counts them.
	Exclude func(Hit) bool
}

// RecallOptions bound what Recall puts into a prompt.
type RecallOptions struct {
	// TopK is the most chunks (default DefaultTopK).
	TopK int
	// MaxChars is the most characters of text in all (default
	// DefaultMaxChars).
	MaxChars int
	// Exclude leaves out hits the prompt shows anyway, the chat's recent
	// messages and the new one.
	Exclude func(Hit) bool
}

// The defaults and bounds of what Recall injects.
const (
	DefaultTopK     = 5
	DefaultMaxChars = 2000
	MaxTopK         = 20
	MaxMaxChars     = 20000
)

// Search brings b's index up to date with its folder and returns the
// chunks that match q best, of every class unless opts narrows them, each
// labelled with its provenance. It is what a brain's own search, the
// fridolin memory search command and the MCP tool call.
func Search(ctx context.Context, b Brain, q string, opts SearchOptions) ([]Hit, error) {
	unlock := lockFor(b.Index)
	defer unlock()
	ix, err := open(b, true)
	if err != nil {
		return nil, err
	}
	defer ix.close()
	if _, err := ix.sync(ctx); err != nil {
		return nil, err
	}
	return ix.search(ctx, q, opts)
}

// Recall is what may go into a run's prompt unasked for message: the
// owner and agent chunks that match it best, never untrusted or system
// ones, cut to the budget of opts.
func Recall(ctx context.Context, b Brain, message string, opts RecallOptions) ([]Hit, error) {
	k := opts.TopK
	if k <= 0 {
		k = DefaultTopK
	}
	budget := opts.MaxChars
	if budget <= 0 {
		budget = DefaultMaxChars
	}
	hits, err := Search(ctx, b, message, SearchOptions{Limit: min(k, MaxTopK), Provenances: []Provenance{Owner, Agent}, Exclude: opts.Exclude})
	if err != nil {
		return nil, err
	}
	var out []Hit
	for _, h := range hits {
		if !h.Provenance.Injectable() { // never, by the query; kept as the rule
			continue
		}
		if budget < 80 {
			break
		}
		h.Text = truncate(h.Text, budget)
		budget -= len(h.Text)
		out = append(out, h)
	}
	return out, nil
}

// Sync brings b's index up to date with its folder.
func Sync(ctx context.Context, b Brain) (Stats, error) {
	unlock := lockFor(b.Index)
	defer unlock()
	ix, err := open(b, true)
	if err != nil {
		return Stats{}, err
	}
	defer ix.close()
	return ix.sync(ctx)
}

// Reindex builds b's index afresh from its folder.
func Reindex(ctx context.Context, b Brain) (Stats, error) {
	unlock := lockFor(b.Index)
	defer unlock()
	ix, err := open(b, true)
	if err != nil {
		return Stats{}, err
	}
	defer ix.close()
	if err := ix.reset(); err != nil {
		return Stats{}, err
	}
	return ix.sync(ctx)
}

// Stale lists the sources b's index does not hold as they are on disk, or
// says the index does not exist; it changes nothing.
func Stale(ctx context.Context, b Brain) ([]string, error) {
	unlock := lockFor(b.Index)
	defer unlock()
	ix, err := open(b, false)
	if err != nil {
		return nil, err
	}
	defer ix.close()
	return ix.stale()
}

func (ix *index) search(ctx context.Context, q string, opts SearchOptions) ([]Hit, error) {
	match := matchQuery(q)
	if match == "" {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 100)
	where, args := "chunks_fts MATCH ?", []any{match}
	if len(opts.Provenances) > 0 {
		marks := make([]string, len(opts.Provenances))
		for i, p := range opts.Provenances {
			marks[i] = "?"
			args = append(args, string(p))
		}
		where += " AND c.provenance IN (" + strings.Join(marks, ", ") + ")"
	}
	// BM25 is negative, better the lower; importance 5 makes a match half
	// again as strong. Trigger phrases weigh four times the text.
	rows, err := ix.db.QueryContext(ctx, `SELECT c.path, c.anchor, c.text, c.provenance, c.importance, c.triggers, c.ts, c.who, c.chat,
			bm25(chunks_fts, 1.0, 4.0) * (1.0 + 0.1 * c.importance) AS score
		FROM chunks_fts JOIN chunks c ON c.id = chunks_fts.rowid
		WHERE `+where+` ORDER BY score, c.ts DESC LIMIT ?`, append(args, limit*4+20)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		h, err := scanHit(rows)
		if err != nil {
			return nil, err
		}
		if opts.Exclude != nil && opts.Exclude(h) {
			continue
		}
		if out = append(out, h); len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

func scanHit(rows *sql.Rows) (Hit, error) {
	var h Hit
	var prov, triggers, ts string
	if err := rows.Scan(&h.Path, &h.Anchor, &h.Text, &prov, &h.Importance, &triggers, &ts, &h.Who, &h.Chat, &h.Score); err != nil {
		return h, err
	}
	h.Provenance = Provenance(prov)
	if triggers != "" {
		h.Triggers = strings.Split(triggers, ", ")
	}
	h.TS, _ = time.Parse(time.RFC3339, ts)
	return h, nil
}

// maxTerms bounds the words of a query, so that a long message makes a
// query of its most telling words, not of all of them.
const maxTerms = 32

// stopwords are words too common to tell notes apart, in the languages
// the brains are written to most.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for w := range strings.FieldsSeq(`the and for are but not you all any can had her was one our out has him his how its may new now see who did get let put say she too use what when where which while with that this from have they will would there their about been into than then them these those could should some your just also more like over such only very even most made after before because does doing done each here other same both under again were is it to of in on at as by an be or if so do no up we me my
	der die das und ist nicht ein eine einen dem den des mit von für auf aus bei wie was wer wann wo ich du er sie es wir ihr sind war hat haben dass oder aber auch noch nur schon mal dann denn wenn als zum zur im am um an zu in`) {
		m[w] = true
	}
	return m
}()

// matchQuery turns free text, a message or what a brain searches for, into
// an FTS5 query: its words, each quoted, any of them matching, so that
// nothing anyone writes is read as FTS5 syntax. "" when it has no word
// worth looking for.
func matchQuery(q string) string {
	seen := map[string]bool{}
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if utf8.RuneCountInString(w) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
		if len(terms) == maxTerms {
			break
		}
	}
	return strings.Join(terms, " OR ")
}
