package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// objectStore is what the core commands need of the vault bucket. It is
// the part of cloud.ObjectStore that lease and coreexport use; the rclone
// and folder stores here stand in until internal/cloud is wired.
type objectStore interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader) error
}

// dirStore keeps objects as files below a folder: for a test, or a
// removable disk that carries an export by hand.
type dirStore struct{ dir string }

func (s dirStore) path(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if key == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bad object key %q", key)
	}
	return filepath.Join(s.dir, clean), nil
}

func (s dirStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (s dirStore) Put(_ context.Context, key string, r io.Reader) error {
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// rcloneStore reaches a bucket through the rclone binary, as every cloud
// access of the core does (no S3 SDK in the bridge). remote is
// "name:bucket"; env holds the remote's RCLONE_CONFIG_<NAME>_* variables,
// which only rclone gets.
type rcloneStore struct {
	rclone string
	remote string
	env    []string
}

func (s rcloneStore) cmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.rclone, args...)
	cmd.Env = s.env
	return cmd
}

func (s rcloneStore) target(key string) string { return s.remote + "/" + key }

// rcloneNotFound are rclone's exit statuses for a missing directory (3)
// and file (4): `rclone cat` of a missing object exits 3 on most backends.
func rcloneNotFound(code int) bool { return code == 3 || code == 4 }

func (s rcloneStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	cmd := s.cmd(ctx, "cat", s.target(key))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(out, 64<<10)
	// An object that is missing and one that is empty both give no output:
	// rclone's exit status tells them apart.
	if _, err := br.Peek(1); err != nil {
		werr := cmd.Wait()
		if ee, ok := errors.AsType[*exec.ExitError](werr); ok && rcloneNotFound(ee.ExitCode()) {
			return nil, &fs.PathError{Op: "get", Path: key, Err: fs.ErrNotExist}
		}
		if werr != nil {
			return nil, fmt.Errorf("rclone cat %s: %v: %s", key, werr, strings.TrimSpace(stderr.String()))
		}
		return io.NopCloser(strings.NewReader("")), nil
	}
	return &rcloneReader{Reader: br, cmd: cmd, stderr: &stderr, key: key}, nil
}

type rcloneReader struct {
	io.Reader
	cmd    *exec.Cmd
	stderr *strings.Builder
	key    string
	done   bool
	err    error
}

func (r *rcloneReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) && !r.done {
		r.done = true
		if werr := r.cmd.Wait(); werr != nil {
			r.err = fmt.Errorf("rclone cat %s: %v: %s", r.key, werr, strings.TrimSpace(r.stderr.String()))
			return n, r.err
		}
	}
	if r.err != nil {
		return n, r.err
	}
	return n, err
}

func (r *rcloneReader) Close() error {
	if r.done {
		return nil
	}
	r.done = true
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
	return nil
}

func (s rcloneStore) Put(ctx context.Context, key string, r io.Reader) error {
	cmd := s.cmd(ctx, "rcat", s.target(key))
	cmd.Stdin = r
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rclone rcat %s: %v: %s", key, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// vaultRemote is the rclone remote the core commands define for the vault
// bucket, through the environment of rclone alone.
const vaultRemote = "fridolinvault"

// coreStoreFrom picks the store of the core commands: --store (a folder,
// or an rclone "remote:bucket" whose remote rclone's own config or
// environment defines), else $FRIDOLIN_CORE_STORE the same way, else the
// B2 vault bucket with the key in B2_VAULT_KEY_ID and B2_VAULT_KEY, which
// `fridolin core start` hands the container.
func coreStoreFrom(flagValue, bucket string, environ []string) (objectStore, string, error) {
	getenv := envLookup(environ)
	loc := flagValue
	if loc == "" {
		loc = getenv("FRIDOLIN_CORE_STORE")
	}
	rclone := func() (string, error) {
		path, err := exec.LookPath("rclone")
		if err != nil {
			return "", errors.New("rclone is not installed: the core reaches the vault bucket through it")
		}
		return path, nil
	}
	baseEnv := []string{"PATH=" + getenv("PATH"), "HOME=" + getenv("HOME")}
	switch {
	case loc != "" && (strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, ".")):
		return dirStore{dir: loc}, "folder " + loc, nil
	case loc != "":
		path, err := rclone()
		if err != nil {
			return nil, "", err
		}
		env := baseEnv
		for _, kv := range environ {
			if strings.HasPrefix(kv, "RCLONE_") {
				env = append(env, kv)
			}
		}
		return rcloneStore{rclone: path, remote: strings.TrimSuffix(loc, "/"), env: env}, loc, nil
	}
	id, key := getenv("B2_VAULT_KEY_ID"), getenv("B2_VAULT_KEY")
	if id == "" || key == "" {
		return nil, "", errors.New("no vault bucket: B2_VAULT_KEY_ID and B2_VAULT_KEY are not set (fridolin core start passes them into the container from the keychain); or pass --store")
	}
	path, err := rclone()
	if err != nil {
		return nil, "", err
	}
	prefix := "RCLONE_CONFIG_" + strings.ToUpper(vaultRemote) + "_"
	env := append(baseEnv, prefix+"TYPE=b2", prefix+"ACCOUNT="+id, prefix+"KEY="+key, prefix+"HARD_DELETE=false")
	return rcloneStore{rclone: path, remote: vaultRemote + ":" + bucket, env: env}, "b2 bucket " + bucket, nil
}

// envLookup returns a getenv over environ, a list of NAME=value.
func envLookup(environ []string) func(string) string {
	return func(name string) string {
		for i := len(environ) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(environ[i], name+"="); ok {
				return v
			}
		}
		return ""
	}
}
