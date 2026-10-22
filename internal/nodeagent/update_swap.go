package nodeagent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// swapBinary puts newPath in place of exe and keeps exe as exe.prev.
// Windows does not let a running .exe be replaced or deleted, but lets it
// be renamed: the running one moves aside to .prev, then the new one
// moves in. Elsewhere .prev is a hard link (or a copy) of exe, and the
// new one is renamed over exe, so exe is there at every moment.
func swapBinary(exe, newPath string) error {
	return swapBinaryOS(runtime.GOOS, exe, newPath)
}

func swapBinaryOS(goos, exe, newPath string) error {
	prev := exe + ".prev"
	if err := removeOrAside(prev); err != nil {
		return err
	}
	if goos == "windows" {
		if err := os.Rename(exe, prev); err != nil {
			return fmt.Errorf("moving the running binary aside: %w", err)
		}
		if err := os.Rename(newPath, exe); err != nil {
			if rerr := os.Rename(prev, exe); rerr != nil {
				return fmt.Errorf("moving the new binary in: %w; and moving the old one back: %v", err, rerr)
			}
			return fmt.Errorf("moving the new binary in: %w", err)
		}
		return nil
	}
	if err := os.Link(exe, prev); err != nil {
		if err := copyFile(exe, prev); err != nil {
			return fmt.Errorf("keeping the old binary: %w", err)
		}
	}
	return os.Rename(newPath, exe)
}

// rollbackBinary puts exe.prev back in place of exe. On Windows the
// running exe moves aside to exe.failed first.
func rollbackBinary(exe string) error {
	return rollbackBinaryOS(runtime.GOOS, exe)
}

func rollbackBinaryOS(goos, exe string) error {
	prev := exe + ".prev"
	if _, err := os.Stat(prev); err != nil {
		return fmt.Errorf("no previous binary: %w", err)
	}
	if goos == "windows" {
		failed := exe + ".failed"
		if err := removeOrAside(failed); err != nil {
			return err
		}
		if err := os.Rename(exe, failed); err != nil {
			return fmt.Errorf("moving the running binary aside: %w", err)
		}
		if err := os.Rename(prev, exe); err != nil {
			_ = os.Rename(failed, exe)
			return fmt.Errorf("moving the previous binary back: %w", err)
		}
		return nil
	}
	return os.Rename(prev, exe)
}

// removeOrAside removes path; a file Windows will not delete, a binary a
// process still runs, moves aside instead, for cleanAside to remove later.
func removeOrAside(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if rerr := os.Rename(path, fmt.Sprintf("%s.old-%d", path, time.Now().UnixNano())); rerr != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	return nil
}

// cleanAside removes what removeOrAside and a rollback left beside exe,
// once nothing runs it: at the agent's start outside a trial.
func cleanAside(exe string) {
	for _, pat := range []string{exe + ".prev.old-*", exe + ".failed", exe + ".failed.old-*", filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".new-*")} {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
	}
	return err
}
