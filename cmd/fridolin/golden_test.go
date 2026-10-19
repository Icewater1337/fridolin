package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/version"
)

// The golden files in testdata/golden pin what the bridge says and writes
// today: API responses, sandbox profiles, prompts, the CLI's arguments,
// what `fridolin check` prints. They are the net the restructure into
// packages moves under: a move that changes one of them changed behaviour.
// A change that is meant to change one rewrites it with
//
//	go test -run 'TestName' -update .
//
// and the pull request says why. What differs between runs and machines
// (temp paths, times, PIDs, the version) is masked first, see scrubber, so
// a second -update writes the same bytes.
var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden with what the code does now")

// goldenDir holds the golden files, below the package's testdata.
const goldenDir = "testdata/golden"

// assertGolden compares got with the golden file name (a path below
// goldenDir), or writes it there with -update.
func assertGolden(t testing.TB, name string, got []byte) {
	t.Helper()
	// The tests run in an empty directory whose testdata links to the
	// package's (linkTestdata), so the path is the same either way.
	path := filepath.Join(pkgDir, goldenDir, filepath.FromSlash(name))
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s: %v\nwrite it with: go test -run '%s' -update .", name, err, testPattern(t))
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (-want +got):\n%s\nIf the change is intended, rewrite it with: go test -run '%s' -update .",
			name, lineDiff(string(want), string(got)), testPattern(t))
	}
}

// testPattern is a -run pattern for the top-level test t belongs to.
func testPattern(t testing.TB) string {
	top, _, _ := strings.Cut(t.Name(), "/")
	return "^" + top + "$"
}

// lineDiff shows the lines of want and got that differ, with a line of
// context, as -want and +got. It is a plain longest-common-subsequence
// diff: golden files are small.
func lineDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	// lcs[i][j] is the length of the longest common subsequence of a[i:]
	// and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op   byte // ' ', '-' or '+'
		text string
	}
	var lines []line
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			lines = append(lines, line{' ', a[i]})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			lines = append(lines, line{'-', a[i]})
			i++
		default:
			lines = append(lines, line{'+', b[j]})
			j++
		}
	}
	var sb strings.Builder
	shown, last := 0, -2
	for k, l := range lines {
		near := func(k int) bool { return k >= 0 && k < len(lines) && lines[k].op != ' ' }
		if l.op == ' ' && !near(k-1) && !near(k+1) {
			continue
		}
		if shown == 60 {
			sb.WriteString("…\n")
			break
		}
		if last >= 0 && k > last+1 {
			sb.WriteString("@@\n")
		}
		fmt.Fprintf(&sb, "%c%s\n", l.op, l.text)
		shown, last = shown+1, k
	}
	return sb.String()
}

// scrubber masks what differs between runs and machines in golden output.
type scrubber struct {
	// paths are replaced by their placeholder wherever they appear, the
	// longest first, so a path inside another keeps the inner placeholder
	// only when it is the more specific one.
	paths []pathMask
	// above is the chain of folders above the test's root, which is the
	// system's temp dir and differs between macOS and Linux; see
	// scrubAbove.
	above []string
	// keys are JSON object keys whose values are masked wherever they
	// appear: uptimes, PIDs, ETags. A key with dots is a path from the top
	// instead, where * stands for any key or index: "runtime.node".
	keys map[string]bool
	// values are whole JSON string values that are masked, such as the
	// version, which as a substring would hit unrelated words.
	values map[string]string
}

type pathMask struct{ path, placeholder string }

// newScrubber masks root, the test's own temp folder, as <ROOT> and the
// folders above it as <ABOVE>, HOME, which the tests put at <root>/home,
// as <HOME>, and the test binary, which stands in for the Claude and Plaud
// CLIs, as <TEST_BINARY>. / is above the root on every system and stays as
// it is: a rule for / covers the whole file system, such as the sandbox's
// deny of every write, and as <ABOVE> it would read as one for a folder
// above the root, with a count that differs between systems.
func newScrubber(root string) *scrubber {
	s := &scrubber{keys: map[string]bool{}, values: map[string]string{version.String(): "<VERSION>"}}
	s.addPath(root, "<ROOT>")
	s.addPath(filepath.Join(root, "home"), "<HOME>")
	if exe, err := filepath.Abs(os.Args[0]); err == nil {
		s.addPath(exe, "<TEST_BINARY>")
	}
	for d := filepath.Dir(fsx.Canonical(root)); d != filepath.Dir(d); d = filepath.Dir(d) {
		s.above = append(s.above, d)
	}
	return s
}

