package backup

import (
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// A brain's backup.exclude is written in rclone's filter syntax, relative
// to the brain folder (FilterLines). The vault's restic snapshots and its
// hot root must leave out the same files the Drive backup does, so the
// patterns are translated here once, into path patterns anchored at the
// brain folder, as restic matches them: segments separated by "/", each
// matched by path.Match, "**" a whole segment that stands for any number
// of segments (none too), and a pattern that matches a folder leaves out
// everything in it.
//
// The translation:
//
//   - "/work/" (leading "/") is anchored at the brain folder: "work";
//   - "*.log" (no leading "/") matches at any depth: "**/*.log";
//   - a trailing "/" (rclone's "dir/**") becomes the folder itself, so
//     restic does not even walk into it;
//   - "**" inside a segment, which crosses folders in rclone
//     ("/exports/**.zip"), becomes both "exports/*.zip" and
//     "exports/*/**/*.zip";
//   - "{a,b}" becomes one pattern per alternative.
//
// Where the two cannot mean the same thing, the translation leaves out
// more, never less: a pattern without a trailing "/" also matches a folder
// of that name (rclone matches it against files only), the folder of
// "dir/" is left out along with its files, and a pattern it cannot
// translate leaves out the whole brain folder.

// ExcludePatterns are the patterns, relative to the brain folder, that
// leave out what the Drive backup leaves out of it: the secret files
// unless includeSecrets, then the brain's own excludes.
func ExcludePatterns(excludes []string, includeSecrets bool) []string {
	var all []string
	if !includeSecrets {
		all = append(all, SecretExcludes...)
	}
	all = append(all, excludes...)
	var out []string
	for _, x := range all {
		out = append(out, translateExclude(x)...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ResticExcludes are patterns (from ExcludePatterns) as restic --exclude
// values for the brain folder dir: absolute, so that a pattern of one
// brain never leaves out a file of another brain or of the data dir.
// Excludes given as command-line values, unlike the lines of an
// --exclude-file, are not run through os.ExpandEnv, so a "$" in a
// folder's name stays itself.
func ResticExcludes(dir string, patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p == wholeFolder {
			return []string{ResticPath(dir)}
		}
		out = append(out, ResticPath(dir)+string(filepath.Separator)+filepath.FromSlash(p))
	}
	return out
}

// ResticPath is dir as a restic --exclude pattern that matches dir itself
// (and with it all of it): its pattern characters escaped. restic matches
// each segment with filepath.Match, which on Windows takes no escapes; a
// folder with "*", "?" or "[" in its name is not one Windows allows anyway.
func ResticPath(dir string) string {
	dir = filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		return dir
	}
	var sb strings.Builder
	for _, r := range dir {
		if strings.ContainsRune(`*?[]\`, r) {
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// Excluded reports whether rel, a slash-separated path relative to the
// brain folder, is left out by patterns (from ExcludePatterns): it or a
// folder above it matches one, as restic decides it.
func Excluded(rel string, patterns []string) bool {
	if slices.Contains(patterns, wholeFolder) {
		return true
	}
	rel = strings.Trim(path.Clean("/"+rel), "/")
	if rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	for _, p := range patterns {
		ps := strings.Split(p, "/")
		for n := 1; n <= len(segs); n++ {
			if matchSegments(ps, segs[:n]) {
				return true
			}
		}
	}
	return false
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

// maxExpansions bounds the patterns one exclude becomes ("{a,b}" and "**"
// inside a segment each multiply them).
const maxExpansions = 64

// wholeFolder is the pattern that leaves out all of a brain folder.
const wholeFolder = "**"

// translateExclude turns one rclone exclude into patterns relative to the
// brain folder. What FilterLines would drop yields none. A pattern this
// cannot translate (rclone's "{{regexp}}", a bad character class, one that
// grows past maxExpansions) yields wholeFolder: leaving out the whole
// folder is wrong in the safe direction, and uploading what the person
// excluded is not.
func translateExclude(x string) []string {
	x = strings.TrimSpace(x)
	if x == "" || strings.ContainsAny(x, "\r\n") || strings.HasPrefix(x, "+") {
		return nil
	}
	x = strings.TrimPrefix(x, "- ")
	if strings.Contains(x, "{{") {
		return []string{wholeFolder}
	}
	anchored := strings.HasPrefix(x, "/")
	x = strings.Trim(x, "/")
	// rclone's "dir/**" is FilterLines' form of "dir/": the folder.
	for strings.HasSuffix(x, "/**") {
		x = strings.TrimSuffix(x, "/**")
	}
	switch {
	case x == "**":
		return []string{wholeFolder}
	case x == "":
		// "/" or "/**": rclone's "/**" leaves out everything.
		if anchored {
			return []string{wholeFolder}
		}
		return nil
	}
	var out []string
	for _, alt := range expandBraces(x) {
		var segs []string
		for s := range strings.SplitSeq(alt, "/") {
			if s != "" {
				segs = append(segs, s)
			}
		}
		if !anchored {
			segs = append([]string{"**"}, segs...)
		}
		inner := 0
		for _, s := range segs {
			if s != "**" {
				inner += strings.Count(s, "**")
			}
		}
		if inner > 4 {
			return []string{wholeFolder}
		}
		for _, e := range expandDoubleStars(segs) {
			e = collapseStars(e)
			for _, s := range e {
				if _, err := path.Match(s, ""); err != nil {
					return []string{wholeFolder}
				}
			}
			out = append(out, strings.Join(e, "/"))
		}
		if len(out) > maxExpansions {
			return []string{wholeFolder}
		}
	}
	return out
}

// expandBraces expands rclone's "{a,b}" (not nested) into one string per
// alternative.
func expandBraces(s string) []string {
	open := strings.IndexByte(s, '{')
	if open < 0 || (open > 0 && s[open-1] == '\\') {
		return []string{s}
	}
	end := strings.IndexByte(s[open:], '}')
	if end < 0 {
		return []string{s}
	}
	end += open
	var out []string
	for alt := range strings.SplitSeq(s[open+1:end], ",") {
		for _, rest := range expandBraces(s[end+1:]) {
			out = append(out, s[:open]+alt+rest)
			if len(out) > maxExpansions {
				return out
			}
		}
	}
	return out
}

// expandDoubleStars turns a segment with "**" among other characters,
// which in rclone crosses folders, into what restic understands: "a**b"
// is "a*b" within one segment, or "a*", then any number of segments, then
// "*b".
func expandDoubleStars(segs []string) [][]string {
	for i, s := range segs {
		j := strings.Index(s, "**")
		if s == "**" || j < 0 {
			continue
		}
		same := slices.Clone(segs)
		same[i] = s[:j] + "*" + s[j+2:]
		cross := append(append(slices.Clone(segs[:i]), s[:j]+"*", "**", "*"+s[j+2:]), segs[i+1:]...)
		return append(expandDoubleStars(same), expandDoubleStars(cross)...)
	}
	return [][]string{segs}
}

// collapseStars drops a "**" segment that follows another: two mean what
// one does.
func collapseStars(segs []string) []string {
	out := segs[:0:0]
	for _, s := range segs {
		if s == "**" && len(out) > 0 && out[len(out)-1] == "**" {
			continue
		}
		out = append(out, s)
	}
	return out
}
