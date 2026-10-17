package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The fake Plaud CLI. TestMain runs fakePlaud when the test binary is
// started as `plaud`. It prints what @plaud-ai/cli 0.3.15 prints, byte for
// byte: TestFakePlaudPrintsWhatTheRealCLIPrints compares it with output
// recorded from the real CLI (testdata/plaud/cli-0.3.15.json), and
// TestPlaudCLIContract records that output again from an installed CLI.

// fakePlaudRecording is one recording, in the shape Plaud's API returns it
// and the CLI reads it.
type fakePlaudRecording struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	StartAt   *string `json:"start_at"`
	// Duration is in milliseconds.
	Duration     int64            `json:"duration"`
	SerialNumber *string          `json:"serial_number"`
	Sources      []fakePlaudBlock `json:"source_list"`
	Notes        []fakePlaudBlock `json:"note_list"`
}

// fakePlaudBlock is a transcript block (data_type "transaction",
// "transaction_polish") or a note ("auto_sum_note").
type fakePlaudBlock struct {
	DataType string `json:"data_type"`
	Content  string `json:"data_content"`
	TabName  string `json:"data_tab_name,omitempty"`
	Title    string `json:"data_title,omitempty"`
}

// fakePlaudSegment is one line of a transcript block's content.
type fakePlaudSegment struct {
	Start   int64  `json:"start_time"`
	End     int64  `json:"end_time"`
	Speaker string `json:"speaker"`
	Content string `json:"content"`
}

func fakePlaudTranscript(segs ...fakePlaudSegment) string {
	data, _ := json.Marshal(segs)
	return string(data)
}

// The fake's fixture directory (FRIDOLIN_FAKE_PLAUD_DIR) holds:
//
//	list.json          the ids `plaud files` lists, newest first
//	rec_<id>.json      a fakePlaudRecording
//	account            the user id `plaud me` reports (default u_1)
//	expired            the login lapsed: like the real CLI, the call deletes
//	                   tokens.json and exits 2
//	revoked            Plaud refuses the login: exit 2, tokens.json stays
//	offline            Plaud is unreachable: exit 3
//	fail_<command>     that command fails: exit 1
//	login_wait         `plaud login` prints the link and waits for login_go
//	login_deny         `plaud login` is denied: exit 2
//	login_timeout      `plaud login` times out: exit 4
//	hang               the command starts a child that holds its output,
//	                   writes the child's pid to child.pid, and hangs
//	flood              the command prints 8 MiB
//
// Without tokens.json in HOME/.plaud every command but login and logout
// exits 2, as the real one does. Each call is appended to calls.log with
// HOME and the working directory, and its environment goes to env.log.
func fakePlaud(args []string) int {
	dir := os.Getenv("FRIDOLIN_FAKE_PLAUD_DIR")
	cwd, _ := os.Getwd()
	if f, err := os.OpenFile(filepath.Join(dir, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintf(f, "HOME=%s CWD=%s %s\n", os.Getenv("HOME"), cwd, strings.Join(args, " "))
		f.Close()
	}
	_ = os.WriteFile(filepath.Join(dir, "env.log"), []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	fp := fakePlaudCLI{dir: dir, token: filepath.Join(os.Getenv("HOME"), ".plaud", "tokens.json")}
	return fp.run(args)
}

type fakePlaudCLI struct {
	dir, token string
}

func (f fakePlaudCLI) exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}

func (f fakePlaudCLI) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(b)
}

func (f fakePlaudCLI) recording(id string) (*fakePlaudRecording, bool) {
	data, err := os.ReadFile(filepath.Join(f.dir, "rec_"+id+".json"))
	if err != nil {
		return nil, false
	}
	var r fakePlaudRecording
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, false
	}
	return &r, true
}

// fail prints one of the CLI's error lines to stderr and returns code.
func fail(code int, kind, msg string) int {
	fmt.Fprintf(os.Stderr, "✗ [%s] %s\n", kind, msg)
	return code
}