// addPath masks path, in both spellings when it runs through a symlink
// (/var and /private/var on macOS).
func (s *scrubber) addPath(path, placeholder string) {
	for _, p := range []string{filepath.Clean(path), fsx.Canonical(path)} {
		if !slices.ContainsFunc(s.paths, func(m pathMask) bool { return m.path == p }) {
			s.paths = append(s.paths, pathMask{p, placeholder})
		}
	}
	sort.SliceStable(s.paths, func(i, j int) bool { return len(s.paths[i].path) > len(s.paths[j].path) })
}

// maskKeys masks the values of these JSON keys.
func (s *scrubber) maskKeys(keys ...string) *scrubber {
	for _, k := range keys {
		s.keys[k] = true
	}
	return s
}

// with returns a copy of s that also masks these keys. The copy has its
// own paths and keys, so masking more on it leaves s as it was.
func (s *scrubber) with(keys ...string) *scrubber {
	c := *s
	c.paths = slices.Clone(s.paths)
	c.keys = maps.Clone(s.keys)
	return c.maskKeys(keys...)
}

// timeRE matches the times the bridge writes: RFC 3339, with or without
// fractional seconds, in UTC or with an offset.
var timeRE = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)

// stampRE matches the stamps backup version folders are named with: the
// time of the run in UTC, with a Z, or in local time for the folders made
// before (versionLayout).
var stampRE = regexp.MustCompile(`\b\d{8}-\d{6}Z?\b`)

// logTimeRE matches the date and time the standard logger puts first on
// every line.
var logTimeRE = regexp.MustCompile(`\d{4}/\d\d/\d\d \d\d:\d\d:\d\d`)

// text masks the paths and times in s.
func (s *scrubber) text(in string) string {
	for _, m := range s.paths {
		in = strings.ReplaceAll(in, m.path, m.placeholder)
	}
	in = s.scrubAbove(in)
	in = timeRE.ReplaceAllString(in, "<TIME>")
	in = logTimeRE.ReplaceAllString(in, "<TIME>")
	return stampRE.ReplaceAllString(in, "<STAMP>")
}

// aboveRE finds a quoted absolute path, as JSON, SBPL and the CLI's
// --settings write them, with an optional regex anchor.
var aboveRE = regexp.MustCompile(`"(\^?)(/[^"]*)"`)

// scrubAbove masks the folders above the test's root that a quoted path
// names: the folder itself, or a memory file or .claude in it, which the
// sandbox and the CLI's settings list for every folder above a brain's.
// That chain is /tmp on Linux and /private/var/folders/… on macOS, so it
// becomes <ABOVE> on both. Other paths, such as /usr/bin or / itself, stay.
func (s *scrubber) scrubAbove(in string) string {
	return aboveRE.ReplaceAllStringFunc(in, func(q string) string {
		m := aboveRE.FindStringSubmatch(q)
		anchor, p := m[1], m[2]
		// Patterns escape glob characters and regexes escape dots; compare
		// the plain path.
		plain := strings.NewReplacer(`\.`, ".", "[", "", "]", "").Replace(p)
		if slices.Contains(s.above, plain) {
			return `"` + anchor + `<ABOVE>"`
		}
		dir, name := filepath.Dir(plain), filepath.Base(plain)
		if slices.Contains(s.above, dir) {
			for _, f := range []string{"CLAUDE.md", "CLAUDE.local.md", ".mcp.json", ".claude"} {
				if name == f {
					return `"` + anchor + "<ABOVE>/" + name + `"`
				}
			}
		}
		for _, sub := range []string{"/.claude/CLAUDE.md", "/.claude/rules/**", "/.claude/rules"} {
			if d, ok := strings.CutSuffix(plain, sub); ok && slices.Contains(s.above, d) {
				return `"` + anchor + "<ABOVE>" + sub + `"`
			}
		}
		return q
	})
}

// lines masks text, and then keeps each line that names the folders above
// the root only where it first appears in its block (blocks end at a blank
// line), with how many times it came, see collapseAbove: the sandbox writes
// one such line per folder, and how many folders there are depends on the
// system.
func (s *scrubber) lines(in string) string {
	var out, block []string
	for _, l := range strings.Split(s.text(in), "\n") {
		if l != "" {
			block = append(block, l)
			continue
		}
		out = append(append(out, s.collapseAbove(block)...), l)
		block = nil
	}
	return strings.Join(append(out, s.collapseAbove(block)...), "\n")
}

// collapseAbove keeps the first of equal items that name the folders above
// the root, marked with how many times it came, and drops the others. An
// item that came once for each folder above says so, which is the same on
// every system; any other count is written out, so a rule that stops
// covering one of the folders changes the golden file.
func (s *scrubber) collapseAbove(items []string) []string {
	count := map[string]int{}
	for _, it := range items {
		if strings.Contains(it, "<ABOVE>") {
			count[it]++
		}
	}
	var out []string
	kept := map[string]bool{}
	for _, it := range items {
		n, above := count[it]
		switch {
		case !above:
			out = append(out, it)
		case !kept[it]:
			kept[it] = true
			out = append(out, it+"  "+s.aboveTimes(n))
		}
	}
	return out
}

