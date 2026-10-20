package coreexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The config edits a move between a native install and the container
// makes to the imported config.json, besides FixDataDir. Each reads the
// file as JSON objects and changes only the keys it names, so a setting
// this version does not know stays; each says what it changed.

// ProgramKeys are the settings that name a program the bridge runs, which
// a native install may have pointed at a file of the Mac's.
var ProgramKeys = []string{"claude_path", "codex_path", "backup.rclone_path", "plaud.cli_path"}

// HostKeys are what a move back to the native install takes from the
// native config.json it replaces rather than from the container's: where
// the data dir and the programs are on the Mac, and the allow_paths the
// move into the container added.
var HostKeys = append([]string{"data_dir", "sandbox.allow_paths"}, ProgramKeys...)

// config is a config.json as nested JSON objects.
type config map[string]json.RawMessage

// get returns the raw value of a dotted key, nil when it is not set.
func (c config) get(key string) json.RawMessage {
	first, rest, nested := strings.Cut(key, ".")
	if !nested {
		return c[first]
	}
	var sub config
	if json.Unmarshal(c[first], &sub) != nil || sub == nil {
		return nil
	}
	return sub.get(rest)
}

// set sets a dotted key, or removes it when raw is nil.
func (c config) set(key string, raw json.RawMessage) error {
	first, rest, nested := strings.Cut(key, ".")
	if !nested {
		if raw == nil {
			delete(c, first)
		} else {
			c[first] = raw
		}
		return nil
	}
	sub := config{}
	if v, ok := c[first]; ok && string(v) != "null" {
		if err := json.Unmarshal(v, &sub); err != nil {
			return fmt.Errorf("%s is not an object: %w", first, err)
		}
	}
	if raw == nil && sub.get(rest) == nil {
		return nil
	}
	if err := sub.set(rest, raw); err != nil {
		return err
	}
	data, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	c[first] = data
	return nil
}

func (c config) str(key string) string {
	var s string
	_ = json.Unmarshal(c.get(key), &s)
	return s
}

// editConfig runs edit on dataDir's config.json and writes it back, by a
// rename, when edit changed it. No config.json is nothing to edit.
func editConfig(dataDir string, edit func(c config) ([]string, error)) ([]string, error) {
	path := filepath.Join(dataDir, "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	before, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	notes, err := edit(c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	after, err := json.Marshal(c)
	if err != nil || bytes.Equal(before, after) {
		return notes, err
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	tmp := path + ".import.tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return notes, nil
}

// ResetProgramPaths removes the program settings (ProgramKeys) of
// dataDir's config.json that name a file by its path, so that the bridge
// finds the image's own programs on PATH: a native install's paths point
// at the Mac's builds (a wrapper around the Claude app, codex under nvm),
// which do not run in the container even where a bound folder shows them.
// A bare name ("codex") is looked up on PATH and stays.
func ResetProgramPaths(dataDir string) ([]string, error) {
	return editConfig(dataDir, func(c config) ([]string, error) {
		var notes []string
		for _, key := range ProgramKeys {
			v := c.str(key)
			if !strings.ContainsRune(v, '/') {
				continue
			}
			if err := c.set(key, nil); err != nil {
				return nil, err
			}
			notes = append(notes, fmt.Sprintf("%s was %s, a program of the Mac: removed, so the bridge runs the image's own from its PATH", key, v))
		}
		return notes, nil
	})
}

// AddAllowPaths adds paths to sandbox.allow_paths of dataDir's
// config.json: folders of the Mac bound into the container read-only that
// brains read there as they did on the Mac, where reading is allowed by
// default and in the container only what is bound into a run.
func AddAllowPaths(dataDir string, paths []string) ([]string, error) {
	return editConfig(dataDir, func(c config) ([]string, error) {
		var have []string
		if raw := c.get("sandbox.allow_paths"); raw != nil {
			if err := json.Unmarshal(raw, &have); err != nil {
				return nil, fmt.Errorf("sandbox.allow_paths: %w", err)
			}
		}
		var notes []string
		for _, p := range paths {
			if !slices.Contains(have, p) {
				have = append(have, p)
				notes = append(notes, fmt.Sprintf("sandbox.allow_paths: added %s, which brains read on the Mac and the container binds read-only", p))
			}
		}
		if len(notes) == 0 {
			return nil, nil
		}
		raw, err := json.Marshal(have)
		if err != nil {
			return nil, err
		}
		return notes, c.set("sandbox.allow_paths", raw)
	})
}

// ForRehearsal turns off, in dataDir's config.json, what would reach past
// a rehearsal of a move while the native install still runs on the same
// accounts: brains' runs (claude_path and codex_path become /usr/bin/false,
// so a run fails at once and spends nothing, and no login is refreshed
// under the native install), Plaud, the Google Drive backup, the lease, the
// tailnet, nodes and the vault.
func ForRehearsal(dataDir string) ([]string, error) {
	return editConfig(dataDir, func(c config) ([]string, error) {
		var notes []string
		set := func(key string, v any) error {
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			notes = append(notes, fmt.Sprintf("rehearsal: %s = %s", key, raw))
			return c.set(key, raw)
		}
		for _, key := range []string{"claude_path", "codex_path"} {
			if err := set(key, "/usr/bin/false"); err != nil {
				return nil, err
			}
		}
		for _, key := range []string{"plaud.enabled", "backup.enabled", "core.lease.enabled", "core.tailnet.enabled", "nodes.enabled", "vault.enabled"} {
			if c.get(key) == nil {
				continue
			}
			if err := set(key, false); err != nil {
				return nil, err
			}
		}
		return notes, nil
	})
}

// RestoreHostKeys sets HostKeys in dataDir's config.json to their values in
// native, the config.json of the native install this copy replaces (a key
// native does not set is removed): the container's /data, its programs
// and the allow_paths a move added mean nothing on the Mac.
func RestoreHostKeys(dataDir string, native []byte) ([]string, error) {
	var old config
	if err := json.Unmarshal(native, &old); err != nil {
		return nil, fmt.Errorf("the native config.json: %w", err)
	}
	return editConfig(dataDir, func(c config) ([]string, error) {
		var notes []string
		for _, key := range HostKeys {
			was, now := c.get(key), old.get(key)
			if bytes.Equal(was, now) {
				continue
			}
			if err := c.set(key, now); err != nil {
				return nil, err
			}
			if now == nil {
				notes = append(notes, fmt.Sprintf("%s: removed, as in the native config", key))
			} else {
				notes = append(notes, fmt.Sprintf("%s: %s, from the native config", key, now))
			}
		}
		return notes, nil
	})
}
