package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Icewater1337/fridolin/internal/platform"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// nvmNodeVersions is where nvm installs node versions, relative to HOME.
const nvmNodeVersions = ".nvm/versions/node"

// findPlaudCLI finds the plaud binary, given plaud.cli_path. The sandbox
// profile uses it too, to keep brains from replacing the CLI (see
// bridgeProgramsOf). Without cli_path it looks only in the PATH folders
// helperPath keeps, since the CLI runs outside the sandbox, then in nvm
// and the usual prefixes.
func findPlaudCLI(c string) (string, error) {
	if c != "" {
		if _, err := os.Stat(c); err != nil {
			return "", fmt.Errorf("plaud.cli_path %s: %w", c, err)
		}
		return c, nil
	}
	if found := lookPathIn("plaud", helperPath()); found != "" {
		return found, nil
	}
	candidates, _ := filepath.Glob(filepath.Join(HomeDir(), nvmNodeVersions, "*/bin/plaud"))
	// The newest node first: by version, not by name, which would put
	// v20.9.0 before v20.10.0, and v9 before v24.
	slices.SortStableFunc(candidates, func(a, b string) int {
		return -compareNodeVersions(filepath.Base(filepath.Dir(filepath.Dir(a))), filepath.Base(filepath.Dir(filepath.Dir(b))))
	})
	candidates = append(candidates, "/opt/homebrew/bin/plaud", "/usr/local/bin/plaud")
	for _, c := range candidates {
		if fsx.Exists(c) {
			return c, nil
		}
	}
	return "", errors.New("the Plaud CLI is not installed")
}

// cliNodeDir is the folder of the Plaud CLI at bin when that also holds a
// node, as nvm installs them: the node the CLI was installed with. ""
// otherwise.
func cliNodeDir(bin string) string {
	if dir := filepath.Dir(bin); filepath.IsAbs(dir) && lookPathIn("node", dir) != "" {
		return filepath.Clean(dir)
	}
	return ""
}

// plaudOptOut keeps Plaud's CLI from sending usage statistics to Plaud's
// analytics and from asking npm for updates. Fridolin's own runs get it,
// and so does every brain's run: with plaud.brain_access brains run the
// CLI themselves, and the bridge's environment has neither variable. It
// names Plaud's own variables, not DO_NOT_TRACK, which other tools in a
// brain's run read too, Claude Code among them.
var plaudOptOut = []string{"PLAUD_TELEMETRY_DISABLED=1", "PLAUD_NO_UPDATE_NOTIFIER=1"}

// compareNodeVersions compares two nvm version folder names ("v20.10.0")
// number by number. A name that is not a version sorts before every one.
func compareNodeVersions(a, b string) int {
	parse := func(v string) []int {
		var n []int
		for _, part := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
			i, err := strconv.Atoi(part)
			if err != nil {
				return nil
			}
			n = append(n, i)
		}
		return n
	}
	return slices.Compare(parse(a), parse(b))
}

// plaudCLIVersion reads the version of the CLI at bin from the package.json
// of the npm package it belongs to, without running it: `plaud version`
// asks npm for updates. "" means it cannot tell (a wrapper script, say).
func plaudCLIVersion(bin string) string {
	pkg := nodePackage(fsx.Resolve(bin))
	if pkg == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(pkg, "package.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &m) != nil || m.Name != "@plaud-ai/cli" {
		return ""
	}
	return m.Version
}

// plaudPATH is the PATH the Plaud CLI at bin runs with, outside the
// sandbox, where its #!/usr/bin/env node looks node up: its own folder
// first when that holds a node, as nvm installs them, so that it gets the
// one it was installed with; then the folders of the bridge's PATH that
// helperPath keeps, then helperDirs for a PATH that lacks them. Never the
// whole PATH: a node a brain planted in a folder of it outside HOME would
// run outside the sandbox. A CLI folder without a node comes last, and
// still comes: the sandbox keeps brains from planting the names the bridge
// runs in the folders of this PATH under HOME (see plantablePrograms), so
// that none can put a node there that would move the folder first.
func plaudPATH(bin string) string {
	var dirs []string
	first := cliNodeDir(bin)
	if first != "" {
		dirs = append(dirs, first)
	}
	rest := append(filepath.SplitList(helperPath()), helperDirs...)
	if dir := filepath.Dir(bin); first == "" && filepath.IsAbs(dir) {
		rest = append(rest, filepath.Clean(dir))
	}
	for _, dir := range rest {
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return strings.Join(dirs, string(filepath.ListSeparator))
}

// androidToolchain finds a JDK and the Android SDK to build the app with
// on this machine (platform.FindAndroidToolchain): JAVA_HOME and
// ANDROID_HOME (or ANDROID_SDK_ROOT) of the bridge's environment, else
// where macOS, Homebrew and Android Studio put them.
func androidToolchain() (javaHome, sdk string, err error) {
	tc, err := platform.FindAndroidToolchain(platform.AndroidLookup{Home: HomeDir()})
	if err != nil {
		return "", "", err
	}
	return tc.JavaHome, tc.SDK, nil
}

// plaudAppSource is plaud-android/ of the checkout the bridge was built in
// or installed from, else the one beside the binary: the fridolin-core
// image keeps it in /opt/fridolin/plaud-android, to send it to a node that
// builds it (docs/PLAUD.md).
func plaudAppSource() (string, error) {
	l := ownLayout()
	for _, dir := range []string{l.Source, l.Dir} {
		if dir == "" {
			continue
		}
		if src := filepath.Join(dir, "plaud-android"); fsx.Exists(filepath.Join(src, "gradlew")) {
			return src, nil
		}
	}
	return "", errors.New("the app's source is not here: Fridolin builds it from plaud-android/ in the checkout it was installed from")
}
