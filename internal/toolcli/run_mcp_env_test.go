package toolcli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Antigravity starts the run's MCP server with no --run: the run's
// environment names it.
func TestRunMCPTakesTheRunFromTheEnvironment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, runID, want string
	}{
		{"neither", "", "usage: fridolin mcp --run ID"},
		{"FRIDOLIN_RUN_ID", "r123", "FRIDOLIN_API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			io := IO{Stdout: &bytes.Buffer{}, Stderr: &stderr, Getenv: func(k string) string {
				if k == "FRIDOLIN_RUN_ID" {
					return tc.runID
				}
				return ""
			}}
			if code := RunMCP(context.Background(), nil, strings.NewReader(""), io); code != 2 || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d, stderr %q; want 2 and %q", code, stderr.String(), tc.want)
			}
		})
	}
}
