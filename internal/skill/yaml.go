package skill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The frontmatter of a skill is YAML, of which this reads the part that
// SKILL.md files use: block mappings and sequences by indentation, flow
// collections ([a, b], {"k": "v"}, as the portable format writes its
// metadata on one line), plain, single- and double-quoted scalars, block
// scalars (| and >) and comments. Every scalar stays a string: no
// numbers, booleans or nulls, no anchors, aliases, tags or documents.
// What it cannot read is an error, never a guess. A brain writes these
// files, so the parser bounds what it builds (maxDepth, maxNodes).

// NodeKind tells a scalar from a mapping and a sequence.
type NodeKind uint8

const (
	ScalarNode NodeKind = iota
	MapNode
	SeqNode
)

// Node is one value of the frontmatter.
type Node struct {
	Kind  NodeKind
	Value string // a scalar's text
	// Keys are a mapping's keys in the order written; Map holds their
	// values.
	Keys  []string
	Map   map[string]*Node
	Items []*Node // a sequence's items
}

// Get returns the value of key in a mapping, or nil.
func (n *Node) Get(key string) *Node {
	if n == nil || n.Kind != MapNode {
		return nil
	}
	return n.Map[key]
}

// Set puts key's value into a mapping, after the keys it has.
func (n *Node) Set(key string, v *Node) {
	if _, ok := n.Map[key]; !ok {
		n.Keys = append(n.Keys, key)
	}
	n.Map[key] = v
}

// Delete takes key out of a mapping.
func (n *Node) Delete(key string) {
	if _, ok := n.Map[key]; !ok {
		return
	}
	delete(n.Map, key)
	for i, k := range n.Keys {
		if k == key {
			n.Keys = append(n.Keys[:i:i], n.Keys[i+1:]...)
			break
		}
	}
}

// NewMap is an empty mapping.
func NewMap() *Node { return &Node{Kind: MapNode, Map: map[string]*Node{}} }

// NewScalar is a scalar.
func NewScalar(s string) *Node { return &Node{Kind: ScalarNode, Value: s} }

// NewList is a sequence of scalars.
func NewList(items []string) *Node {
	n := &Node{Kind: SeqNode, Items: []*Node{}}
	for _, s := range items {
		n.Items = append(n.Items, NewScalar(s))
	}
	return n
}

// Limits of what the parser builds from one frontmatter.
const (
	maxDepth = 16
	maxNodes = 4096
)

type yline struct {
	no      int // 1-based
	indent  int
	text    string // without the indentation
	blank   bool   // empty or a comment only
	content string // the raw line
}

type yparser struct {
	lines []yline
	i     int
	nodes int
}

// ParseYAML reads a frontmatter. An empty one is an empty mapping.
func ParseYAML(src string) (*Node, error) {
	if !utf8.ValidString(src) {
		return nil, errors.New("the frontmatter is not UTF-8")
	}
	p := &yparser{}
	for i, raw := range strings.Split(src, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimLeft(raw, " ")
		l := yline{no: i + 1, indent: len(raw) - len(trimmed), text: trimmed, content: raw}
		if strings.HasPrefix(trimmed, "\t") {
			return nil, fmt.Errorf("line %d: indent with spaces, not tabs", l.no)
		}
		t := strings.TrimSpace(trimmed)
		l.blank = t == "" || strings.HasPrefix(t, "#")
		p.lines = append(p.lines, l)
	}
	p.skipBlank()
	if p.i >= len(p.lines) {
		return NewMap(), nil
	}
	first := p.lines[p.i]
	if first.indent != 0 {
		return nil, fmt.Errorf("line %d: the frontmatter must start at the left margin", first.no)
	}
	if isSeqItem(first.text) {
		return nil, fmt.Errorf("line %d: the frontmatter must be a mapping (key: value), not a list", first.no)
	}
	n, err := p.parseMap(0, 1)
	if err != nil {
		return nil, err
	}
	p.skipBlank()
	if p.i < len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation", p.lines[p.i].no)
	}
	return n, nil
}

