package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
