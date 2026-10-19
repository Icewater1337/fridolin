package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// PlaudFixture writes the fake CLI's input and reads its call log. It owns
// no environment: callers pass Dir to their child process explicitly.
type PlaudFixture struct {
	Dir string
	t   testing.TB
}

func NewPlaudFixture(t testing.TB) *PlaudFixture {
	t.Helper()
	return &PlaudFixture{Dir: t.TempDir(), t: t}
}

func (f *PlaudFixture) Write(name, body string) {
	if err := os.WriteFile(filepath.Join(f.Dir, name), []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// Files sets what `plaud files` lists, newest first.
func (f *PlaudFixture) Files(ids ...string) {
	data, _ := json.Marshal(ids)
	f.Write("list.json", string(data))
}

// Recording puts a recording named "Team sync" in Plaud, with Plaud's
// summary, and with its transcript once transcriptReady.
func (f *PlaudFixture) Recording(id string, transcriptReady bool) {
	f.Put(f.TeamSync(id, transcriptReady))
}

// TeamSync is the recording that Recording puts in Plaud, to change before
// putting it there.
func (f *PlaudFixture) TeamSync(id string, transcriptReady bool) PlaudRecording {
	start := "2026-10-16T18:00:00"
	r := PlaudRecording{ID: id, Name: "Team sync", CreatedAt: start, StartAt: &start, Duration: 300000,
		Notes: []PlaudBlock{{DataType: "auto_sum_note", Content: "Decided to ship on Friday.", TabName: "Summary"}}}
	if transcriptReady {
		r.Sources = []PlaudBlock{{DataType: "transaction", Content: PlaudTranscript(PlaudSegment{Start: 0, End: 4000, Speaker: "Speaker 1", Content: "Let's ship on Friday."})}}
	}
	return r
}

// Put puts r in Plaud, or replaces it there.
func (f *PlaudFixture) Put(r PlaudRecording) {
	data, _ := json.Marshal(r)
	f.Write("rec_"+r.ID+".json", string(data))
}

func (f *PlaudFixture) Calls() string {
	b, _ := os.ReadFile(filepath.Join(f.Dir, "calls.log"))
	return string(b)
}
