package nodeagent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// sbx's side on the host, as each system has it (docs/NODES.md, "FrIDE
// instances on Windows"). On macOS and Linux the agent runs as the user
// whose sbx it drives. On Windows the node's service runs as LocalSystem
// in session 0, while sbx and its VMs are the signed-in user's: there
// every sbx call runs in that user's session, as that user
// (sbx_windows.go), the token files sbx reads and the folders sbx cp
// writes are that user's and SYSTEM's alone, and sbx reads a token with
// the node's own fridolin.exe instead of /bin/cat. These are the helpers
// that do not need Windows to run, so that every system tests them.

// sbxSecretArgs are the hidden subcommand sbx runs on Windows to read a
// token file: fridolin node fride host sbx-secret FILE.
var sbxSecretArgs = []string{"node", "fride", "host", "sbx-secret"}

// sbxSecretCommandFor is the host command sbx runs to read the token in
// file (sbx secret set-custom --command) on goos: /bin/cat on macOS and
// Linux; on Windows exe (the node's fridolin.exe) with sbx-secret, its
// paths with forward slashes, which Windows takes and sbx's word
// splitting leaves alone, quoted only when they hold a space. exe and
// file should be short (8.3) paths there, which hold none.
func sbxSecretCommandFor(goos, exe, file string) (string, error) {
	if strings.ContainsAny(file+exe, "'\"\n\r\x00") {
		return "", fmt.Errorf("the path %s has a quote or line break, which sbx's command cannot hold", file)
	}
	if goos != "windows" {
		return "/bin/cat '" + file + "'", nil
	}
	if exe == "" {
		return "", errors.New("the node's own program is not known: sbx reads the tokens with it")
	}
	word := func(p string) string {
		p = strings.ReplaceAll(p, `\`, "/")
		if strings.ContainsAny(p, " \t") {
			return "'" + p + "'"
		}
		return p
	}
	return word(exe) + " " + strings.Join(sbxSecretArgs, " ") + " " + word(file), nil
}

// sbxSecretFileOf is the token file in a command sbxSecretCommandFor
// made, for the tests' fake sbx and the live test: "" when it is not
// one.
func sbxSecretFileOf(cmd string) string {
	if f, ok := strings.CutPrefix(cmd, "/bin/cat '"); ok {
		if f, ok := strings.CutSuffix(f, "'"); ok {
			return f
		}
		return ""
	}
	_, f, ok := strings.Cut(cmd, " "+strings.Join(sbxSecretArgs, " ")+" ")
	if !ok || f == "" {
		return ""
	}
	if s, ok := strings.CutPrefix(f, "'"); ok {
		f, _ = strings.CutSuffix(s, "'")
	}
	return filepath.FromSlash(f)
}

// maxSbxSecret bounds a token file sbx-secret prints.
const maxSbxSecret = 8192

// PrintSbxSecret writes the token in file to w, for sbx: `fridolin node
// fride host sbx-secret FILE`, which sbx runs on Windows as the
// signed-in user to read a git host's token (sbx_tokens.go). It prints
// only a token file of an instance's.
func PrintSbxSecret(w io.Writer, file string) error {
	if !strings.HasSuffix(file, ".token") || filepath.Base(filepath.Dir(file)) != frideInstanceTokens {
		return fmt.Errorf("%s is not a token file of a FrIDE instance's", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSbxSecret+1))
	if err != nil {
		return err
	}
	if len(b) > maxSbxSecret {
		return fmt.Errorf("%s is too big for a token", file)
	}
	_, err = w.Write(b)
	return err
}

// sbxFolderSDDL is the security descriptor of a folder sbx reads or
// writes on Windows: SYSTEM (the node's service) and the user sid (who
// runs sbx) have it, and its files and folders, alone; nothing is
// inherited from above.
func sbxFolderSDDL(sid string) string {
	return "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + sid + ")"
}

// windowsEnv is the value of name in env, as Windows looks it up: the
// case of the name does not matter.
func windowsEnv(env []string, name string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// windowsPathList splits a Windows PATH: by semicolons, a part in double
// quotes kept whole, empty parts dropped.
func windowsPathList(list string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if p := strings.TrimSpace(cur.String()); p != "" {
			out = append(out, p)
		}
		cur.Reset()
	}
	for _, r := range list {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ';' && !quoted:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// windowsSbxCandidates are where sbx.exe may be for a user whose
// environment is env, in order: on their PATH, then the folders winget
// links its programs into (the user's and the machine's), which a
// service's or an older shell's PATH may lack.
func windowsSbxCandidates(env []string) []string {
	join := func(dir string, parts ...string) string {
		return strings.TrimRight(dir, `\/`) + `\` + strings.Join(parts, `\`)
	}
	var out []string
	for _, d := range windowsPathList(windowsEnv(env, "PATH")) {
		out = append(out, join(d, "sbx.exe"))
	}
	if d := windowsEnv(env, "LOCALAPPDATA"); d != "" {
		out = append(out, join(d, "Microsoft", "WinGet", "Links", "sbx.exe"))
	}
	if d := windowsEnv(env, "ProgramFiles"); d != "" {
		out = append(out, join(d, "WinGet", "Links", "sbx.exe"))
	}
	return out
}

// FindSbx is the sbx program of the user whose environment is env, on
// Windows (windowsSbxCandidates): "" when none is there.
func FindSbx(env []string) string {
	for _, p := range windowsSbxCandidates(env) {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// sbxSupported reports whether goos and arch run Docker Sandboxes:
// macOS, Linux, and Windows on x64 (Windows 11 with the Windows
// Hypervisor Platform; sbx has no build for Windows on Arm).
func sbxSupported(goos, arch string) bool {
	return goos == "darwin" || goos == "linux" || goos == "windows" && arch == "amd64"
}

// sbxOS reports whether this system runs Docker Sandboxes.
func sbxOS() bool { return sbxSupported(runtime.GOOS, runtime.GOARCH) }

// SbxUnsupported says why this system runs no Docker Sandboxes, "" when
// it does.
func SbxUnsupported() string {
	if sbxOS() {
		return ""
	}
	if runtime.GOOS == "windows" {
		return "Docker Sandboxes run on Windows 11 on x64, not on " + runtime.GOARCH
	}
	return "Docker Sandboxes run on macOS, Linux with KVM and Windows 11 on x64, not on " + runtime.GOOS
}

// SbxInstallHint is how to install sbx on this system.
func SbxInstallHint() string {
	switch runtime.GOOS {
	case "windows":
		return "winget install -h Docker.sbx (as the user who signs in to Windows), then sbx login"
	case "darwin":
		return "brew install --cask sbx, then sbx login"
	}
	return "install Docker Sandboxes (docs.docker.com/ai/sandboxes), then sbx login"
}

// sbxNotInstalled is the problem of a node without sbx.
func sbxNotInstalled() string {
	where := "on the PATH, /opt/homebrew/bin, /usr/local/bin"
	if runtime.GOOS == "windows" {
		where = `on the signed-in user's PATH or in WinGet\Links`
	}
	return "sbx is not installed (or not where the agent looks: " + where + "): " + SbxInstallHint()
}