func (p *yparser) skipBlank() {
	for p.i < len(p.lines) && p.lines[p.i].blank {
		p.i++
	}
}

func (p *yparser) count() error {
	p.nodes++
	if p.nodes > maxNodes {
		return fmt.Errorf("the frontmatter has more than %d values", maxNodes)
	}
	return nil
}

func isSeqItem(text string) bool { return text == "-" || strings.HasPrefix(text, "- ") }

// parseBlock reads the block that starts at the next line, indented by
// more than parent.
func (p *yparser) parseBlock(parent, depth int) (*Node, error) {
	p.skipBlank()
	if p.i >= len(p.lines) || p.lines[p.i].indent <= parent {
		return NewScalar(""), p.count()
	}
	l := p.lines[p.i]
	if isSeqItem(l.text) {
		return p.parseSeq(l.indent, depth)
	}
	return p.parseMap(l.indent, depth)
}

func (p *yparser) parseMap(indent, depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("line %d: nested deeper than %d", p.lines[p.i].no, maxDepth)
	}
	if err := p.count(); err != nil {
		return nil, err
	}
	n := NewMap()
	for {
		p.skipBlank()
		if p.i >= len(p.lines) {
			return n, nil
		}
		l := p.lines[p.i]
		if l.indent < indent {
			return n, nil
		}
		if l.indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", l.no)
		}
		if isSeqItem(l.text) {
			return n, nil // a sequence at this indent belongs to the key above
		}
		key, rest, err := splitKey(l.text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", l.no, err)
		}
		if _, dup := n.Map[key]; dup {
			return nil, fmt.Errorf("line %d: %q is given twice", l.no, key)
		}
		p.i++
		v, err := p.parseValue(rest, indent, depth, true)
		if err != nil {
			return nil, err
		}
		n.Set(key, v)
	}
}

func (p *yparser) parseSeq(indent, depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("line %d: nested deeper than %d", p.lines[p.i].no, maxDepth)
	}
	if err := p.count(); err != nil {
		return nil, err
	}
	n := &Node{Kind: SeqNode, Items: []*Node{}}
	for {
		p.skipBlank()
		if p.i >= len(p.lines) {
			return n, nil
		}
		l := p.lines[p.i]
		if l.indent < indent || (l.indent == indent && !isSeqItem(l.text)) {
			return n, nil
		}
		if l.indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", l.no)
		}
		rest := strings.TrimPrefix(strings.TrimPrefix(l.text, "-"), " ")
		trimmed := strings.TrimLeft(rest, " ")
		col := indent + (len(l.text) - len(trimmed))
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") && looksLikeKey(trimmed) {
			// "- key: value": a mapping whose first key is on the dash's
			// line, at the column after the dash.
			p.lines[p.i] = yline{no: l.no, indent: col, text: trimmed, content: l.content}
			item, err := p.parseMap(col, depth+1)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, item)
			continue
		}
		p.i++
		item, err := p.parseValue(trimmed, indent, depth, false)
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, item)
	}
}

