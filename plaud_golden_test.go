package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// What @plaud-ai/cli prints, recorded from the real CLI against a stand-in
// for Plaud's API that serves plaudGoldenRecordings. The fake CLI is held
// to it, and TestPlaudCLIContract records it again:
//
//	FRIDOLIN_PLAUD_CLI=$(command -v plaud) go test -run TestPlaudCLIContract .
//
// compares an installed CLI with it, and with -update-plaud-golden rewrites
// it. Neither signs in to Plaud or leaves this machine: the CLI gets a test
// token and talks to a server on 127.0.0.1.
var updatePlaudGolden = flag.Bool("update-plaud-golden", false, "rewrite "+plaudGoldenFile+" from the CLI in FRIDOLIN_PLAUD_CLI")

// plaudCLITested is the CLI version the golden output comes from.
const plaudCLITested = "0.3.15"

var plaudGoldenFile = filepath.Join("testdata", "plaud", "cli-"+plaudCLITested+".json")

// plaudGoldenRun is one recorded call: the login it ran with, its
// arguments (@OUT@ stands for a file to write to), and what it did.
type plaudGoldenRun struct {
	// Login is ok, expired (the refresh token lapsed), rejected (Plaud
	// refused the refresh), revoked (Plaud refuses the access token) or
	// none (no tokens.json).
	Login     string   `json:"login"`
	Offline   bool     `json:"offline"`
	Args      []string `json:"args"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
	Exit      int      `json:"exit"`
	TokenKept bool     `json:"token_kept"`
	// OutputFile is what the CLI wrote to @OUT@, if it wrote anything.
	OutputFile *string `json:"output_file,omitempty"`
}

func loadPlaudGolden(t *testing.T) map[string]plaudGoldenRun {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoDir, plaudGoldenFile))
	if err != nil {
		t.Fatal(err)
	}
	var runs map[string]plaudGoldenRun
	if err := json.Unmarshal(data, &runs); err != nil {
		t.Fatal(err)
	}
	return runs
}

// The recordings of the golden output. Their ids say what each is for.
const (
	plaudGoldTeam     = "of_0000000000000000000003" // no start_at; a summary line starts with "Fetching"
	plaudGoldDoctor   = "of_0000000000000000000002" // with start_at and a polished transcript
	plaudGoldOld      = "of_0000000000000000000001" // no notes
	plaudGoldNoPolish = "of_00000000000000000000aa" // no polished block
	plaudGoldEmptyPol = "of_00000000000000000000ab" // an empty polished block
	plaudGoldNoNotes  = "of_00000000000000000000ac"
	plaudGoldEmptySum = "of_00000000000000000000ad" // a summary note without content
	plaudGoldNoTrans  = "of_00000000000000000000ae" // not transcribed yet
)

// plaudGoldenSummary is the summary of the golden recordings. Its second
// line starts with "Fetching", as the spinner line does.
const plaudGoldenSummary = "## Team sync\n\nDecisions: ship Friday.\nFetching the release notes is Anna's job.\nBudget: 10k approved."

// plaudGoldenRecordings returns what `plaud files` lists, newest first,
// and every recording.
func plaudGoldenRecordings() ([]string, []fakePlaudRecording) {
	tr := fakePlaudBlock{DataType: "transaction", Content: fakePlaudTranscript(
		fakePlaudSegment{0, 4000, "Speaker 1", "Let's ship on Friday."},
		fakePlaudSegment{4000, 9500, "Speaker 2", "Fetching the release notes is Anna's job."})}
	pol := fakePlaudBlock{DataType: "transaction_polish", Content: fakePlaudTranscript(
		fakePlaudSegment{0, 4000, "Speaker 1", "Let us ship on Friday."})}
	sum := fakePlaudBlock{DataType: "auto_sum_note", Content: plaudGoldenSummary, TabName: "Summary", Title: "Team sync"}
	at := func(s string) *string { return &s }
	rec := func(id, name string, start *string, sources, notes []fakePlaudBlock) fakePlaudRecording {
		return fakePlaudRecording{ID: id, Name: name, CreatedAt: "2026-10-16T18:00:00", StartAt: start, Duration: 300000, Sources: sources, Notes: notes}
	}
	day := at("2026-10-15T07:30:00")
	return []string{plaudGoldTeam, plaudGoldDoctor, plaudGoldOld}, []fakePlaudRecording{
		rec(plaudGoldTeam, "Team sync", nil, []fakePlaudBlock{tr}, []fakePlaudBlock{sum}),
		rec(plaudGoldDoctor, "Doctor: Fetching results", day, []fakePlaudBlock{tr, pol}, []fakePlaudBlock{sum}),
		rec(plaudGoldOld, "Old one", at("2026-10-14T09:00:00"), []fakePlaudBlock{tr}, nil),
		rec(plaudGoldNoPolish, "No polish", day, []fakePlaudBlock{tr}, []fakePlaudBlock{sum}),
		rec(plaudGoldEmptyPol, "Empty polish", day, []fakePlaudBlock{tr, {DataType: "transaction_polish"}}, []fakePlaudBlock{sum}),
		rec(plaudGoldNoNotes, "No notes", day, []fakePlaudBlock{tr}, nil),
		rec(plaudGoldEmptySum, "Empty summary", day, []fakePlaudBlock{tr}, []fakePlaudBlock{{DataType: "auto_sum_note"}}),
		rec(plaudGoldNoTrans, "Still transcribing", day, nil, nil),
	}
}

// writeFakePlaudRecordings gives the fake CLI in dir what to list and show.
func writeFakePlaudRecordings(t *testing.T, dir string, list []string, recs []fakePlaudRecording) {
	t.Helper()
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(dir, "list.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		data, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(dir, "rec_"+r.ID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// plaudCallResult is what one CLI call did, in the terms of plaudGoldenRun.
type plaudCallResult struct {
	stdout, stderr string
	exit           int
	tokenKept      bool
	outputFile     *string
}

func (r plaudCallResult) diff(want plaudGoldenRun) []string {
	var d []string
	if r.stdout != want.Stdout {
		d = append(d, fmt.Sprintf("stdout:\n%q\nwant\n%q", r.stdout, want.Stdout))
	}
	if r.stderr != want.Stderr {
		d = append(d, fmt.Sprintf("stderr:\n%q\nwant\n%q", r.stderr, want.Stderr))
	}
	if r.exit != want.Exit {
		d = append(d, fmt.Sprintf("exit %d, want %d", r.exit, want.Exit))
	}
	if r.tokenKept != want.TokenKept {
		d = append(d, fmt.Sprintf("tokens.json kept = %v, want %v", r.tokenKept, want.TokenKept))
	}
	switch {
	case (r.outputFile == nil) != (want.OutputFile == nil):
		d = append(d, fmt.Sprintf("wrote a file = %v, want %v", r.outputFile != nil, want.OutputFile != nil))
	case r.outputFile != nil && *r.outputFile != *want.OutputFile:
		d = append(d, fmt.Sprintf("file:\n%q\nwant\n%q", *r.outputFile, *want.OutputFile))
	}
	return d
}

// runPlaudCall runs cmd (the CLI, args filled in) for a golden run, with
// HOME home, and reports what it did. out is the file @OUT@ stands for.
func runPlaudCall(t *testing.T, cmd *exec.Cmd, home, out string) plaudCallResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Dir = t.TempDir()
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatal(err)
	}
	r := plaudCallResult{
		stdout: strings.ReplaceAll(stdout.String(), out, "@OUT@"),
		stderr: strings.ReplaceAll(stderr.String(), out, "@OUT@"),
		exit:   cmd.ProcessState.ExitCode(),
	}
	_, err = os.Stat(filepath.Join(home, ".plaud", "tokens.json"))
	r.tokenKept = err == nil
	if data, err := os.ReadFile(out); err == nil {
		s := string(data)
		r.outputFile = &s
	}
	return r
}

func plaudGoldenArgs(args []string, out string) []string {
	filled := make([]string, len(args))
	for i, a := range args {
		filled[i] = strings.ReplaceAll(a, "@OUT@", out)
	}
	return filled
}

// The fake CLI prints what the real one printed for the same recordings,
// on the same streams, and leaves the login as it did.
func TestFakePlaudPrintsWhatTheRealCLIPrints(t *testing.T) {
	runs := loadPlaudGolden(t)
	list, recs := plaudGoldenRecordings()
	for name, want := range runs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir, home := t.TempDir(), t.TempDir()
			writeFakePlaudRecordings(t, dir, list, recs)
			if want.Login != "none" {
				if err := os.MkdirAll(filepath.Join(home, ".plaud"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, ".plaud", "tokens.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			flags := map[string]string{"expired": "expired", "rejected": "expired", "revoked": "revoked"}
			if f := flags[want.Login]; f != "" {
				_ = os.WriteFile(filepath.Join(dir, f), nil, 0o600)
			}
			if want.Offline {
				_ = os.WriteFile(filepath.Join(dir, "offline"), nil, 0o600)
			}
			out := filepath.Join(t.TempDir(), "out.txt")
			cmd := exec.Command(os.Args[0], plaudGoldenArgs(want.Args, out)...)
			cmd.Env = []string{"FRIDOLIN_FAKE_PLAUD=1", "FRIDOLIN_FAKE_PLAUD_DIR=" + dir, "HOME=" + home, "GORACE=atexit_sleep_ms=0", "TZ=UTC"}
			for _, d := range runPlaudCall(t, cmd, home, out).diff(want) {
				t.Error(d)
			}
		})
	}
}

// plaudStubAPI stands in for Plaud's API, serving list and recs to a
// client with the access token "test-access". Refreshing a token is
// always refused.
func plaudStubAPI(list []string, recs []fakePlaudRecording) http.Handler {
	byID := map[string]fakePlaudRecording{}
	for _, r := range recs {
		byID[r.ID] = r
	}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-access" {
				http.Error(w, `{"detail":"bad token"}`, http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /open/third-party/users/current", auth(func(w http.ResponseWriter, r *http.Request) {
		// `plaud me` prints the fields in the order Plaud sends them.
		writeJSON(w, http.StatusOK, struct {
			ID       string `json:"id"`
			Nickname string `json:"nickname"`
			Email    string `json:"email"`
		}{"u_1", "You", "you@example.com"})
	}))
	mux.HandleFunc("GET /open/third-party/files/{$}", auth(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
		data := []map[string]any{}
		for i := (page - 1) * size; i < min(page*size, len(list)); i++ {
			rec := byID[list[i]]
			data = append(data, map[string]any{"id": rec.ID, "name": rec.Name, "created_at": rec.CreatedAt, "duration": rec.Duration})
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "page": page})
	}))
	mux.HandleFunc("GET /open/third-party/files/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := byID[r.PathValue("id")]
		if !ok {
			http.Error(w, `{"detail":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}))
	mux.HandleFunc("POST /open/third-party/users/current/revoke", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{})
	}))
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"refresh token revoked"}`, http.StatusUnauthorized)
	})
	return mux
}

// writeRealPlaudLogin gives the real CLI in home a login of the kind a
// golden run names, and points it at api.
func writeRealPlaudLogin(t *testing.T, home, kind, api string) {
	t.Helper()
	jwt := func(exp int64) string {
		enc := base64.RawURLEncoding.EncodeToString
		return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp))) + ".x"
	}
	future, past := time.Now().Add(24*time.Hour).UnixMilli(), time.Now().Add(-24*time.Hour).UnixMilli()
	tokens := map[string]map[string]any{
		"ok":       {"access_token": "test-access", "refresh_token": jwt(4102444800), "expires_at": future},
		"expired":  {"access_token": "old", "refresh_token": jwt(1000), "expires_at": past},
		"rejected": {"access_token": "old", "refresh_token": jwt(4102444800), "expires_at": past},
		"revoked":  {"access_token": "revoked", "refresh_token": jwt(4102444800), "expires_at": future},
	}
	dir := filepath.Join(home, ".plaud")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if tok, ok := tokens[kind]; ok {
		tok["token_type"] = "Bearer"
		data, _ := json.Marshal(tok)
		if err := os.WriteFile(filepath.Join(dir, "tokens.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	yaml := fmt.Sprintf("api_base: %s\nrefresh_url: %s/refresh\n", api, api)
	if err := os.WriteFile(filepath.Join(dir, "cli.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// realPlaudCLI returns the installed CLI to test against, or skips.
func realPlaudCLI(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("FRIDOLIN_PLAUD_CLI")
	if bin == "" {
		t.Skip("set FRIDOLIN_PLAUD_CLI to an installed plaud to check it against the recorded output")
	}
	return bin
}

// realPlaudEnv is what the real CLI runs with in these tests: its own
// node first on PATH, no telemetry, no update check.
func realPlaudEnv(bin, home string) []string {
	return []string{"HOME=" + home, "PATH=" + filepath.Dir(bin) + ":/usr/bin:/bin", "NO_COLOR=1", "TZ=UTC",
		"PLAUD_TELEMETRY_DISABLED=1", "DO_NOT_TRACK=1", "PLAUD_NO_UPDATE_NOTIFIER=1"}
}

// An installed CLI still prints what the golden output says, so the fake,
// and what Fridolin reads, still match it.
func TestPlaudCLIContract(t *testing.T) {
	bin := realPlaudCLI(t)
	list, recs := plaudGoldenRecordings()
	srv := httptest.NewServer(plaudStubAPI(list, recs))
	defer srv.Close()
	runs := loadPlaudGolden(t)
	names := make([]string, 0, len(runs))
	for name := range runs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := runs[name]
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			api := srv.URL
			if want.Offline {
				api = "http://127.0.0.1:1"
			}
			writeRealPlaudLogin(t, home, want.Login, api)
			out := filepath.Join(t.TempDir(), "out.txt")
			cmd := exec.Command(bin, plaudGoldenArgs(want.Args, out)...)
			cmd.Env = realPlaudEnv(bin, home)
			got := runPlaudCall(t, cmd, home, out)
			if *updatePlaudGolden {
				want.Stdout, want.Stderr, want.Exit, want.TokenKept, want.OutputFile = got.stdout, got.stderr, got.exit, got.tokenKept, got.outputFile
				runs[name] = want
				return
			}
			for _, d := range got.diff(want) {
				t.Error(d)
			}
		})
	}
	if *updatePlaudGolden {
		data, _ := json.MarshalIndent(runs, "", "  ")
		if err := os.WriteFile(filepath.Join(repoDir, plaudGoldenFile), append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Fridolin reads what an installed CLI prints: a poll against the same
// stand-in for Plaud's API hands both new recordings over, each with its
// summary and transcript whole and its own date.
func TestPlaudForwardWithTheRealCLI(t *testing.T) {
	bin := realPlaudCLI(t)
	list, recs := plaudGoldenRecordings()
	srv := httptest.NewServer(plaudStubAPI(list, recs))
	defer srv.Close()
	tb := newTestBridge(t, `, "plaud": {"enabled": true, "cli_path": "`+bin+`"}`)
	tb.addFolderBrain(t, plaudDMBrain)
	writeRealPlaudLogin(t, tb.plaud.cliHome(), "ok", srv.URL)
	if err := tb.plaud.saveState(plaudSeededState("u_1", plaudGoldOld)); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	local := func(s string) string {
		t, _ := time.ParseInLocation("2006-01-02T15:04:05", s, time.UTC)
		return t.Local().Format("02.01.2006 15:04")
	}
	for id, want := range map[string][]string{
		plaudGoldTeam: {"Plaud recording: Team sync\nRecorded: " + local("2026-10-16T18:00:00") + "\nDuration: 5m00s\n",
			"=== Plaud AI summary ===\n" + plaudGoldenSummary + "\n\n=== Transcript ===\n[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n[00:04 - 00:09] Speaker 2: Fetching the release notes is Anna's job.\n"},
		plaudGoldDoctor: {"Plaud recording: Doctor: Fetching results\nRecorded: " + local("2026-10-15T07:30:00") + "\n"},
	} {
		got := tb.plaudDelivered(t, id)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", id, w, got)
			}
		}
	}
}

// parseKV leaves out a value the CLI has not got: "-" for a missing
// start_at or serial_number, as in the recorded details, and "undefined"
// or "null" for a field Plaud did not send. The transcript's header then
// falls back ("?" for the duration) rather than print them.
func TestPlaudParseKV(t *testing.T) {
	d := parseKV(loadPlaudGolden(t)["file"].Stdout)
	for k, want := range map[string]string{"id": plaudGoldTeam, "name": "Team sync", "duration": "5m00s", "transcript": "available", "start_at": "", "serial_number": ""} {
		if d[k] != want {
			t.Errorf("recorded details: %s = %q, want %q", k, d[k], want)
		}
	}
	for _, c := range []struct {
		line, want string
	}{
		{"  duration: 5m00s", "5m00s"},
		{"  duration: -", ""},
		{"  duration: undefined", ""},
		{"  duration: null", ""},
		{"  duration:", ""},
		{"  duration:    ", ""},
	} {
		got, ok := parseKV(c.line)["duration"]
		if got != c.want || ok != (c.want != "") {
			t.Errorf("parseKV(%q) duration = %q (%v), want %q", c.line, got, ok, c.want)
		}
	}
}
