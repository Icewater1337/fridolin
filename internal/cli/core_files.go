package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/platform"
	"github.com/Icewater1337/fridolin/internal/secretxfer"
)

// secretFiles is what core files export prints and core files import
// reads: the secret files of the core's credentials folder (#187), and
// the folder as config names it under ~ (sandbox.credentials_dir), so
// that the target puts them where the moved config looks.
type secretFiles struct {
	Dir   string            `json:"dir"`
	Files map[string][]byte `json:"files"`
}

// secretFilesResult is what core files import says.
type secretFilesResult struct {
	Stored []string `json:"stored"`
	Kept   []string `json:"kept"`
	// Replaced are the local files that were there and differed: the
	// moving core's replace them.
	Replaced []string `json:"replaced,omitempty"`
}

// coreFiles is core files export|import, run in the core's container
// (fridolin core run core files …) by a core host's agent in a move: the
// secret files of the core's credentials folder (the Google OAuth web
// client, the dashboard's users, the calendars' clients and tokens) go
// with the host secrets, sealed to the target's key. Import writes only
// files that are not there yet, 0600: a machine that ran the core before
// keeps its folder as it was. The container's home is the volume
// fridolin-home, unless a Mac's compose.host.yaml binds its own folders.
//
// The local files of the data dir (core/local-secrets: the GitLab
// connection, which no export takes) go along as "local:<name>", and
// import writes them there, replacing one that differs.
//
// Forget removes the files named on stdin that a move wrote (#266): the
// agent of a former core host sends only those its record names.
func coreFiles(args []string, env coreEnv) int {
	if len(args) != 1 || (args[0] != "export" && args[0] != "import" && args[0] != "forget") {
		fmt.Fprintln(env.stderr, "usage: fridolin core files export|import|forget (in the core's container: fridolin core run core files …)")
		return 2
	}
	home := envValue(env.environ, "HOME")
	dataDir, derr := insideDataDir(insideFlags{}, env)
	if args[0] == "forget" {
		return coreFilesForget(home, dataDir, derr, env)
	}
	if args[0] == "export" {
		out := secretFiles{Files: map[string][]byte{}}
		dir, err := credentialsDir(env, home)
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files export: %v\n", err)
			return 1
		}
		if dir != "" {
			if out.Files, err = secretxfer.ReadFiles(expandTilde(dir, home)); err != nil {
				fmt.Fprintf(env.stderr, "fridolin core files export: %v\n", err)
				return 1
			}
			out.Dir = dir
		}
		// The local files of the data dir (the GitLab connection), which
		// no export takes: the move carries them instead.
		if derr == nil {
			local, err := secretxfer.ReadLocalFiles(dataDir)
			if err != nil {
				fmt.Fprintf(env.stderr, "fridolin core files export: %v\n", err)
				return 1
			}
			maps.Copy(out.Files, local)
		}
		if err := json.NewEncoder(env.stdout).Encode(out); err != nil {
			return 1
		}
		return 0
	}
	var in secretFiles
	if env.stdin == nil {
		fmt.Fprintln(env.stderr, "fridolin core files import: nothing on stdin")
		return 2
	}
	if err := json.NewDecoder(io.LimitReader(env.stdin, 1<<20)).Decode(&in); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core files import: %v\n", err)
		return 2
	}
	defer func() {
		for k := range in.Files {
			clear(in.Files[k])
		}
	}()
	res := secretFilesResult{Stored: []string{}, Kept: []string{}}
	// A failure still prints what was written, so the move records it
	// (and forget can remove it) even though the import did not finish.
	failed := func(err error) int {
		fmt.Fprintf(env.stderr, "fridolin core files import: %v\n", err)
		_ = json.NewEncoder(env.stdout).Encode(res)
		return 1
	}
	creds, local := secretxfer.SplitFiles(in.Files)
	if len(creds) > 0 {
		if err := secretxfer.CheckFilesDir(in.Dir); err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files import: %v\n", err)
			return 2
		}
		stored, kept, err := secretxfer.StoreFiles(expandTilde(in.Dir, home), creds)
		res.Stored, res.Kept = append(res.Stored, stored...), append(res.Kept, kept...)
		if err != nil {
			return failed(err)
		}
	}
	if len(local) > 0 {
		if derr != nil {
			return failed(fmt.Errorf("the data dir: %w", derr))
		}
		// Into core/, which the import that follows keeps.
		stored, replaced, err := secretxfer.StoreLocalFiles(dataDir, local)
		res.Stored, res.Replaced = append(res.Stored, stored...), replaced
		if err != nil {
			return failed(err)
		}
	}
	_ = json.NewEncoder(env.stdout).Encode(res)
	return 0
}

