package site

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The site passes a phone's request on through its spool; the bridge
// answers with what the request's handler says.
func TestSiteSpoolPassesPhoneRequestsOn(t *testing.T) {
	m := newTestSite(t)
	var got []string
	m.hooks.Device = func(op, assertion string, body []byte) (map[string]any, error) {
		got = append(got, op+"|"+assertion+"|"+string(body))
		return map[string]any{"device": "dev_1"}, nil
	}
	dir := filepath.Join(m.spoolDir(), strings.Repeat("d", 24))
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "body.txt"), []byte("text"), 0o600)
	os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"op":"transcript","assertion":"a.b.c"}`), 0o600)

	m.processSpool(context.Background())

	if !slices.Equal(got, []string{"transcript|a.b.c|text"}) {
		t.Errorf("the bridge got %v", got)
	}
	if a := readSpoolAnswer(t, dir); a["ok"] != true || a["device"] != "dev_1" {
		t.Errorf("answer = %v", a)
	}
}

// A fetch of a voice answer waits for it off the spool's loop: the
// requests after it are answered meanwhile, and it once, when it is there.
func TestSiteSpoolLetsAVoiceFetchWait(t *testing.T) {
	m := newTestSite(t)
	release := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	m.hooks.Device = func(op, assertion string, body []byte) (map[string]any, error) {
		mu.Lock()
		calls[op]++
		mu.Unlock()
		if op == "voice_reply" {
			<-release
			return map[string]any{"state": "done"}, nil
		}
		return nil, nil
	}
	spool := func(name, req string) string {
		dir := filepath.Join(m.spoolDir(), strings.Repeat(name, 24))
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "device.json"), []byte(req), 0o600)
		return dir
	}
	wait := spool("a", `{"op":"voice_reply","assertion":"a.b.c"}`)
	m.processSpool(context.Background())
	other := spool("b", `{"op":"transcript","assertion":"a.b.c"}`)
	m.processSpool(context.Background())
	if a := readSpoolAnswer(t, other); a["ok"] != true {
		t.Errorf("the request after the fetch: %v", a)
	}
	if _, err := os.Stat(filepath.Join(wait, "result.json")); err == nil {
		t.Fatal("the fetch was answered before its answer was there")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(wait, "result.json")); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a := readSpoolAnswer(t, wait); a["state"] != "done" {
		t.Errorf("the fetch's answer: %v", a)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["voice_reply"] != 1 {
		t.Errorf("the fetch was passed on %d times", calls["voice_reply"])
	}
}

// A fetch of the phone app is written to a file of its own in the
// request's folder, off the spool's loop, for the site to send on; one
// that fails leaves no file.
func TestSiteSpoolWritesTheAppAPhoneFetches(t *testing.T) {
	m := newTestSite(t)
	m.hooks.DeviceDownload = func(op, assertion string, w io.Writer) (map[string]any, error) {
		if assertion == "refused" {
			_, _ = w.Write([]byte("half"))
			return nil, errors.New("that build is not the current one any more")
		}
		n, err := w.Write([]byte("sealed app for " + assertion))
		return map[string]any{"size": n}, err
	}
	spool := func(name, req string) string {
		dir := filepath.Join(m.spoolDir(), strings.Repeat(name, 24))
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "device.json"), []byte(req), 0o600)
		return dir
	}
	ok := spool("a", `{"op":"app_apk","assertion":"a.b.c"}`)
	refused := spool("b", `{"op":"app_apk","assertion":"refused"}`)
	m.processSpool(context.Background())
	answered := func(dir string) bool {
		_, err := os.Stat(filepath.Join(dir, "result.json"))
		return err == nil
	}
	for deadline := time.Now().Add(5 * time.Second); !(answered(ok) && answered(refused)) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if a := readSpoolAnswer(t, ok); a["ok"] != true || a["file"] != "app.sealed" || a["size"] != float64(len("sealed app for a.b.c")) {
		t.Errorf("answer = %v", a)
	}
	if data, err := os.ReadFile(filepath.Join(ok, "app.sealed")); err != nil || string(data) != "sealed app for a.b.c" {
		t.Errorf("the file: %q, %v", data, err)
	}
	if a := readSpoolAnswer(t, refused); a["ok"] != false || !strings.Contains(a["error"].(string), "not the current one") {
		t.Errorf("refused answer = %v", a)
	}
	if _, err := os.Stat(filepath.Join(refused, "app.sealed")); err == nil {
		t.Error("a refused fetch left its half-written file")
	}
}
