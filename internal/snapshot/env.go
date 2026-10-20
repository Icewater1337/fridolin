package snapshot

import (
	"bytes"
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
)

// stderrLimit is how much of a child's error output an error keeps.
const stderrLimit = 2048

// secretNameParts mark an environment variable whose value is a secret:
// RESTIC_PASSWORD, B2_ACCOUNT_KEY, B2_ACCOUNT_ID, LITESTREAM_SECRET_ACCESS_KEY,
// AWS_SESSION_TOKEN and the like. An account id is not a password, but
// together with a leaked key it names whose bucket to try it on.
var secretNameParts = []string{"PASSWORD", "KEY", "SECRET", "TOKEN", "ACCOUNT"}

// secretValues returns the values of env's secret variables and extra, the
// longest first, so that a secret that contains another is blanked whole.
func secretValues(env []string, extra ...string) []string {
	var out []string
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok || val == "" {
			continue
		}
		upper := strings.ToUpper(name)
		for _, part := range secretNameParts {
			if strings.Contains(upper, part) {
				out = append(out, val)
				break
			}
		}
	}
	for _, s := range extra {
		if s != "" {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	return slices.Compact(out)
}

// redact blanks secrets out of s and keeps its last stderrLimit bytes.
func redact(s string, secrets []string) string {
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, "***")
	}
	s = strings.TrimSpace(s)
	if len(s) > stderrLimit {
		s = "…" + s[len(s)-stderrLimit:]
	}
	return s
}

// urlPassword returns the password in a repository location such as
// "rest:https://user:pass@host/", so that it is blanked out of errors as
// well; restic prints the location in many of its messages.
func urlPassword(repo string) string {
	_, rest, _ := strings.Cut(repo, ":")
	for _, candidate := range []string{repo, rest} {
		u, err := url.Parse(candidate)
		if err != nil || u.User == nil {
			continue
		}
		if pw, ok := u.User.Password(); ok {
			return pw
		}
	}
	return ""
}

// envValue returns the value of name in env; the last setting wins, as it
// does for a child process.
func envValue(env []string, name string) (string, bool) {
	val, found := "", false
	for _, kv := range env {
		if n, v, ok := strings.Cut(kv, "="); ok && n == name {
			val, found = v, true
		}
	}
	return val, found
}

// fromNames are the variables of the source repository that restic reads
// under another name when it is the source of a copy or of chunker
// parameters: its own RESTIC_PASSWORD would be the destination's.
var fromNames = map[string]string{
	"RESTIC_PASSWORD":         "RESTIC_FROM_PASSWORD",
	"RESTIC_PASSWORD_FILE":    "RESTIC_FROM_PASSWORD_FILE",
	"RESTIC_PASSWORD_COMMAND": "RESTIC_FROM_PASSWORD_COMMAND",
	"RESTIC_KEY_HINT":         "RESTIC_FROM_KEY_HINT",
	"RESTIC_REPOSITORY":       "RESTIC_FROM_REPOSITORY",
	"RESTIC_REPOSITORY_FILE":  "RESTIC_FROM_REPOSITORY_FILE",
}

// mergeEnv returns the environment for a restic run in dst's repository
// that also reads src's: dst's variables, src's password under its
// RESTIC_FROM_ name, and src's backend variables. Backend variables are
// read by restic for both repositories alike, so two that differ (two B2
// accounts, two rclone configs) cannot be given to one run; that is an
// error naming the variable, never its values.
func mergeEnv(dst, src []string) ([]string, error) {
	out := append([]string{}, dst...)
	for _, kv := range src {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if from, renamed := fromNames[name]; renamed {
			name = from
		}
		have, set := envValue(out, name)
		switch {
		case !set:
			out = append(out, name+"="+val)
		case have != val:
			return nil, fmt.Errorf("snapshot: both repositories set %s to different values; one restic run cannot use both", name)
		}
	}
	return out, nil
}

// mergeOptions returns dst's -o options followed by those of src that dst
// does not have. An option both set to different values would apply to
// both repositories, so it is an error like a clashing variable.
func mergeOptions(dst, src []string) ([]string, error) {
	out := append([]string{}, dst...)
	for _, o := range src {
		key, _, _ := strings.Cut(o, "=")
		clash := false
		present := slices.ContainsFunc(out, func(have string) bool {
			k, _, _ := strings.Cut(have, "=")
			if k != key {
				return false
			}
			clash = clash || have != o
			return true
		})
		if clash {
			return nil, fmt.Errorf("snapshot: both repositories set the option %s to different values", key)
		}
		if !present {
			out = append(out, o)
		}
	}
	return out, nil
}

// tailBuffer is an io.Writer that keeps only the last limit bytes written.
// When it drops output it also drops the line that was cut, so that a
// secret is never kept as a fragment that redact would not recognise.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	cut   bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
		b.cut = true
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf
	if b.cut {
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		} else {
			s = nil
		}
	}
	return string(s)
}
