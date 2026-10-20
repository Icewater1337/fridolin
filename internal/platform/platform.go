// Package platform is what the bridge asks of the operating system beyond
// files: the instance lock, process groups, replacing its own process on
// a restart, opening a browser, telling a terminal, a folder dialog and
// the optional tmux activity viewer.
// It builds on macOS, Linux and Windows, where some of it does less (each
// _windows.go file says what), and it is the one package below internal/
// that may read the process's environment.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// OpenBrowser opens url in the user's browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "linux":
		return exec.Command("xdg-open", url).Run()
	case "windows":
		// Not cmd /c start, which takes an & in url for its own.
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url).Run()
	}
	return fmt.Errorf("don't know how to open a browser on %s", runtime.GOOS)
}

// IsTerminal reports whether f is a terminal, or at least a character
// device, so that a QR code or a login link is shown to a person.
func IsTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// FolderPicker returns the command that shows a native folder chooser,
// or nil when the system has none, as on Windows for now, where the
// dashboard takes a typed path. kdialog starts in home, the user's home
// folder.
func FolderPicker(home string) []string {
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
