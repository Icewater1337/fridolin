package app

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/history"
	"github.com/Icewater1337/fridolin/internal/manifest"
	prompttext "github.com/Icewater1337/fridolin/internal/prompt"
)

type testRunnerFunc func(context.Context, brain.RunSpec, claudecli.Callbacks) claudecli.Result

func (f testRunnerFunc) Run(c context.Context, s brain.RunSpec, h claudecli.Callbacks) claudecli.Result {
	return f(c, s, h)
}

// capturedPrompt observes the runtime's Runner input without changing the
// integration test's live queue/history. Only its three prompt inputs are
// copied into a fresh scratch folder. Confinement tests live beside the runtime
// and use the original pinned root directly.
func capturedPrompt(t *testing.T, br *brain.Brain, m brain.Inbound) string {
	t.Helper()
	dir := t.TempDir()
	manifestValue := br.Manifest()
	promptFile := cmp.Or(manifestValue.PromptFile, manifest.DefaultPromptFile)
	for _, name := range []string{promptFile, prompttext.IdentityMapPath, history.FileName} {
		data, err := os.ReadFile(filepath.Join(br.Dir(), name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		to := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(to), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan string, 1)
	mgr := brain.NewManager(brain.Options{Input: commandBrainInputRules(nil), ClaimMedia: func(string, string, []string) map[string]string { return nil }, Runner: testRunnerFunc(func(_ context.Context, s brain.RunSpec, _ claudecli.Callbacks) claudecli.Result {
		ready <- s.Prompt
		return claudecli.Result{}
	}), ShutdownGrace: func() time.Duration { return 0 }})
	defer mgr.Stop()
	if failures, err := mgr.Apply([]manifest.Spec{{Dir: dir, PromptFile: promptFile, Manifest: manifestValue}}, brain.Globals{Defaults: brain.Settings{MaxPending: 1}}, brain.ApplyOptions{}); err != nil || len(failures) > 0 {
		t.Fatalf("prompt runtime: %v %v", failures, err)
	}
	m.Done = nil
	m.Silent = false
	if _, _, err := mgr.Get(br.ID()).Submit(m); err != nil {
		t.Fatal(err)
	}
	select {
	case prompt := <-ready:
		return prompt
	case <-time.After(10 * time.Second):
		t.Fatal("prompt did not reach Runner")
		return ""
	}
}
