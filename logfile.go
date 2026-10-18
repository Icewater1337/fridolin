package main

import (
	"fmt"
	"os"
)

// maxLogBytes is how large the log file may grow before it is rotated. One
// older file is kept, so the logs take at most twice this.
const maxLogBytes = 10 << 20

// rotatingLog is the log file launchd opens as the bridge's stderr
// (StandardErrorPath), which would otherwise grow for as long as the
// service runs, naming chats and senders all the way. Past max bytes it
// moves to <path>.1, replacing the one before, and a new file takes its
// place on the descriptor launchd gave the bridge, so what the Go runtime
// writes there itself (a panic, say) lands in the new file too.
type rotatingLog struct {
	path string
	fd   int // the descriptor that must follow the file: stderr
	max  int64
	f    *os.File
	size int64
}

// openRotatingLog takes over fd with the file at path. Writes are not
// synchronised: the logger serialises its own.
func openRotatingLog(path string, fd int, max int64) (*rotatingLog, error) {
	r := &rotatingLog{path: path, fd: fd, max: max}
	if err := r.reopen(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingLog) reopen() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// launchd creates it with the umask's mode.
	err = f.Chmod(0o600)
	var st os.FileInfo
	if err == nil {
		st, err = f.Stat()
	}
	if err == nil {
		err = dup2(int(f.Fd()), r.fd)
	}
	if err != nil {
		f.Close()
		return fmt.Errorf("opening the log %s: %w", r.path, err)
	}
	if r.f != nil {
		r.f.Close()
	}
	r.f, r.size = f, st.Size()
	return nil
}

func (r *rotatingLog) rotate() error {
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return r.reopen()
}

func (r *rotatingLog) Write(p []byte) (int, error) {
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			// Keep logging to the old file rather than lose the line.
			fmt.Fprintf(r.f, "rotating the log: %v\n", err)
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}
