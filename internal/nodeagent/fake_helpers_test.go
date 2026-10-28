package nodeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// syncLog is what the agent logs, for the tests to search.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format+"\n", args...)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// called reports whether a call began with prefix.
func called(calls []fakeCall, prefix ...string) bool {
	return slices.ContainsFunc(calls, func(c fakeCall) bool { return len(c.Args) >= len(prefix) && slices.Equal(c.Args[:len(prefix)], prefix) })
}

// fakeCall is one sbx call the fake recorded.
type fakeCall struct {
	Args         []string `json:"args"`
	TokenEnv     bool     `json:"token_env"`
	TelemetryOff bool     `json:"telemetry_off"`
}

// fakeCalls are the sbx calls the fake recorded in fake's calls.log.
func fakeCalls(t *testing.T, fake string) []fakeCall {
	t.Helper()
	f, err := os.Open(filepath.Join(fake, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []fakeCall
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 4<<20)
	for sc.Scan() {
		var c fakeCall
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}
