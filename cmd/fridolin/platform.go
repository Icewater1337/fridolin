package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// What the bridge asks of the operating system beyond files: opening a
// browser, telling a terminal, a folder dialog and replacing its own
// process.

// openBrowser opens url in the user's browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "linux":
		return exec.Command("xdg-open", url).Run()
	}
	return fmt.Errorf("don't know how to open a browser on %s", runtime.GOOS)
}

// isTerminal reports whether f is a terminal, or at least a character
// device, so that a QR code or a login link is shown to a person.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// folderPicker returns the command that shows a native folder chooser,
// or nil when the system has none. kdialog starts in home, the user's
// home folder.
func folderPicker(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"osascript", "-e", "activate", "-e", `POSIX path of (choose folder with prompt "Choose a folder for this brain")`}
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return nil
		}
		if p, err := exec.LookPath("zenity"); err == nil {
			return []string{p, "--file-selection", "--directory", "--title=Choose a folder for this brain"}
		}
		if p, err := exec.LookPath("kdialog"); err == nil {
			return []string{p, "--getexistingdirectory", home}
		}
	}
	return nil
}

// execInPlace replaces the running process with argv0, run with argv and
// envv. It returns only when that fails.
func execInPlace(argv0 string, argv, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}