// aboveTimes says how many times an item that names the folders above the
// root came, out of how many folders there are.
func (s *scrubber) aboveTimes(n int) string {
	if n == len(s.above) {
		return "[×each folder above]"
	}
	return fmt.Sprintf("[×%d of the %d folders above]", n, len(s.above))
}

// json returns data as indented JSON with sorted keys, its paths and
// times masked, and the values of the masked keys replaced.
func (s *scrubber) json(t testing.TB, data []byte) []byte {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	return s.marshal(t, s.walk(v, ""))
}

// masked reports whether the value at path (keys joined with dots) is
// masked.
func (s *scrubber) masked(path, key string) bool {
	if s.keys[key] {
		return true
	}
	for k := range s.keys {
		if strings.Contains(k, ".") && pathMatch(k, path) {
			return true
		}
	}
	return false
}

// pathMatch matches a dotted path against a pattern of the same length in
// which * stands for any one key or index.
func pathMatch(pattern, path string) bool {
	ps, xs := strings.Split(pattern, "."), strings.Split(path, ".")
	if len(ps) != len(xs) {
		return false
	}
	for i := range ps {
		if ps[i] != "*" && ps[i] != xs[i] {
			return false
		}
	}
	return true
}

func (s *scrubber) walk(v any, path string) any {
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			if e != nil && s.masked(join(k), k) {
				v[k] = "<" + strings.ToUpper(k) + ">"
				continue
			}
			v[k] = s.walk(e, join(k))
		}
		return v
	case []any:
		for i, e := range v {
			v[i] = s.walk(e, join(fmt.Sprint(i)))
		}
		return v
	case string:
		if p, ok := s.values[v]; ok {
			return p
		}
		if _, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return "<TIME>"
		}
		return s.text(v)
	}
	return v
}

