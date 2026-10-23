package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/nodeagent"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// packageDoc is where docs/NODES.md says what to do inside an app package.
const packageDoc = `docs/NODES.md, "Inside an app package"`

// appPackage is what Windows does with what this process writes under
// AppData, which it keeps in an app package's own copy when the process
// runs inside the package (MSIX), as a coding agent's commands in the
// Claude desktop app do (platform.AppData). Tests set their own.
type appPackage struct {
	appData platform.AppData
	home    string
	// redirected is appData.WritesRedirected, seenAsCopy appData.SeenAsCopy.
	redirected func(dir string) (copyDir, family string, ok bool)
	seenAsCopy func(path string) (copyPath, family string, ok bool)
}

func thisAppPackage() appPackage {
	a := platform.UserAppData()
	home, _ := os.UserHomeDir()
	return appPackage{appData: a, home: home, redirected: a.WritesRedirected, seenAsCopy: a.SeenAsCopy}
}

// outsideDir is a state dir outside AppData, which a package's processes
// and the service see alike: in the home folder, which only its owner,
// SYSTEM and the administrators may read.
func (p appPackage) outsideDir() string {
	return filepath.Join(p.home, ".fridolin-node")
}

// writeError says why the node service could not use what this process
// writes into dir, nil where it could.
func (p appPackage) writeError(dir string) error {
	if p.redirected == nil {
		return nil
	}
	copyDir, family, ok := p.redirected(dir)
	if !ok {
		return nil
	}
	return fmt.Errorf("this process runs inside the app package %s (as commands in the Claude desktop app do), so Windows keeps what it writes in %s in the package's own copy, %s; %s. Run this from a PowerShell started outside the app (from the Start menu), or give it a state dir outside AppData, and the same one to every later fridolin node command: --state-dir \"%s\" (%s)",
		family, dir, copyDir, unseen, p.outsideDir(), packageDoc)
}

// unseen is why a package's copy is no use to the node service.
const unseen = "the node service runs as LocalSystem outside the package and never sees that copy, so it would stop at once, and Windows would say only error 1053"

// serviceError says why a service installed from this process could not
// use dir, nil where it sees what this process sees.
func (p appPackage) serviceError(dir string) error {
	copyPath, family, ok := p.ownCopy(dir)
	if !ok {
		return nil
	}
	return fmt.Errorf("%s in %s is, as this process sees it, the app package %s's own copy, %s, because this process runs inside that package (as commands in the Claude desktop app do); %s. Install from a PowerShell started outside the app (from the Start menu); or, if that copy holds the node you mean, copy %s and %s from %s to a folder outside AppData and install with that one: --state-dir \"%s\" (%s)",
		nodeagent.ConfigFile, dir, family, copyPath, unseen, nodeagent.ConfigFile, nodeagent.KeyFile, filepath.Dir(copyPath), p.outsideDir(), packageDoc)
}

// ownCopy is the package's copy of dir's node.json, where that is what
// this process sees in dir.
func (p appPackage) ownCopy(dir string) (copyPath, family string, ok bool) {
	if p.seenAsCopy == nil {
		return "", "", false
	}
	return p.seenAsCopy(filepath.Join(dir, nodeagent.ConfigFile))
}

// strayCopies says where the node's files went when dir has none: into
// the copies of dir that app packages keep (platform.AppData.PackageCopies).
func (p appPackage) strayCopies(dir string) string {
	var with []string
	for _, c := range p.appData.PackageCopies(dir) {
		if _, err := os.Stat(filepath.Join(c, nodeagent.ConfigFile)); err == nil {
			with = append(with, c)
		}
	}
	if len(with) == 0 {
		return ""
	}
	return fmt.Sprintf("%s lies in %s instead: fridolin joined from inside an app package, and Windows keeps what such a process writes under AppData in the package's own copy, which only the package sees",
		nodeagent.ConfigFile, strings.Join(with, " and "))
}

// serviceDirProblem says what keeps the installed service from the state
// dir it runs with (nodeagent.ServiceStateDir), "" for nothing it can
// tell: no node.json there, so that the service stops before it answers
// Windows, which says only error 1053 (event 7009); or one that only this
// process's app package sees.
func (p appPackage) serviceDirProblem(serviceDir string) (detail, fix string) {
	if serviceDir == "" {
		return "", ""
	}
	if copyPath, family, ok := p.ownCopy(serviceDir); ok {
		return fmt.Sprintf("the service runs with --state-dir %s, where %s, as this process sees it, is the app package %s's own copy (%s), which the service does not see; then it stops at once, and Windows says only error 1053",
				serviceDir, nodeagent.ConfigFile, family, copyPath),
			fmt.Sprintf("check %s from a PowerShell started outside the app; or copy %s and %s from %s to %s, then fridolin node uninstall, fridolin node install --state-dir \"%s\" and fridolin node start, elevated (%s)",
				serviceDir, nodeagent.ConfigFile, nodeagent.KeyFile, filepath.Dir(copyPath), p.outsideDir(), p.outsideDir(), packageDoc)
	}
	_, err := os.Stat(filepath.Join(serviceDir, nodeagent.ConfigFile))
	if !errors.Is(err, fs.ErrNotExist) {
		return "", ""
	}
	detail = fmt.Sprintf("the service runs with --state-dir %s, which has no %s: it stops as soon as Windows starts it, and Windows says only error 1053 (event 7009)", serviceDir, nodeagent.ConfigFile)
	if stray := p.strayCopies(serviceDir); stray != "" {
		return detail + "; " + stray,
			fmt.Sprintf("from a PowerShell started outside the app, copy %s and %s from there to %s, then fridolin node start (elevated); inside the app, copy them to %s instead and reinstall the service with --state-dir \"%s\" (%s)",
				nodeagent.ConfigFile, nodeagent.KeyFile, serviceDir, p.outsideDir(), p.outsideDir(), packageDoc)
	}
	return detail, "reinstall the service with the state dir that holds node.json: fridolin node uninstall, then fridolin node install --state-dir DIR and fridolin node start, elevated; or join again with --state-dir " + serviceDir
}

// samePath says whether two Windows paths name one folder, whatever their
// case.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
