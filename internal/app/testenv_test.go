package app

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
)

// testEnv is the environment of what a test makes: the bridges
// (Options.Env), brain managers and configs. It is the process's
// environment with what the test set (setenv) on top, so that tests that
// run side by side each give their bridges a HOME and fake CLIs of their
// own, where t.Setenv would change the one environment of the process,
// which Go refuses in a parallel test. The process's part is read when
// asked: a serial test that still changes it with t.Setenv reaches the
// bridges it makes, as it did.
type testEnv struct {
	mu sync.Mutex
	// set holds the variables set on top of the process's, and unset
	// those taken away.
	set   map[string]string
	unset map[string]bool
}

func (e *testEnv) LookupEnv(name string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.set[name]; ok {
		return v, true
	}
	if e.unset[name] {
		return "", false
	}
	return os.LookupEnv(name)
}

func (e *testEnv) Environ() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, ok := e.set[name]; !ok && !e.unset[name] {
			out = append(out, kv)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(e.set)) {
		out = append(out, name+"="+e.set[name])
	}
	return out
}

// change sets name to value, or takes it away when unset, and returns a
// function that puts back what was there.
func (e *testEnv) change(name, value string, unset bool) (restore func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	old, wasSet := e.set[name]
	wasUnset := e.unset[name]
	if unset {
		delete(e.set, name)
		e.unset[name] = true
	} else {
		e.set[name] = value
		delete(e.unset, name)
	}
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.set, name)
		delete(e.unset, name)
		if wasSet {
			e.set[name] = old
		}
		if wasUnset {
			e.unset[name] = true
		}
	}
}

// perTest keeps a T for each running test, by its name. A test gets its
// own the first time it asks: a copy of the closest enclosing test's, or a
// fresh one, so that a subtest starts from what its parent set while what
// it changes stays its own (subtests may run in parallel, each making its
// own bridge).
type perTest[T any] struct {
	mu    sync.Mutex
	m     map[string]*T
	fresh func() *T
	copy  func(*T) *T
}

func (r *perTest[T]) of(t testing.TB) *T {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Name()
	if v, ok := r.m[name]; ok {
		return v
	}
	var v *T
	for p := name; strings.Contains(p, "/") && v == nil; {
		p = p[:strings.LastIndex(p, "/")]
		if parent, ok := r.m[p]; ok {
			v = r.copy(parent)
		}
	}
	if v == nil {
		v = r.fresh()
	}
	if r.m == nil {
		r.m = map[string]*T{}
	}
	r.m[name] = v
	t.Cleanup(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.m, name)
	})
	return v
}

var testEnvs = perTest[testEnv]{
	fresh: func() *testEnv { return &testEnv{set: map[string]string{}, unset: map[string]bool{}} },
	copy: func(parent *testEnv) *testEnv {
		parent.mu.Lock()
		defer parent.mu.Unlock()
		return &testEnv{set: maps.Clone(parent.set), unset: maps.Clone(parent.unset)}
	},
}

// envOf is t's environment (see perTest). A bridge made in a parent does
// not see what a subtest sets; set that in the parent's (setenv with the
// parent's t, or testBridge.setenv).
func envOf(t testing.TB) *testEnv {
	t.Helper()
	return testEnvs.of(t)
}

var testOptions = perTest[Options]{
	fresh: func() *Options {
		w := defaultWaits()
		// FrIDE's news goes out once a moment has gathered it.
		w.frideGather = 50 * time.Millisecond
		return &Options{waits: &w}
	},
	copy: func(parent *Options) *Options {
		o, w := *parent, *parent.waits
		o.waits = &w
		return &o
	},
}

// optionsOf are the Options of the bridges and brain managers t makes,
// besides their config and environment: the waits a test shortens and the
// hooks it watches (see perTest). Set them before making the bridge, which
// takes a copy.
func optionsOf(t testing.TB) *Options {
	t.Helper()
	return testOptions.of(t)
}