// marshal writes v as indented JSON without escaping <, > and &, so the
// placeholders and the arrows in messages stay readable.
func (s *scrubber) marshal(t testing.TB, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// response renders an HTTP response for a golden file: the status, the
// content type and the body, canonical JSON for a JSON body and masked text
// for any other.
func (s *scrubber) response(t testing.TB, w *httptest.ResponseRecorder) []byte {
	t.Helper()
	ct := w.Header().Get("Content-Type")
	body := w.Body.Bytes()
	if strings.HasPrefix(ct, "application/json") {
		body = s.json(t, body)
	} else {
		body = []byte(s.text(string(body)))
	}
	return fmt.Appendf(nil, "%d %s\nContent-Type: %s\n\n%s", w.Code, http.StatusText(w.Code), ct, body)
}

func TestScrubberJSON(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	s := newScrubber(root).maskKeys("pid", "a.*.node")
	tests := []struct {
		name, in, want string
	}{
		{"keys are sorted", `{"b":1,"a":2}`, "{\n  \"a\": 2,\n  \"b\": 1\n}\n"},
		{"the root is masked", `{"dir":"` + root + `/data"}`, "{\n  \"dir\": \"<ROOT>/data\"\n}\n"},
		{"HOME is masked", `{"dir":"` + filepath.Join(root, "home", ".fridolin") + `"}`, "{\n  \"dir\": \"<HOME>/.fridolin\"\n}\n"},
		{"a time is masked", `{"at":"2026-10-18T03:12:00.123+02:00"}`, "{\n  \"at\": \"<TIME>\"\n}\n"},
		{"a masked key keeps its place", `{"pid":4242,"other":1}`, "{\n  \"other\": 1,\n  \"pid\": \"<PID>\"\n}\n"},
		{"a masked key that is null stays null", `{"pid":null}`, "{\n  \"pid\": null\n}\n"},
		{"a masked path", `{"a":[{"node":"x"}],"node":"y"}`, "{\n  \"a\": [\n    {\n      \"node\": \"<NODE>\"\n    }\n  ],\n  \"node\": \"y\"\n}\n"},
		{"the version as a whole value", `{"version":"` + version.String() + `"}`, "{\n  \"version\": \"<VERSION>\"\n}\n"},
		{"no HTML escaping", `{"m":"a → <b> & c"}`, "{\n  \"m\": \"a → <b> & c\"\n}\n"},
		{"numbers stay as written", `{"n":1.50}`, "{\n  \"n\": 1.50\n}\n"},
		{"version stamps are masked", `{"v":"20261018-090405Z","old":"20261018-110405"}`, "{\n  \"old\": \"<STAMP>\",\n  \"v\": \"<STAMP>\"\n}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(s.json(t, []byte(tc.in))); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestScrubberAbove(t *testing.T) {
	// The folders above the root differ between systems; once masked, the
	// lines naming them are one per block on every system, with a count
	// that shows whether each folder had its line.
	root := fsx.Canonical(t.TempDir())
	s := newScrubber(root)
	var above []string
	for d := filepath.Dir(root); d != "/"; d = filepath.Dir(d) {
		above = append(above, d)
	}
	// rule is a deny of file in each of dirs.
	rule := func(op, file string, dirs []string) []string {
		var out []string
		for _, d := range dirs {
			out = append(out, `(deny `+op+` (literal "`+filepath.Join(d, file)+`"))`)
		}
		return out
	}
	lines := func(groups ...[]string) string { return strings.Join(slices.Concat(groups...), "\n") }
	partial := fmt.Sprintf("[×%d of the %d folders above]", len(above)-1, len(above))
	tests := []struct {
		name, in, want string
	}{
		{"a line for each folder", lines(rule("file-read*", "CLAUDE.md", above), rule("file-write*", "", above)), lines(
			[]string{`(deny file-read* (literal "<ABOVE>/CLAUDE.md"))  [×each folder above]`, `(deny file-write* (literal "<ABOVE>"))  [×each folder above]`},
		)},
		{"interleaved", lines(rule("file-read*", "CLAUDE.md", above[:1]), rule("file-read*", ".mcp.json", above), rule("file-read*", "CLAUDE.md", above[1:])), lines(
			[]string{`(deny file-read* (literal "<ABOVE>/CLAUDE.md"))  [×each folder above]`, `(deny file-read* (literal "<ABOVE>/.mcp.json"))  [×each folder above]`},
		)},
		{"one folder left out", lines(rule("file-read*", "CLAUDE.md", above[:len(above)-1])), lines(
			[]string{`(deny file-read* (literal "<ABOVE>/CLAUDE.md"))  ` + partial},
		)},
		{"other paths stay", lines([]string{`(deny file-write* (literal "/usr/bin/true"))`}, rule("file-read*", "brains/CLAUDE.md", []string{root})), lines(
			[]string{`(deny file-write* (literal "/usr/bin/true"))`, `(deny file-read* (literal "<ROOT>/brains/CLAUDE.md"))`},
		)},
		{"blocks count apart", lines(rule("file-read*", "CLAUDE.md", above), []string{""}, rule("file-read*", "CLAUDE.md", above)), lines(
			[]string{`(deny file-read* (literal "<ABOVE>/CLAUDE.md"))  [×each folder above]`, "", `(deny file-read* (literal "<ABOVE>/CLAUDE.md"))  [×each folder above]`},
		)},
		// A rule for / covers everything, and a deeper folder in its place
		// would change the golden file.
		{"the root of the file system stays", lines([]string{`(deny file-write* (subpath "/"))`}, rule("file-read*", "CLAUDE.md", append(slices.Clone(above), "/")), rule("file-read*", ".claude/CLAUDE.md", []string{"/"})), lines(
			[]string{`(deny file-write* (subpath "/"))`, `(deny file-read* (literal "<ABOVE>/CLAUDE.md"))  [×each folder above]`, `(deny file-read* (literal "/CLAUDE.md"))`, `(deny file-read* (literal "/.claude/CLAUDE.md"))`},
		)},
		{"a deeper folder in its place", lines([]string{`(deny file-write* (subpath "` + above[len(above)-1] + `"))`}), lines(
			[]string{`(deny file-write* (subpath "<ABOVE>"))  ` + fmt.Sprintf("[×1 of the %d folders above]", len(above))},
		)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.lines(tc.in); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestScrubberWithLeavesTheOriginal(t *testing.T) {
	root := fsx.Canonical(t.TempDir())
	// Room to append in place: a copy that shared the array would write
	// its new mask into the original's.
	s := &scrubber{paths: make([]pathMask, 0, 8), keys: map[string]bool{}}
	s.addPath(filepath.Join(root, "a"), "<A>")
	c := s.with("pid")
	c.addPath(filepath.Join(root, "a", "longer"), "<LONGER>")
	path, pid := filepath.Join(root, "a", "longer", "f"), []byte(`{"pid":1}`)
	for _, tc := range []struct {
		name, got, want string
	}{
		{"the original keeps its paths", s.text(path), "<A>/longer/f"},
		{"the copy masks its own path", c.text(path), "<LONGER>/f"},
		{"the original keeps its keys", string(s.json(t, pid)), "{\n  \"pid\": 1\n}\n"},
		{"the copy masks its own key", string(c.json(t, pid)), "{\n  \"pid\": \"<PID>\"\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc\nd\ne\n", "a\nB\nc\nd\ne\n")
	if want := " a\n-b\n+B\n c\n"; got != want {
		t.Errorf("diff = %q, want %q", got, want)
	}
}