// parseValue reads what follows a key's colon or an item's dash: a
// scalar, a flow collection, a block scalar, or a block below.
func (p *yparser) parseValue(rest string, indent, depth int, inMap bool) (*Node, error) {
	no := p.lines[p.i-1].no
	rest = strings.TrimSpace(rest)
	switch {
	case rest == "" || strings.HasPrefix(rest, "#"):
		p.skipBlank()
		if inMap && p.i < len(p.lines) && p.lines[p.i].indent == indent && isSeqItem(p.lines[p.i].text) {
			return p.parseSeq(indent, depth+1) // key:\n- a\n- b
		}
		return p.parseBlock(indent, depth+1)
	case rest[0] == '|' || rest[0] == '>':
		return p.blockScalar(rest, indent, no)
	case rest[0] == '[' || rest[0] == '{':
		text := rest
		// A flow collection may go on over the lines below.
		for !flowClosed(text) && p.i < len(p.lines) && (p.lines[p.i].indent > indent || p.lines[p.i].blank) {
			text += " " + strings.TrimSpace(p.lines[p.i].text)
			p.i++
		}
		fp := &flowParser{s: text, p: p}
		v, err := fp.value(depth + 1)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", no, err)
		}
		fp.space()
		if tail := strings.TrimSpace(fp.s[fp.pos:]); tail != "" && !strings.HasPrefix(tail, "#") {
			return nil, fmt.Errorf("line %d: unexpected %q after the closing bracket", no, tail)
		}
		return v, nil
	case rest[0] == '&' || rest[0] == '*' || rest[0] == '!':
		return nil, fmt.Errorf("line %d: anchors, aliases and tags are not supported", no)
	}
	s, err := scalar(rest)
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", no, err)
	}
	return NewScalar(s), p.count()
}

// flowClosed reports whether the brackets of text balance, outside quotes.
func flowClosed(text string) bool {
	depth := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	return depth <= 0 && quote == 0
}

// blockScalar reads a | or > scalar: the lines below indented by more
// than indent.
func (p *yparser) blockScalar(header string, indent, no int) (*Node, error) {
	h := header
	if i := strings.Index(h, " #"); i >= 0 {
		h = strings.TrimSpace(h[:i])
	}
	style, chomp := h[0], h[1:]
	if chomp != "" && chomp != "-" && chomp != "+" {
		return nil, fmt.Errorf("line %d: block scalar header %q is not supported (use |, |-, > or >-)", no, header)
	}
	var lines []string
	block := -1
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if strings.TrimSpace(l.content) == "" {
			lines = append(lines, "")
			p.i++
			continue
		}
		if l.indent <= indent {
			break
		}
		if block < 0 {
			block = l.indent
		}
		if l.indent < block {
			break
		}
		lines = append(lines, l.content[block:])
		p.i++
	}
	// Trailing empty lines belong to the chomping, not the text.
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	body := lines[:end]
	var text string
	if style == '|' {
		text = strings.Join(body, "\n")
	} else {
		var b strings.Builder
		for i, l := range body {
			switch {
			case i == 0, body[i-1] == "" && l != "":
			case l == "":
				b.WriteByte('\n')
			default:
				b.WriteByte(' ')
			}
			b.WriteString(l)
		}
		text = b.String()
	}
	if chomp != "-" && text != "" {
		text += "\n"
	}
	return NewScalar(text), p.count()
}

var plainKeyRe = regexp.MustCompile(`^[^\s"'#\[\]{},&*!|>%@` + "`" + `][^:#]*?\s*:(\s|$)`)

// looksLikeKey reports whether a sequence item's text starts a mapping.
func looksLikeKey(text string) bool {
	if text[0] == '"' || text[0] == '\'' {
		_, rest, err := quotedPrefix(text)
		return err == nil && (strings.HasPrefix(rest, ":") && (len(rest) == 1 || rest[1] == ' '))
	}
	return plainKeyRe.MatchString(text)
}

// splitKey splits "key: rest" (the key plain or quoted).
func splitKey(text string) (key, rest string, err error) {
	if text[0] == '"' || text[0] == '\'' {
		k, after, err := quotedPrefix(text)
		if err != nil {
			return "", "", err
		}
		if !strings.HasPrefix(after, ":") || (len(after) > 1 && after[1] != ' ') {
			return "", "", fmt.Errorf("expected a colon after the key %q", k)
		}
		return k, after[1:], nil
	}
	if text[0] == '?' || text[0] == '[' || text[0] == '{' || text[0] == '&' || text[0] == '*' || text[0] == '!' {
		return "", "", errors.New("complex keys, anchors and tags are not supported")
	}
	for i := 0; i < len(text); i++ {
		if text[i] == '#' && i > 0 && text[i-1] == ' ' {
			break
		}
		if text[i] == ':' && (i+1 == len(text) || text[i+1] == ' ') {
			if key = strings.TrimSpace(text[:i]); key != "" {
				return key, text[i+1:], nil
			}
			break
		}
	}
	return "", "", fmt.Errorf("expected key: value, not %q", truncate(text, 40))
}

