package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
}

// coreFiles is core files export|import, run in the core's container
// (fridolin core run core files …) by a core host's agent in a move: the
// secret files of the core's credentials folder (the Google OAuth web
// client, the dashboard's users, the calendars' clients and tokens) go
// with the host secrets, sealed to the target's key. Import writes only
// files that are not there yet, 0600: a machine that ran the core before
// keeps its folder as it was. The container's home is the volume
// fridolin-home, unless a Mac's compose.host.yaml binds its own folders.
func coreFiles(args []string, env coreEnv) int {
	if len(args) != 1 || (args[0] != "export" && args[0] != "import") {
		fmt.Fprintln(env.stderr, "usage: fridolin core files export|import (in the core's container: fridolin core run core files …)")
		return 2
	}
	home := envValue(env.environ, "HOME")
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
	if len(in.Files) > 0 {
		if err := secretxfer.CheckFilesDir(in.Dir); err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files import: %v\n", err)
			return 2
		}
		stored, kept, err := secretxfer.StoreFiles(expandTilde(in.Dir, home), in.Files)
		if err != nil {
			fmt.Fprintf(env.stderr, "fridolin core files import: %v\n", err)
			return 1
		}
		res.Stored, res.Kept = append(res.Stored, stored...), append(res.Kept, kept...)
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
