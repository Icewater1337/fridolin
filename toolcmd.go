package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The subcommands brains run: `fridolin history`, `fridolin polls` and
// `fridolin calendar`. They take the place of the Python helpers that used
// to do the same with their own copies of the data dir, the token file and
// bridge.db. Each prints one JSON document on stdout, which the assistant
// reads; an error is {"error": "..."} on stdout with exit status 1, so the
// assistant sees it in the same place. Usage text goes to stderr.

// toolIO is the process a tool subcommand runs in, passed in as values so
// that a test can run one in process.
type toolIO struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	dir            string    // the working directory: the brain's folder in a run
	now            time.Time // what "today" and --since 7d count from
}

// processToolIO is the real process's.
func processToolIO() toolIO {
	dir, _ := os.Getwd()
	return toolIO{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, dir: dir, now: time.Now()}
}

// emit prints v as the command's JSON document and returns exit status 0.
func (t toolIO) emit(v any) int {
	enc := json.NewEncoder(t.stdout)
	enc.SetIndent("", "  ")
	// The assistant reads it, and "&" or "<" in an event title stays readable.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(t.stderr, "writing the result: %v\n", err)
		return 1
	}
	return 0
}

// fail prints {"error": msg} as the command's JSON document and returns
// exit status 1.
func (t toolIO) fail(format string, args ...any) int {
	t.emit(map[string]string{"error": fmt.Sprintf(format, args...)})
	return 1
}

// errHelp is parseTool's answer to -h: the usage was printed, exit 0.
var errHelp = errors.New("help requested")

// parseTool parses args with fs and returns the positional arguments.
// Unlike flag.Parse it takes flags after them too, as the Python tools'
// argparse did: `fridolin history invoice --since 7d`. "--" ends the
// flags. Usage errors go to stderr, with the error itself for the JSON.
func (t toolIO) parseTool(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(t.stderr)
	var positional []string
	for {
		err := fs.Parse(args)
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelp
		}
		if err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// flag stops at "--" too, and swallows it: everything after it is
		// positional, also what looks like a flag.
		if used := len(args) - len(rest); used > 0 && args[used-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// exitFor turns a parseTool error into the exit status, printing the JSON
// error for a usage mistake.
func (t toolIO) exitFor(cmd string, err error) int {
	if errors.Is(err, errHelp) {
		return 0
	}
	return t.fail("%s: %v (see %s -h)", cmd, err, cmd)
}

// brainClient calls the bridge's brain API (/brain/) from inside a run,
// with the address the bridge put in its environment and the run's token
// from the file the environment names.
type brainClient struct {
	base, token string
	http        *http.Client
}

// brainClientFor is the client for the run t is part of.
func brainClientFor(t toolIO) (*brainClient, error) {
	base, file := t.getenv("FRIDOLIN_API"), t.getenv("FRIDOLIN_BRAIN_TOKEN_FILE")
	if base == "" || file == "" {
		return nil, errors.New("this command works inside a brain's run, where the bridge sets FRIDOLIN_API and FRIDOLIN_BRAIN_TOKEN_FILE (and not when the bridge's HTTP server is off)")
	}
	token, err := readRunToken(file)
	if err != nil {
		return nil, fmt.Errorf("reading this run's brain API token (FRIDOLIN_BRAIN_TOKEN_FILE): %w", err)
	}
	return &brainClient{base: strings.TrimSuffix(base, "/"), token: token, http: &http.Client{
		// A calendar call can take several Google requests; the bridge gives
		// each 20 seconds.
		Timeout: 2 * time.Minute,
		// The bridge is on loopback: never through a proxy from the environment.
		Transport: &http.Transport{Proxy: nil},
	}}, nil
}

// readRunToken reads the token in file, which the bridge wrote for the run.
// A token is a few dozen characters; anything longer is not one.
func readRunToken(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<10))
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || len(data) == 1<<10 {
		return "", fmt.Errorf("%s does not hold a token", file)
	}
	return token, nil
}

// call sends one request, with body as JSON when it is not nil, and
// decodes the answer into out. A refusal comes back as the bridge's error
// message.
func (c *brainClient) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the bridge at %s (is it running?): %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("reading the bridge's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("the bridge answered %s", resp.Status)
		}
		return errors.New(e.Error)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(out)
}
