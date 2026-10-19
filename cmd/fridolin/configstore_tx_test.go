package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/jsonx"
	"github.com/Icewater1337/fridolin/internal/manifest"
	"github.com/Icewater1337/fridolin/internal/testutil"
)

// openTxStore opens a config store on a config.json with body, in a temp
// HOME of its own.
func openTxStore(t *testing.T, body string) *config.Store {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cs, err := openConfigStore(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// A panic in an update is an error, and leaves the store as it was and
// free for the next update: net/http would recover the handler's panic,
// and a lock left held would block every later save.
func TestConfigUpdatePanicIsAnError(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config) error
	}{
		{"in mutate", func(c *config.Config) error {
			c.LogLevel = "debug"
			panic("boom")
		}},
		{"out of range", func(c *config.Config) error {
			c.LogLevel = c.AllowedNumbers[len(c.AllowedNumbers)]
			return nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := openTxStore(t, `{"allowed_numbers":["15551234567"]}`)
			before, err := os.ReadFile(cs.Path())
			if err != nil {
				t.Fatal(err)
			}
			etag := cs.ETag()
			done := make(chan error, 1)
			go func() {
				_, err := cs.Update("", tc.mutate)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "the config update failed") {
					t.Errorf("Update with a panic: err = %v, want the panic as an error", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Update with a panic did not return")
			}
			if cs.ETag() != etag || cs.Get().LogLevel != "info" {
				t.Errorf("after the panic: etag %q (was %q), log_level %q: the update was applied", cs.ETag(), etag, cs.Get().LogLevel)
			}
			if after, _ := os.ReadFile(cs.Path()); string(after) != string(before) {
				t.Errorf("config.json changed:\n%s", after)
			}

			next := make(chan error, 1)
			go func() {
				_, err := cs.Update("", func(c *config.Config) error { c.LogLevel = "warn"; return nil })
				next <- err
			}()
			select {
			case err := <-next:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the update after the panic blocks: the lock was left held")
			}
			if got := cs.Get().LogLevel; got != "warn" {
				t.Errorf("log_level = %q after the next update, want warn", got)
			}
			_ = cs.FileCopy() // and so does reading the file layer
		})
	}
}

// GET /api/config sends the etag of the config it sends, also while other
// updates commit: with an etag of a later write, a save of the page's
// stale config would pass the conflict check and undo that write.
func TestGetConfigSendsTheETagOfItsConfig(t *testing.T) {
	tb := newTestBridge(t, "")
	// The etag is the hash of config.json, which is the file layer GET
	// sends, indented; without brains, brain_dirs is null in both.
	check := func() error {
		w := tb.do("GET", "/api/config", nil)
		var v struct {
			ETag   string          `json:"etag"`
			Config json.RawMessage `json:"config"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			return fmt.Errorf("GET /api/config: %d %s", w.Code, w.Body.String())
		}
		var c config.Config
		if err := json.Unmarshal(v.Config, &c); err != nil {
			return err
		}
		data, err := json.MarshalIndent(&c, "", "  ")
		if err != nil {
			return err
		}
		if got := configETag(append(data, '\n')); got != v.ETag {
			return fmt.Errorf("etag %s with a config whose hash is %s (_comment %q)", v.ETag, got, c.Comment)
		}
		return nil
	}
	if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Comment = "start"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("without other updates: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := tb.cfgs.Update("", func(c *config.Config) error { c.Comment = fmt.Sprintf("w%d-%d", w, i); return nil }); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for n := 0; n < 2000 && time.Now().Before(deadline); n++ {
		if err := check(); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// The subscribers hear of the updates in the order they were made, one at a
// time: one that applies what it hears, as the brains do, must not end on
// a configuration a later update replaced because the later notification
// overtook the earlier one.
func TestConfigSubscribersHearUpdatesInOrder(t *testing.T) {
	cs := openTxStore(t, `{}`)
	var mu sync.Mutex
	var heard []string
	inA := make(chan struct{})
	heardB := make(chan struct{})
	cs.Subscribe(func(_, next *config.Config) {
		if next.Comment == "a" {
			close(inA)
			// B commits meanwhile; its notification must wait for this one.
			testutil.WaitFor(t, 5*time.Second, "update b to commit", func() bool { return cs.Get().Comment == "b" })
			select {
			case <-heardB:
			case <-time.After(200 * time.Millisecond):
			}
		}
		mu.Lock()
		heard = append(heard, next.Comment)
		mu.Unlock()
		if next.Comment == "b" {
			close(heardB)
		}
	})
	done := make(chan error, 2)
	go func() {
		_, err := cs.Update("", func(c *config.Config) error { c.Comment = "a"; return nil })
		done <- err
	}()
	<-inA
	go func() {
		_, err := cs.Update("", func(c *config.Config) error { c.Comment = "b"; return nil })
		done <- err
	}()
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(heard, ",") != "a,b" {
		t.Errorf("the subscriber heard of the updates as %v, want [a b]", heard)
	}
}

// A subscriber that panics is logged; the update stays saved, the other
// subscribers hear of it, and the next update is not blocked.
func TestConfigSubscriberPanicIsLogged(t *testing.T) {
	cs := openTxStore(t, `{}`)
	logs := captureLog(t)
	cs.Subscribe(func(_, next *config.Config) {
		if next.Comment == "boom" {
			panic("subscriber")
		}
	})
	var heard []string
	cs.Subscribe(func(_, next *config.Config) { heard = append(heard, next.Comment) })
	if _, err := cs.Update("", func(c *config.Config) error { c.Comment = "boom"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *config.Config) error { c.Comment = "next"; return nil }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(heard, ",") != "boom,next" || cs.Get().Comment != "next" {
		t.Errorf("heard %v, config %q", heard, cs.Get().Comment)
	}
	if !logContains(logs, "a subscriber panicked on the saved update: subscriber") {
		t.Errorf("the panic was not logged: %q", logs())
	}
}

// After updates that run at once, the brains run on the configuration the
// store has, not on one an update replaced.
func TestBrainsRunOnTheLatestConfig(t *testing.T) {
	h := newHarness(t, "")
	h.AddBrain(manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := manifest.Manifest{ID: "main", Name: fmt.Sprintf("Main %d-%d", round, i), Routing: manifest.Routing{HandlesDMs: true}}
				if w := h.Do("PUT", "/api/brains/main", map[string]any{"manifest": m}); w.Code != 200 {
					t.Errorf("PUT /api/brains/main: %d %s", w.Code, w.Body.String())
				}
			}()
		}
		wg.Wait()
		var views []httpapi.BrainView
		if w := h.Do("GET", "/api/brains", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &views) != nil {
			t.Fatalf("GET /api/brains: %d %s", w.Code, w.Body.String())
		}
		if len(views) != 1 || views[0].Runtime == nil {
			t.Fatalf("GET /api/brains = %+v", views)
		}
		approved := h.Approved()["main"].Name
		if views[0].Name != approved || views[0].Runtime.Name != approved {
			t.Fatalf("round %d: config.json approves %q, the config %q, but the brain runs as %q", round, approved, views[0].Name, views[0].Runtime.Name)
		}
	}
}

// The bridge applies the configuration the store has when it hears of an
// update, not the one the notification carries: a notification that comes
// after a later update's leaves the brains on the later one.
func TestBridgeAppliesTheLatestConfigOnALateNotification(t *testing.T) {
	tb := newTestBridge(t, "")
	tb.addFolderBrain(t, manifest.Manifest{ID: "main", Name: "Main", Routing: manifest.Routing{HandlesDMs: true}})
	rename := func(name string) config.Snapshot {
		t.Helper()
		if err := updateManifest(tb.cfgs, "main", func(m *manifest.Manifest) error { m.Name = name; return nil }); err != nil {
			t.Fatal(err)
		}
		return tb.cfgs.Snapshot()
	}
	before := tb.cfgs.Snapshot()
	first := rename("First")
	rename("Second")
	tb.configChanged(before.Effective, first.Effective) // the first update's, late
	if got := tb.brainMgr.Get("main").Snapshot().Name; got != "Second" {
		t.Errorf("after a late notification the brain runs as %q, want Second", got)
	}
}

// PUT /api/config answers with the etag of the config.json it wrote, also
// when another update has written one since: with that one's etag, the
// page would take the other write for its own and save over it.
func TestPutConfigSendsItsOwnETag(t *testing.T) {
	tb := newTestBridge(t, "")
	var once sync.Once
	var putETag string
	later := make(chan error, 1)
	tb.cfgs.Subscribe(func(_, next *config.Config) {
		if next.LogLevel != "debug" {
			return
		}
		// Heard of the PUT's update: another one commits before the PUT
		// answers. It waits to tell its subscribers until this returns.
		once.Do(func() {
			putETag = tb.cfgs.ETag()
			go func() {
				_, err := tb.cfgs.Update("", func(c *config.Config) error { c.Comment = "later"; return nil })
				later <- err
			}()
			testutil.WaitFor(t, 5*time.Second, "the later update", func() bool { return tb.cfgs.Get().Comment == "later" })
		})
	})
	w := tb.editConfig(t, func(c map[string]any) { c["log_level"] = "debug" })
	if err := <-later; err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ETag string `json:"etag"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
		t.Fatalf("PUT /api/config: %d %s", w.Code, w.Body.String())
	}
	if resp.ETag != putETag || resp.ETag == tb.cfgs.ETag() {
		t.Errorf("PUT answered etag %s; it wrote %s, the later update %s", resp.ETag, putETag, tb.cfgs.ETag())
	}
	// Saving again with it is a conflict: the page has not seen the later
	// write.
	if w := tb.do("PUT", "/api/config", map[string]any{"etag": resp.ETag, "config": tb.cfgs.FileCopy()}); w.Code != 409 {
		t.Errorf("save with the PUT's etag after a later write: %d %s, want 409", w.Code, w.Body.String())
	}
}

// editByHand rewrites config.json the way an editor would, with edit
// applied to its keys.
func editByHand(t *testing.T, path string, edit func(c map[string]any)) {
	t.Helper()
	var c map[string]any
	if err := json.Unmarshal([]byte(readFile(t, path)), &c); err != nil {
		t.Fatal(err)
	}
	edit(c)
	data, err := json.MarshalIndent(c, "", "\t")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A config.json changed by hand while the bridge runs is not written over
// by the next save. A save of the whole config made from the one before
// is a conflict; once reloaded, the page shows the hand edit and saves.
func TestHandEditThenSaveWithTheOldETagConflicts(t *testing.T) {
	tb := newTestBridge(t, "")
	var page struct {
		ETag   string         `json:"etag"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(tb.do("GET", "/api/config", nil).Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	logs := captureLog(t)
	editByHand(t, tb.cfgs.Path(), func(c map[string]any) { c["max_chunk_size"] = 1234 })
	edited := readFile(t, tb.cfgs.Path())

	page.Config["log_level"] = "debug"
	w := tb.do("PUT", "/api/config", map[string]any{"etag": page.ETag, "config": page.Config})
	if w.Code != 409 {
		t.Fatalf("save over a hand edit: %d %s, want 409", w.Code, w.Body.String())
	}
	if got := readFile(t, tb.cfgs.Path()); got != edited {
		t.Errorf("the refused save changed config.json:\n%s", got)
	}
	if !logContains(logs, "changed on disk since the bridge read it") {
		t.Errorf("the hand edit was not logged: %q", logs())
	}
	// The bridge runs on the hand edit now, and the page gets it on reload.
	if got := tb.cfgs.Get().MaxChunkSize; got != 1234 {
		t.Errorf("max_chunk_size = %d after the hand edit was found, want 1234", got)
	}
	w = tb.editConfig(t, func(c map[string]any) {
		if c["max_chunk_size"] != float64(1234) {
			t.Errorf("the reloaded page has max_chunk_size %v, want the hand edit", c["max_chunk_size"])
		}
		c["log_level"] = "debug"
	})
	if w.Code != 200 {
		t.Fatalf("save after reloading: %d %s", w.Code, w.Body.String())
	}
	if c := tb.cfgs.FileCopy(); c.MaxChunkSize != 1234 || c.LogLevel != "debug" {
		t.Errorf("after the save: max_chunk_size %d, log_level %q, want both edits", c.MaxChunkSize, c.LogLevel)
	}
}

// A save that changes one thing, as allowing a chat does, goes on top of a
// hand edit of config.json: both stay.
func TestHandEditThenDeltaUpdateKeepsBoth(t *testing.T) {
	tb := newTestBridge(t, "")
	editByHand(t, tb.cfgs.Path(), func(c map[string]any) { c["max_chunk_size"] = 1234 })
	if w := tb.do("POST", "/api/chats/allow", ChatRequest{Kind: "dm", Number: "15559876543"}); w.Code != 200 {
		t.Fatalf("allow: %d %s", w.Code, w.Body.String())
	}
	var file config.Config
	if err := json.Unmarshal([]byte(readFile(t, tb.cfgs.Path())), &file); err != nil {
		t.Fatal(err)
	}
	if file.MaxChunkSize != 1234 || !slices.Contains(file.AllowedNumbers, "15559876543") {
		t.Errorf("config.json has max_chunk_size %d and allowed_numbers %v, want the hand edit and the allowed number", file.MaxChunkSize, file.AllowedNumbers)
	}
	if cfg := tb.cfgs.Get(); cfg.MaxChunkSize != 1234 || !slices.Contains(cfg.AllowedNumbers, "15559876543") {
		t.Errorf("the bridge runs with max_chunk_size %d and allowed_numbers %v", cfg.MaxChunkSize, cfg.AllowedNumbers)
	}
}

// A hand edit that leaves config.json unloadable, half typed say, makes a
// save fail instead of writing over it; the bridge runs on what it had.
func TestHandEditThatDoesNotLoadIsNotWrittenOver(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"not JSON", `{"allowed_numbers": [`, "parsing config file"},
		{"invalid", `{"brain_effort": "extreme"}`, "effort"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := openTxStore(t, `{"allowed_numbers":["15551234567"]}`)
			if err := os.WriteFile(cs.Path(), []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := cs.Update("", func(c *config.Config) error { c.LogLevel = "debug"; return nil })
			if err == nil || !strings.Contains(err.Error(), "changed on disk and does not load") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("save over an unloadable hand edit: %v", err)
			}
			if got := readFile(t, cs.Path()); got != tc.body {
				t.Errorf("config.json was written over:\n%s", got)
			}
			if got := cs.Get().AllowedNumbers; !slices.Equal(got, []string{"15551234567"}) {
				t.Errorf("the bridge runs with allowed_numbers %v", got)
			}
		})
	}
}

// A config.json that is gone is written again by the next save, from the
// configuration the bridge runs with.
func TestConfigFileGoneIsWrittenAgain(t *testing.T) {
	cs := openTxStore(t, `{"allowed_numbers":["15551234567"]}`)
	if err := os.Remove(cs.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update("", func(c *config.Config) error { c.LogLevel = "debug"; return nil }); err != nil {
		t.Fatal(err)
	}
	var file config.Config
	if err := json.Unmarshal([]byte(readFile(t, cs.Path())), &file); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(file.AllowedNumbers, []string{"15551234567"}) || file.LogLevel != "debug" {
		t.Errorf("config.json written again: allowed_numbers %v, log_level %q", file.AllowedNumbers, file.LogLevel)
	}
}

// Loading and saving the configuration makes no folder: check and open
// load it too, and must not make a ~/.fridolin for a config file that is
// not there, and a folder typed into Settings waits for the restart that
// uses it. The bridge makes them as it starts (EnsureDirs).
func TestTheConfigMakesNoFolders(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	folders := []string{home, filepath.Join(root, "data"), filepath.Join(root, "db"), filepath.Join(root, "media"), filepath.Join(root, "new-media")}
	noFolders := func(when string) {
		t.Helper()
		for _, d := range folders {
			if _, err := os.Lstat(d); !os.IsNotExist(err) {
				t.Errorf("%s: %s is there (%v)", when, d, err)
			}
		}
	}

	if _, err := loadConfig(filepath.Join(root, "typo.json")); err != nil {
		t.Fatal(err)
	}
	noFolders("loading a config file that is not there")

	path := filepath.Join(root, "config.json")
	body := fmt.Sprintf(`{"data_dir": %q, "db_path": %q, "media_dir": %q}`, folders[1], filepath.Join(folders[2], "bridge.db"), folders[3])
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, err := openConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	noFolders("loading a config")
	if _, err := cs.Update("", func(c *config.Config) error { c.MediaDir = folders[4]; return nil }); err != nil {
		t.Fatal(err)
	}
	noFolders("saving a new media_dir")

	if err := cs.Boot().EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{folders[1], folders[2], folders[3]} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Errorf("EnsureDirs: %s is %v, %v; want a 0700 folder", d, st, err)
		}
	}
}

// check, open and rotate-token on a config file that is not there, a
// mistyped --config say, fail and make nothing, instead of reporting the
// defaults as a config that is fine.
func TestCommandsRefuseAMissingConfig(t *testing.T) {
	for _, args := range [][]string{{"check"}, {"open", "--print"}, {"rotate-token"}} {
		t.Run(args[0], func(t *testing.T) {
			root, home := fixtureHome(t)
			typo := filepath.Join(root, "typo.json")
			before := treeOf(t, root)
			out, code := runBridgeCLI(t, root, append(args, "--config", typo)...)
			if code == 0 || !strings.Contains(out, "no config file at "+typo+" (run fridolin init)") {
				t.Errorf("fridolin %s on a missing config: exit %d, %q", args[0], code, out)
			}
			if after := treeOf(t, root); !slices.Equal(after, before) {
				t.Errorf("it made %v (before: %v)", after, before)
			}
			if _, err := os.Lstat(filepath.Join(home, ".fridolin")); !os.IsNotExist(err) {
				t.Errorf("~/.fridolin is there: %v", err)
			}
		})
	}
}

// treeOf lists what is below root, but the folders runBridgeCLI makes for
// the command it runs: bin/, with the bridge binary, and tmp/, its TMPDIR.
func treeOf(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if top, _, _ := strings.Cut(rel, string(filepath.Separator)); rel != "." && top != "bin" && top != "tmp" {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A key config.json has that no setting takes is logged by its path, also
// inside a section or a brain's approved manifest: a typo there was
// dropped without a word on the next save.
func TestUnknownConfigKeysAreLoggedByPath(t *testing.T) {
	cases := []struct {
		name, body string
		want       string // the logged list, "" for no line
	}{
		{"in sections", `{"sandbox": {"enable": true}, "queue": {"notfy": false, "mode": "queue"}}`, "[queue.notfy sandbox.enable]"},
		{"at the top", `{"alowed_numbers": []}`, "[alowed_numbers]"},
		{"in a brain's approved manifest", `{"brain_dirs": [{"path": "~/a", "approved": {"id": "a", "name": "A", "nmae": "x", "routing": {"handles_dm": true}}}]}`, "[brain_dirs[0].approved.nmae brain_dirs[0].approved.routing.handles_dm]"},
		{"in another case", `{"Sandbox": {"Enabled": false}}`, ""},
		{"a retired key", `{"work_dir": "~/w"}`, ""},
		{"inside what decodes itself", `{"triggers": [{"text": "@c", "match": "word"}]}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			openTxStore(t, tc.body)
			var got string
			for _, l := range logs() {
				if _, rest, ok := strings.Cut(l, "ignoring unknown settings "); ok {
					got, _, _ = strings.Cut(rest, " (they will")
				}
			}
			if got != tc.want {
				t.Errorf("logged unknown settings %q, want %q (log %q)", got, tc.want, logs())
			}
		})
	}
}

// unknownKeys goes into maps and slices that hold structs.
func TestUnknownKeysInMapsAndSlices(t *testing.T) {
	type item struct {
		Name string `json:"name"`
	}
	type doc struct {
		Items  []item          `json:"items"`
		ByName map[string]item `json:"by_name"`
		Tags   []string        `json:"tags"`
		Ptr    *item           `json:"ptr,omitempty"`
		Plain  string
	}
	data := `{"items": [{"name": "a"}, {"nmae": "b"}], "by_name": {"x": {"name": "x", "extra": 1}}, "tags": ["t"], "ptr": {"bad": 1}, "Plain": "p", "other": 2}`
	got := jsonx.UnknownKeys([]byte(data), reflect.TypeOf(doc{}), "")
	want := []string{"by_name.x.extra", "items[1].nmae", "other", "ptr.bad"}
	if !slices.Equal(got, want) {
		t.Errorf("unknownKeys = %v, want %v", got, want)
	}
}

// A hand edit follows the same version migration as startup, but its ETag
// still names the exact bytes on disk until a successful delta saves v2.
func TestConfigExternalEditMigrationKeepsItsETag(t *testing.T) {
	cs := openTxStore(t, `{"version":2,"brain_model":"sonnet"}`)
	old := cs.ETag()
	const external = `{"model":"opus","log_level":"debug"}`
	if err := os.WriteFile(cs.Path(), []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := cs.Update(old, func(c *config.Config) error { called = true; return nil }); !errors.Is(err, config.ErrConflict) || called {
		t.Fatalf("update = %v, mutation called %v; want conflict before mutation", err, called)
	}
	snap := cs.Snapshot()
	if snap.File.Version != config.Version || snap.Effective.BrainModel != "opus" || snap.File.BrainModel != "opus" || snap.ETag != configETag([]byte(external)) {
		t.Fatalf("hand edit was not migrated into a consistent snapshot: %+v", snap)
	}
	if data, err := os.ReadFile(cs.Path()); err != nil || string(data) != external {
		t.Fatalf("conflict rewrote external file: %s (%v)", data, err)
	}
	if _, err := cs.Update(snap.ETag, func(c *config.Config) error { c.Version = 0; c.LogLevel = "info"; return nil }); err != nil {
		t.Fatal(err)
	}
	raw := readRawConfig(t, cs.Path())
	if string(raw["version"]) != "2" || string(raw["brain_model"]) != `"opus"` || raw["model"] != nil {
		t.Fatalf("saved migration = %v", raw)
	}
	data, err := os.ReadFile(cs.Path())
	if err != nil {
		t.Fatal(err)
	}
	if cs.ETag() != configETag(data) {
		t.Fatal("saved snapshot ETag does not identify the saved file")
	}
	if backup, err := os.ReadFile(cs.Path() + ".bak"); err != nil || string(backup) != external {
		t.Fatalf("backup = %s (%v), want original external edit", backup, err)
	}
}

// A newer hand edit must not be published or overwritten by an update.
func TestConfigUpdateRefusesANewerExternalVersion(t *testing.T) {
	cs := openTxStore(t, `{"version":2,"brain_model":"sonnet"}`)
	before := cs.Snapshot()
	const external = `{"version":3,"brain_model":"opus","a_new_setting":true}`
	if err := os.WriteFile(cs.Path(), []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := cs.Update("", func(c *config.Config) error { called = true; return nil }); !errors.Is(err, config.ErrTooNew) || called {
		t.Fatalf("update = %v, mutation called %v; want future-version refusal", err, called)
	}
	if after := cs.Snapshot(); after != before {
		t.Fatal("a refused future version changed the live snapshot")
	}
	if data, err := os.ReadFile(cs.Path()); err != nil || string(data) != external {
		t.Fatalf("future file changed: %s (%v)", data, err)
	}
}

// configETag independently checks the raw file-byte checksum contract.
func configETag(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:8]) }