// bridgeOptions are the Options of a dev-mode bridge on cfgs that t makes:
// optionsOf(t), with t's environment.
func bridgeOptions(t testing.TB, cfgs *config.Store) Options {
	t.Helper()
	o := *optionsOf(t)
	w := *o.waits
	o.waits = &w
	o.Config, o.NoWhatsApp, o.Env = cfgs, true, envOf(t)
	return o
}

// setenv sets name to value in t's environment until t ends, as t.Setenv
// does in the process's; unlike it, in a parallel test too.
func setenv(t testing.TB, name, value string) {
	t.Helper()
	t.Cleanup(envOf(t).change(name, value, false))
}

// unsetenv takes name away from t's environment until t ends.
func unsetenv(t testing.TB, name string) {
	t.Helper()
	t.Cleanup(envOf(t).change(name, "", true))
}

// homeOf is HOME in t's environment: ~ in the configs t loads, and what the
// bridges it makes run their programs with.
func homeOf(t testing.TB) string {
	t.Helper()
	return getenv(envOf(t), "HOME")
}

// configOptionsOf are ConfigOptions for t: the config resolves ~ against
// t's HOME and reads t's environment.
func configOptionsOf(t testing.TB) config.Options {
	t.Helper()
	env := envOf(t)
	return config.Options{Paths: ConfigPathsFor(homeOf(t)), GOOS: ConfigGOOS(), Getenv: func(name string) string { return getenv(env, name) }, CheckBrains: CheckSet}
}

// loadConfig is LoadConfig in t's environment.
func loadConfig(t testing.TB, path string) (*config.Settings, error) {
	t.Helper()
	return config.Load(path, configOptionsOf(t))
}

// openConfigStore is OpenConfigStore in t's environment.
func openConfigStore(t testing.TB, path string) (*config.Store, error) {
	t.Helper()
	return config.Open(path, configOptionsOf(t))
}

// TestCommandsResolveTheConfigAgainstTheProcessHome: the binary opens its
// config with the process's own HOME (LoadConfig, OpenConfigStore), which
// tests replace with their own (loadConfig, openConfigStore).
func TestCommandsResolveTheConfigAgainstTheProcessHome(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `{"data_dir": "`+filepath.Join(t.TempDir(), "data")+`"}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Home != HomeDir() {
		t.Errorf("LoadConfig: home %q, want the process's %q", cfg.Home, HomeDir())
	}
	cfgs, err := OpenConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfgs.Boot().Home != HomeDir() {
		t.Errorf("OpenConfigStore: home %q, want the process's %q", cfgs.Boot().Home, HomeDir())
	}
}

// TestBridgesReadTheirOwnEnvironment: what a test sets in its environment
// reaches the bridge it makes, and the programs the bridge starts, but not
// the process's environment or another test's bridge.
func TestBridgesReadTheirOwnEnvironment(t *testing.T) {
	t.Parallel()
	const name = "FRIDOLIN_TEST_OWN_ENV"
	setenv(t, name, "mine")
	tb := newTestBridge(t, "")
	if got := getenv(tb.env, name); got != "mine" {
		t.Errorf("the bridge reads %s=%q, want mine", name, got)
	}
	if _, ok := os.LookupEnv(name); ok {
		t.Errorf("%s reached the process's environment", name)
	}
	if env := childEnv(tb.env, anyChild, []string{name}); !slices.Contains(env, name+"=mine") {
		t.Errorf("a program the bridge starts, passed %s through, got %q", name, env)
	}
	t.Run("a subtest", func(t *testing.T) {
		setenv(t, name, "the subtest's")
		if got := getenv(envOf(t), name); got != "the subtest's" {
			t.Errorf("the subtest reads %q", got)
		}
		if got := getenv(tb.env, name); got != "mine" {
			t.Errorf("the parent's bridge reads %q, want what the parent set", got)
		}
	})
	if got := getenv(envOf(t), name); got != "mine" {
		t.Errorf("after the subtest: %q, want mine again", got)
	}
	other := &testEnv{set: map[string]string{}, unset: map[string]bool{}}
	if _, ok := other.LookupEnv(name); ok {
		t.Errorf("another environment sees %s", name)
	}
}