// quotedPrefix reads the quoted scalar at the start of s and returns it
// and what follows it.
func quotedPrefix(s string) (string, string, error) {
	q := s[0]
	if q == '\'' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), s[i+1:], nil
			}
			b.WriteByte(s[i])
		}
		return "", "", errors.New("a single-quoted string is not closed")
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			v, err := unquoteDouble(s[:i+1])
			return v, s[i+1:], err
		}
	}
	return "", "", errors.New("a double-quoted string is not closed")
}

// unquoteDouble reads a double-quoted scalar, with JSON's escapes (which
// YAML's include) and \/ .
func unquoteDouble(q string) (string, error) {
	var v string
	if err := json.Unmarshal([]byte(q), &v); err == nil {
		return v, nil
	}
	v, err := strconv.Unquote(q)
	if err != nil {
		return "", fmt.Errorf("bad escape in %s", truncate(q, 40))
	}
	return v, nil
}

// scalar reads a plain or quoted scalar on its own: the rest of a line.
func scalar(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if s[0] == '"' || s[0] == '\'' {
		v, rest, err := quotedPrefix(s)
		if err != nil {
			return "", err
		}
		if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", fmt.Errorf("unexpected %q after the quoted string", truncate(rest, 40))
		}
		return v, nil
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// flowParser reads a flow collection: [a, "b"], {"k": [1, 2]}.
type flowParser struct {
	s   string
	pos int
	p   *yparser
}

func (f *flowParser) space() {
	for f.pos < len(f.s) && (f.s[f.pos] == ' ' || f.s[f.pos] == '\t') {
		f.pos++
	}
}

func (f *flowParser) value(depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("nested deeper than %d", maxDepth)
	}
	if err := f.p.count(); err != nil {
		return nil, err
	}
	f.space()
	if f.pos >= len(f.s) {
		return nil, errors.New("a value is missing")
	}
	switch f.s[f.pos] {
	case '[':
		f.pos++
		n := &Node{Kind: SeqNode, Items: []*Node{}}
		for {
			f.space()
			if f.pos < len(f.s) && f.s[f.pos] == ']' {
				f.pos++
				return n, nil
			}
			v, err := f.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, v)
			if err := f.sep(']'); err != nil {
				return nil, err
			}
			if f.s[f.pos-1] == ']' {
				return n, nil
			}
		}
	case '{':
		f.pos++
		n := NewMap()
		for {
			f.space()
			if f.pos < len(f.s) && f.s[f.pos] == '}' {
				f.pos++
				return n, nil
			}
			k, err := f.scalar(true)
			if err != nil {
				return nil, err
			}
			f.space()
			if f.pos >= len(f.s) || f.s[f.pos] != ':' {
				return nil, fmt.Errorf("expected a colon after the key %q", k)
			}
			f.pos++
			if _, dup := n.Map[k]; dup {
				return nil, fmt.Errorf("%q is given twice", k)
			}
			v, err := f.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.Set(k, v)
			if err := f.sep('}'); err != nil {
				return nil, err
			}
			if f.s[f.pos-1] == '}' {
				return n, nil
			}
		}
	}
	s, err := f.scalar(false)
	if err != nil {
		return nil, err
	}
	return NewScalar(s), nil
}

// sep reads the comma between items, or the closing bracket.
func (f *flowParser) sep(closing byte) error {
	f.space()
	if f.pos >= len(f.s) {
		return fmt.Errorf("expected , or %c", closing)
	}
	switch f.s[f.pos] {
	case ',', closing:
		f.pos++
		return nil
	}
	return fmt.Errorf("expected , or %c, not %q", closing, truncate(f.s[f.pos:], 20))
}

