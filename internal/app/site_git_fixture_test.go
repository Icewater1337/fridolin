package app

import "strings"

// gitEnv is env without the GIT_ variables, so that git works on the
// repository in its working directory and nothing else. The bridge's own
// environment may carry them: run from a git hook or `git rebase -x`, it
// inherits GIT_DIR and GIT_INDEX_FILE, and the site's commits and pushes
// would then go to that repository instead of the site's.
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}