// opt returns the value of a --name option in args, and whether it is set.
func opt(args []string, names ...string) (string, bool) {
	for i, a := range args {
		for _, n := range names {
			if a == n && i+1 < len(args) {
				return args[i+1], true
			}
			if v, ok := strings.CutPrefix(a, n+"="); ok {
				return v, true
			}
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func (f fakePlaudCLI) run(args []string) int {
	switch {
	case f.exists("hang"):
		child := exec.Command("sleep", "30")
		child.Stdout = os.Stdout
		if err := child.Start(); err == nil {
			_ = os.WriteFile(filepath.Join(f.dir, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		}
		time.Sleep(30 * time.Second)
		return 0
	case f.exists("flood"):
		line := strings.Repeat("x", 1023) + "\n"
		for i := 0; i < 8<<10; i++ {
			fmt.Print(line)
		}
		return 0
	}
	spinner := map[string]string{"me": "Fetching user info...", "files": "Fetching files...", "file": "Fetching file...",
		"transcript": "Fetching transcript...", "summary": "Fetching summary..."}
	switch args[0] {
	case "login":
		return f.login()
	case "logout":
		if _, err := os.Stat(f.token); err != nil {
			fmt.Println("Not logged in.")
			return 0
		}
		_ = os.Remove(f.token)
		fmt.Println("Logged out and revoked authorization.")
		return 0
	}
	// ora writes its spinner to stderr, and without a terminal only once.
	fmt.Fprintf(os.Stderr, "- %s\n", spinner[args[0]])
	if _, err := os.Stat(f.token); err != nil {
		return fail(2, "AUTH_FAILED", "Token invalid or expired. Run `plaud login`.")
	}
	switch {
	case f.exists("expired"):
		// The refresh token lapsed: the CLI discards the login.
		_ = os.Remove(f.token)
		return fail(2, "AUTH_FAILED", "Token invalid or expired. Run `plaud login`.")
	case f.exists("revoked"):
		return fail(2, "AUTH_FAILED", "Token invalid or expired. Run `plaud login`.")
	case f.exists("offline"):
		code := fail(3, "UNREACHABLE", "Cannot reach Plaud servers. Check your network.")
		fmt.Fprintln(os.Stderr, "TypeError: fetch failed | cause: Error: bad port")
		return code
	case f.exists("fail_" + args[0]):
		what := map[string]string{"me": "user info", "files": "files", "file": "file", "transcript": "transcript", "summary": "summary"}[args[0]]
		return fail(1, "FETCH_FAILED", "Failed to fetch "+what+".")
	}
	switch args[0] {
	case "me":
		id := strings.TrimSpace(f.read("account"))
		if id == "" {
			id = "u_1"
		}
		fmt.Printf("\nUser Info:\n\n  id: %s\n  nickname: You\n  email: you@example.com\n\n", id)
	case "files":
		return f.files(args)
	case "file":
		return f.file(args[1])
	case "transcript":
		return f.transcript(args)
	case "summary":
		return f.summary(args)
	}
	return 0
}

func (f fakePlaudCLI) login() int {
	if _, err := os.Stat(f.token); err == nil {
		fmt.Println("Already logged in. Run `plaud logout` first to switch accounts.")
		return 0
	}
	fmt.Fprintln(os.Stderr, "- Waiting for browser authentication...")
	fmt.Print("\nOpening browser for authentication...\n\n")
	if f.exists("login_wait") {
		// What the real CLI prints when it cannot open a browser.
		fmt.Println("Could not open browser. Open this URL manually:\n  https://web.plaud.ai/platform/oauth?client_id=x&state=y")
		for i := 0; i < 200 && !f.exists("login_go"); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
	switch {
	case f.exists("login_deny"):
		return fail(2, "AUTH_FAILED", "Authentication was denied.")
	case f.exists("login_timeout"):
		return fail(4, "AUTH_FAILED", "Authentication timed out after 2 minutes.")
	}
	_ = os.MkdirAll(filepath.Dir(f.token), 0o700)
	_ = os.WriteFile(f.token, []byte(`{"access_token":"test-access","refresh_token":"fake","token_type":"Bearer"}`), 0o600)
	fmt.Fprintln(os.Stderr, "✔ Logged in successfully!")
	return 0
}

func (f fakePlaudCLI) files(args []string) int {
	page, size := 1, 20
	if v, ok := opt(args, "--page", "-p"); ok {
		page, _ = strconv.Atoi(v)
	}
	if v, ok := opt(args, "--page-size", "-s"); ok {
		size, _ = strconv.Atoi(v)
	}
	if size < 10 || size > 100 {
		return fail(1, "INVALID_ARGS", "--page-size must be a number between 10 and 100")
	}
	var ids []string
	_ = json.Unmarshal([]byte(f.read("list.json")), &ids)
	from, to := min((page-1)*size, len(ids)), min(page*size, len(ids))
	ids = ids[from:to]
	fmt.Printf("\nFiles on this page: %d\n\n", len(ids))
	fmt.Printf("  %-34s  %-36s  %-12s  DURATION\n", "ID", "NAME", "DATE")
	fmt.Println("  " + strings.Repeat("─", 34+36+12+16))
	for _, id := range ids {
		r, ok := f.recording(id)
		if !ok {
			r = &fakePlaudRecording{ID: id, Name: "Some name", CreatedAt: "2026-10-16T18:00:00", Duration: 300000}
		}
		name := []rune(r.Name)
		col := fmt.Sprintf("%-36s", r.Name)
		if len(name) > 36 {
			col = string(name[:35]) + "…"
		}
		fmt.Printf("  %-34s  %s  %-12s  %s\n", r.ID, col, fakePlaudDate(r.CreatedAt), fakePlaudDuration(r.Duration))
	}
	fmt.Printf("\nPage %d\n", page)
	return 0
}

func (f fakePlaudCLI) file(id string) int {
	r, ok := f.recording(id)
	if !ok {
		return fail(1, "NOT_FOUND", "File not found: "+id)
	}
	avail := func(blocks []fakePlaudBlock, typ string) string {
		for _, b := range blocks {
			if b.DataType == typ {
				return "available"
			}
		}
		return "unavailable"
	}
	orDash := func(s *string) string {
		if s == nil {
			return "-"
		}
		return *s
	}
	fmt.Print("\nFile Details:\n\n")
	fmt.Printf("  id:           %s\n", r.ID)
	fmt.Printf("  name:         %s\n", r.Name)
	fmt.Printf("  created_at:   %s\n", r.CreatedAt)
	fmt.Printf("  start_at:     %s\n", orDash(r.StartAt))
	fmt.Printf("  duration:     %s\n", fakePlaudDuration(r.Duration))
	fmt.Printf("  serial_number: %s\n", orDash(r.SerialNumber))
	fmt.Printf("  audio:        unknown (run `plaud audio` to check)\n")
	fmt.Printf("  transcript:   %s\n", avail(r.Sources, "transaction"))
	fmt.Printf("  summary:      %s\n", avail(r.Notes, "auto_sum_note"))
	fmt.Println()
	return 0
}

func (f fakePlaudCLI) transcript(args []string) int {
	r, ok := f.recording(args[1])
	if !ok {
		code := fail(1, "FETCH_FAILED", "Failed to fetch transcript.")
		fmt.Fprintln(os.Stderr, "Error: API error: 404 Not Found")
		return code
	}
	block := "transaction"
	if hasFlag(args, "--polished") {
		block = "transaction_polish"
	}
	var src *fakePlaudBlock
	var available []string
	for i, b := range r.Sources {
		available = append(available, b.DataType)
		if b.DataType == block && src == nil {
			src = &r.Sources[i]
		}
	}
	if src == nil {
		list := strings.Join(available, ", ")
		if list == "" {
			list = "(none)"
		}
		fmt.Printf("No %q transcript for this recording. Available: %s.\n", block, list)
		return 0
	}
	if src.Content == "" {
		if block == "transaction_polish" {
			fmt.Println("The cleaned-up (polished) transcript hasn't been generated for this recording yet.")
		} else {
			fmt.Printf("The %q transcript is not available for this recording.\n", block)
		}
		return 0
	}
	var segs []fakePlaudSegment
	out := src.Content
	if json.Unmarshal([]byte(src.Content), &segs) == nil {
		lines := make([]string, len(segs))
		for i, s := range segs {
			lines[i] = fmt.Sprintf("[%s - %s] %s: %s", fakePlaudTime(s.Start), fakePlaudTime(s.End), s.Speaker, s.Content)
		}
		out = strings.Join(lines, "\n")
	}
	if path, ok := opt(args, "--output", "-o"); ok {
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			return fail(1, "FETCH_FAILED", "Failed to fetch transcript.")
		}
		fmt.Printf("Transcript saved to %s\n", path)
		return 0
	}
	fmt.Printf("\nTranscript: %s\n\n%s\n\n", r.Name, out)
	return 0
}

func (f fakePlaudCLI) summary(args []string) int {
	r, ok := f.recording(args[1])
	if !ok {
		code := fail(1, "FETCH_FAILED", "Failed to fetch summary.")
		fmt.Fprintln(os.Stderr, "Error: API error: 404 Not Found")
		return code
	}
	var sum *fakePlaudBlock
	var available []string
	for i, n := range r.Notes {
		available = append(available, n.DataType)
		if n.DataType == "auto_sum_note" && sum == nil {
			sum = &r.Notes[i]
		}
	}
	if sum == nil {
		if len(r.Notes) == 0 {
			fmt.Println("Summary not available for this recording.")
		} else {
			fmt.Printf("No \"auto_sum_note\" note for this recording. Available: %s.\n", strings.Join(available, ", "))
		}
		return 0
	}
	if len(r.Notes) > 1 {
		fmt.Fprintf(os.Stderr, "Note: this recording has %d notes (1 summary); showing the first. Use --all to print them all, or --json for the raw payload.\n", len(r.Notes))
	}
	body := sum.Content
	if body == "" {
		body = "(this note has no content yet — nothing was returned for \"auto_sum_note\")"
	}
	if path, ok := opt(args, "--output", "-o"); ok {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fail(1, "FETCH_FAILED", "Failed to fetch summary.")
		}
		fmt.Printf("Summary saved to %s\n", path)
		return 0
	}
	fmt.Printf("\nSummary: %s\n\n%s\n\n", r.Name, body)
	return 0
}

// fakePlaudDuration is the CLI's formatDuration.
func fakePlaudDuration(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	s := ms / 1000
	h, m, sec := s/3600, s%3600/60, s%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// fakePlaudTime is the CLI's formatTime: mm:ss.
func fakePlaudTime(ms int64) string {
	s := ms / 1000
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// fakePlaudDate is the CLI's formatDate: the local day of a timestamp,
// which Plaud sends in UTC without an offset.
func fakePlaudDate(iso string) string {
	if iso == "" {
		return "-"
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", iso, time.UTC)
	if err != nil {
		return iso
	}
	return t.Local().Format("2006-01-02")
}