func (f *flowParser) scalar(key bool) (string, error) {
	f.space()
	if f.pos >= len(f.s) {
		return "", errors.New("a value is missing")
	}
	if c := f.s[f.pos]; c == '"' || c == '\'' {
		v, rest, err := quotedPrefix(f.s[f.pos:])
		if err != nil {
			return "", err
		}
		f.pos = len(f.s) - len(rest)
		return v, nil
	}
	start := f.pos
	for f.pos < len(f.s) {
		c := f.s[f.pos]
		if c == ',' || c == ']' || c == '}' || c == '[' || c == '{' || (key && c == ':') || (c == ':' && f.pos+1 < len(f.s) && f.s[f.pos+1] == ' ') {
			break
		}
		f.pos++
	}
	v := strings.TrimSpace(f.s[start:f.pos])
	if v == "" {
		return "", errors.New("a value is missing")
	}
	return v, nil
}

// FormatYAML writes n, a mapping, as block YAML that ParseYAML reads back
// as n: lists of plain values in flow style, everything else as blocks,
// a value quoted when it would not read back as written.
func FormatYAML(n *Node) string {
	var b bytes.Buffer
	formatMap(&b, n, 0)
	return b.String()
}

func formatMap(b *bytes.Buffer, n *Node, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, k := range n.Keys {
		v := n.Map[k]
		b.WriteString(pad + quoteScalar(k) + ":")
		formatValue(b, v, indent)
	}
}

func formatValue(b *bytes.Buffer, v *Node, indent int) {
	switch v.Kind {
	case ScalarNode:
		b.WriteString(" " + quoteScalar(v.Value) + "\n")
	case MapNode:
		if len(v.Keys) == 0 {
			b.WriteString(" {}\n")
			return
		}
		b.WriteString("\n")
		formatMap(b, v, indent+2)
	case SeqNode:
		if flat(v) {
			parts := make([]string, len(v.Items))
			for i, it := range v.Items {
				parts[i] = quoteFlow(it.Value)
			}
			b.WriteString(" [" + strings.Join(parts, ", ") + "]\n")
			return
		}
		b.WriteString("\n")
		pad := strings.Repeat(" ", indent+2)
		for _, it := range v.Items {
			switch it.Kind {
			case MapNode:
				if len(it.Keys) == 0 {
					b.WriteString(pad + "- {}\n")
					continue
				}
				var inner bytes.Buffer
				formatMap(&inner, it, indent+4)
				// The first key goes on the dash's line.
				s := inner.String()
				b.WriteString(pad + "- " + s[indent+4:])
			case SeqNode:
				b.WriteString(pad + "-")
				formatValue(b, it, indent+2)
			default:
				b.WriteString(pad + "- " + quoteScalar(it.Value) + "\n")
			}
		}
	}
}

func flat(v *Node) bool {
	for _, it := range v.Items {
		if it.Kind != ScalarNode || strings.Contains(it.Value, "\n") {
			return false
		}
	}
	return true
}

var plainRe = regexp.MustCompile(`^[A-Za-z0-9_./$()][A-Za-z0-9 _./$(),'+=-]*$`)

// quoteScalar quotes s unless it reads back plain as itself.
func quoteScalar(s string) string {
	if plainRe.MatchString(s) && !strings.HasSuffix(s, " ") && !strings.Contains(s, " #") {
		return s
	}
	return jsonQuote(s)
}

// quoteFlow quotes s for a flow list, where commas end a value.
func quoteFlow(s string) string {
	if plainRe.MatchString(s) && !strings.ContainsAny(s, ",'") && !strings.HasSuffix(s, " ") {
		return s
	}
	return jsonQuote(s)
}

func jsonQuote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}
