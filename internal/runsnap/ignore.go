package runsnap

import (
	"bufio"
	"bytes"
	"path"
	"strings"
)

// The .gitignore files of a folder leave what they name out of its
// records, as git leaves it out of a commit: build output, caches,
// dependencies. A run's rules are those its folder had as it started
// (ignoreRules is filled by the first walk and only read by the second),
// so a run cannot hide what it changes by writing a .gitignore.
//
// The syntax is git's, less what a brain folder has no use for: blank
// lines and "#" comments are skipped, "!" negates, a trailing "/"
// matches folders only, a "/" at the start or inside anchors the pattern
// at the .gitignore's folder (otherwise it matches a name at any depth),
// "*", "?" and "[…]" match within a name and "**" a whole segment that
// stands for any number of them. The last rule that matches wins, those
// of a deeper .gitignore over a shallower one, and nothing inside an
// ignored folder is looked at, so "!" cannot bring it back.

// Bounds on what one .gitignore may hold: a brain writes these files,
// and the bridge reads them outside its sandbox.
const (
	maxIgnoreBytes   = 64 << 10
	maxIgnoreRules   = 1000
	maxIgnoreStars   = 4
	gitignoreName    = ".gitignore"
	maxIgnoreSegment = 64
)

// ignoreRule is one line of a .gitignore.
type ignoreRule struct {
	segs    []string
	negate  bool
	dirOnly bool
}

// ignoreRules are the rules of a root's .gitignore files, by the folder
// each lies in ("" for the root).
type ignoreRules map[string][]ignoreRule

// parseIgnore reads the rules of one .gitignore.
func parseIgnore(data []byte) []ignoreRule {
	if len(data) > maxIgnoreBytes {
		data = data[:maxIgnoreBytes]
	}
	var rules []ignoreRule
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), maxIgnoreBytes)
	for sc.Scan() && len(rules) < maxIgnoreRules {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r ignoreRule
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		anchored := strings.Contains(line, "/")
		line = strings.Trim(line, "/")
		if line == "" {
			continue
		}
		stars := 0
		bad := false
		for s := range strings.SplitSeq(line, "/") {
			if s == "" {
				continue
			}
			if s == "**" {
				stars++
			} else if _, err := path.Match(s, ""); err != nil {
				bad = true
			}
			r.segs = append(r.segs, s)
		}
		if bad || stars > maxIgnoreStars || len(r.segs) > maxIgnoreSegment {
			continue
		}
		if !anchored {
			r.segs = append([]string{"**"}, r.segs...)
		}
		rules = append(rules, r)
	}
	return rules
}

// ignored reports whether rel, a slash-separated path in the root (a
// folder when dir), is left out by the rules.
func (ig ignoreRules) ignored(rel string, dir bool) bool {
	if len(ig) == 0 {
		return false
	}
	segs := strings.Split(rel, "/")
	out := false
	// From the root down: a deeper .gitignore decides after a shallower.
	for depth := 0; depth < len(segs); depth++ {
		folder := strings.Join(segs[:depth], "/")
		rules, ok := ig[folder]
		if !ok {
			continue
		}
		below := segs[depth:]
		for _, r := range rules {
			if r.dirOnly && !dir {
				continue
			}
			if matchSegments(r.segs, below) {
				out = !r.negate
			}
		}
	}
	return out
}

// matchSegments matches a whole path against a pattern, "**" standing for
// any number of segments.
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], segs[0])
	return err == nil && ok && matchSegments(pat[1:], segs[1:])
}
