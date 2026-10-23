package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// AppData is the user's AppData folders on Windows, which Windows treats
// apart for the processes of an app package (MSIX), such as the Claude
// desktop app and every process it starts, elevated ones too: what they
// create under AppData goes to the package's own copy,
// %LOCALAPPDATA%\Packages\<family>\LocalCache\Local (and \Roaming), which
// Windows shows them in place of the real folder. Every other process, a
// service among them, sees the real folder. A package may exclude
// folders from this, or turn it off; and a process can be redirected
// without the package's identity (GetCurrentPackageFullName says "no
// package" to the shells of the Claude desktop app's Code tab), so only
// what lands where shows it.
type AppData struct {
	// Local and Roaming are %LOCALAPPDATA% and %APPDATA%.
	Local, Roaming string
}

// UserAppData is this user's AppData folders, which other systems leave
// unset.
func UserAppData() AppData {
	return AppData{Local: os.Getenv("LOCALAPPDATA"), Roaming: os.Getenv("APPDATA")}
}

// PackageCopy is where a process of the package family writes what it
// creates at path: the package's copy of AppData for a path under
// %LOCALAPPDATA% or %APPDATA%, but not in %LOCALAPPDATA%\Packages, the
// packages' own folders, which are what they are. ok is false for any
// other path.
func (a AppData) PackageCopy(family, path string) (string, bool) {
	folder, rel, ok := a.place(path)
	if family == "" || !ok {
		return "", false
	}
	return filepath.Join(a.Local, "Packages", family, "LocalCache", folder, rel), true
}

// place is the AppData folder that path lies in, Local or Roaming, and
// path relative to it; ok is false outside both, and in the packages'
// own folders.
func (a AppData) place(path string) (folder, rel string, ok bool) {
	if a.Local == "" {
		return "", "", false
	}
	if rel, ok := within(a.Local, path); ok {
		if first, _, _ := strings.Cut(rel, string(filepath.Separator)); strings.EqualFold(first, "Packages") {
			return "", "", false
		}
		return "Local", rel, true
	}
	if rel, ok := within(a.Roaming, path); ok {
		return "Roaming", rel, true
	}
	return "", "", false
}

// PackageCopies are the copies of path that processes of app packages
// made (PackageCopy), which no process outside the package sees.
func (a AppData) PackageCopies(path string) []string {
	var copies []string
	for _, family := range a.families(path) {
		if c, ok := a.PackageCopy(family, path); ok {
			if _, err := os.Stat(c); err == nil {
				copies = append(copies, c)
			}
		}
	}
	return copies
}

// SeenAsCopy is the package copy that this process sees at path, which
// must exist, in place of the real file: where it is the same file as a
// package's copy of it, this process runs inside that package, and
// processes outside it see another file there, or none.
func (a AppData) SeenAsCopy(path string) (copyPath, family string, ok bool) {
	st, err := os.Stat(path)
	if err != nil {
		return "", "", false
	}
	for _, f := range a.families(path) {
		c, _ := a.PackageCopy(f, path)
		if cst, err := os.Stat(c); err == nil && os.SameFile(st, cst) {
			return c, f, true
		}
	}
	return "", "", false
}

// WritesRedirected says whether what this process creates in dir lands in
// an app package's copy (SeenAsCopy), and where its copy of dir is: only
// a file created to find out shows it. It creates the file in dir, or in
// the nearest folder above it that exists, and removes it.
func (a AppData) WritesRedirected(dir string) (copyDir, family string, ok bool) {
	if _, _, ok := a.place(dir); !ok {
		return "", "", false
	}
	probeDir := dir
	for {
		if st, err := os.Stat(probeDir); err == nil && st.IsDir() {
			break
		}
		up := filepath.Dir(probeDir)
		if up == probeDir {
			return "", "", false
		}
		probeDir = up
	}
	f, err := os.CreateTemp(probeDir, ".fridolin-probe-*")
	if err != nil {
		return "", "", false
	}
	probe := f.Name()
	f.Close()
	defer os.Remove(probe)
	if _, family, ok = a.SeenAsCopy(probe); !ok {
		return "", "", false
	}
	copyDir, _ = a.PackageCopy(family, dir)
	return copyDir, family, true
}

// families are the app packages that may hold a copy of path: none for a
// path outside AppData.
func (a AppData) families(path string) []string {
	if _, _, ok := a.place(path); !ok {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(a.Local, "Packages"))
	if err != nil {
		return nil
	}
	var families []string
	for _, e := range entries {
		if e.IsDir() {
			families = append(families, e.Name())
		}
	}
	return families
}

// within is path relative to base, if path is base or lies below it
// (filepath.Rel, which ignores case on Windows).
func within(base, path string) (string, bool) {
	if base == "" {
		return "", false
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}