// credentialsDir is the core's sandbox.credentials_dir under ~ ("~/x"),
// read from its config as written; "" when it is unset or in the data
// dir, whose files travel in the export anyway.
func credentialsDir(env coreEnv, home string) (string, error) {
	path := envValue(env.environ, "FRIDOLIN_CONFIG")
	if path == "" {
		path = "/data/config.json"
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var c struct {
		Sandbox struct {
			CredentialsDir string `json:"credentials_dir"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	dir := strings.TrimRight(strings.TrimSpace(c.Sandbox.CredentialsDir), "/")
	if home != "" && strings.HasPrefix(dir, home+"/") {
		dir = "~/" + strings.TrimPrefix(dir, home+"/")
	}
	if dir == "" || !strings.HasPrefix(dir, "~/") {
		return "", nil
	}
	return dir, secretxfer.CheckFilesDir(dir)
}

func expandTilde(dir, home string) string {
	return filepath.Join(home, strings.TrimPrefix(dir, "~/"))
}

// coreFilesForget is core files forget: it reads {"dir": "~/x", "names":
// [...]} on stdin and removes each named secret file directly in that
// folder, a regular file only (never through a link), printing those it
// removed as ~/x/name. A name a move never carries is refused; one not
// there is skipped.
func coreFilesForget(home, dataDir string, derr error, env coreEnv) int {
	var in struct {
		Dir   string   `json:"dir"`
		Names []string `json:"names"`
	}
	if env.stdin == nil {
		fmt.Fprintln(env.stderr, "fridolin core files forget: nothing on stdin")
		return 2
	}
	if err := json.NewDecoder(io.LimitReader(env.stdin, 64<<10)).Decode(&in); err != nil {
		fmt.Fprintf(env.stderr, "fridolin core files forget: %v\n", err)
		return 2
	}
	var names, local []string
	for _, n := range in.Names {
		switch {
		case secretxfer.LocalFileName(n):
			local = append(local, n)
		case secretxfer.FileName(n):
			names = append(names, n)
		default:
			fmt.Fprintf(env.stderr, "fridolin core files forget: %q is no secret file a move carries\n", n)
			return 2
		}
	}
	if err := secretxfer.CheckFilesDir(in.Dir); err != nil && len(names) > 0 {
		fmt.Fprintf(env.stderr, "fridolin core files forget: %v\n", err)
		return 2
	}
	removed := []string{}
	// The local files a move wrote into the data dir's core/local-secrets.
	if len(local) > 0 {
		if derr != nil {
			fmt.Fprintf(env.stderr, "fridolin core files forget: the data dir: %v\n", derr)
			return 1
		}
		// Never under a core that runs on the data dir.
		l, err := platform.AcquireLock(config.Layout{Root: dataDir}.Lock())
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files forget: %v: a core runs on %s; its local files stay\n", err, dataDir)
			return 1
		}
		defer l.Release()
		gone, err := secretxfer.RemoveLocalFiles(dataDir, local)
		removed = append(removed, gone...)
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files forget: %v\n", err)
			return 1
		}
	}
	dir := expandTilde(in.Dir, home)
	for _, n := range names {
		p := filepath.Join(dir, n)
		st, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files forget: %v\n", err)
			return 1
		}
		if !st.Mode().IsRegular() {
			fmt.Fprintf(env.stderr, "fridolin core files forget: %s/%s is not a regular file: left as it is\n", in.Dir, n)
			continue
		}
		if err := os.Remove(p); err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files forget: %v\n", err)
			return 1
		}
		removed = append(removed, in.Dir+"/"+n)
	}
	_ = json.NewEncoder(env.stdout).Encode(map[string][]string{"removed": removed})
	return 0
}
