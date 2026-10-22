package nodeagent

import "errors"

// ptyProc is a shell on a pseudo-terminal: a PTY on macOS and Linux
// (pty_unix.go), a ConPTY on Windows (pty_windows.go).
type ptyProc interface {
	// Read reads its output; errEndOfOutput or io.EOF once it ended.
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Resize(cols, rows int) error
	// Kill ends it and what it started.
	Kill()
	// Wait waits for it to end and returns its exit code.
	Wait() int
}

var errEndOfOutput = errors.New("the terminal ended")
