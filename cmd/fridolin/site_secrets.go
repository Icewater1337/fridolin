package main

// Secrets of site modules (docs/SITE.md, "Secrets"). A module declares the
// names it needs in capabilities.secrets; their values are set in the
// dashboard and kept in <data_dir>/site/secrets/<module>.json, which the
// site's sandbox keeps from module code. When the live site starts, Fridolin
// hands it, through a pipe, the values each module declares, and nothing
// else; the site gives every module only its own, through ctx.secret(name).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/config"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/sandbox"
)

var (
	// siteModuleIDRE is module.json's rule for an id (modules.ts ID_RE).
	siteModuleIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	// siteSecretNameRE is module.json's rule for a secret's name (modules.ts SECRET_RE).
	siteSecretNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
)

const siteSecretMaxLen = 16 * 1024

func (m *SiteManager) secretsFile(module string) string { return m.dir("secrets", module+".json") }

func checkSecretKey(module, name string) error {
	if !siteModuleIDRE.MatchString(module) {
		return fmt.Errorf("%q is not a module id", module)
	}
	if !siteSecretNameRE.MatchString(name) {
		return fmt.Errorf("%q: a secret's name is a letter, then up to 63 letters, digits, _ or -", name)
	}
	return nil
}

// loadSecretsLocked reads one module's secrets; a missing file is none.
func (m *SiteManager) loadSecretsLocked(module string) (map[string]string, error) {
	data, err := os.ReadFile(m.secretsFile(module))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var v map[string]string
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", m.secretsFile(module), err)
	}
	if v == nil {
		v = map[string]string{}
	}
	return v, nil
}

func (m *SiteManager) saveSecretsLocked(module string, v map[string]string) error {
	dir := m.dir("secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if len(v) == 0 {
		if err := os.Remove(m.secretsFile(module)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, _ := json.MarshalIndent(v, "", "  ")
	return fsx.WriteFileAtomic(m.secretsFile(module), append(data, '\n'), 0o600, false)
}

// SetSecret saves a module's secret. The live site gets it when it next
// starts; the dashboard restarts it.
func (m *SiteManager) SetSecret(module, name, value string) error {
	if err := checkSecretKey(module, name); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return errors.New("enter the secret's value")
	case len(value) > siteSecretMaxLen:
		return fmt.Errorf("a secret is at most %d KB", siteSecretMaxLen/1024)
	case !utf8.ValidString(value):
		return errors.New("a secret must be text (UTF-8)")
	}
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	v, err := m.loadSecretsLocked(module)
	if err != nil {
		return err
	}
	v[name] = value
	return m.saveSecretsLocked(module, v)
}

// RemoveSecret deletes a module's secret.
func (m *SiteManager) RemoveSecret(module, name string) error {
	if err := checkSecretKey(module, name); err != nil {
		return err
	}
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	v, err := m.loadSecretsLocked(module)
	if err != nil {
		return err
	}
	if _, ok := v[name]; !ok {
		return fmt.Errorf("module %s has no secret %s", module, name)
	}
	delete(v, name)
	return m.saveSecretsLocked(module, v)
}

// SiteModuleSecrets is one module's secrets as the dashboard shows them:
// the names, never the values.
type SiteModuleSecrets struct {
	Module string `json:"module"`
	Title  string `json:"title"`
	// Live: the module is in the live commit.
	Live bool `json:"live"`
	// Declared are the names its live module.json lists; Set those with a
	// value, declared or not (a value nobody declares reaches no module).
	Declared []string `json:"declared"`
	Set      []string `json:"set"`
}

// Secrets lists the live modules that declare secrets, and every module
// with a value saved.
func (m *SiteManager) Secrets() ([]SiteModuleSecrets, error) {
	byID := map[string]*SiteModuleSecrets{}
	if approved := m.Settings().Approved; approved != "" {
		for _, c := range readModuleCaps(m.modulesDirOf(approved), config.HomeDir()) {
			if len(c.Secrets) > 0 {
				byID[c.ID] = &SiteModuleSecrets{Module: c.ID, Title: c.Title, Live: true, Declared: slices.Clone(c.Secrets), Set: []string{}}
			}
		}
	}
	entries, err := os.ReadDir(m.dir("secrets"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || !siteModuleIDRE.MatchString(id) {
			continue
		}
		v, err := m.loadSecretsLocked(id)
		if err != nil {
			return nil, err
		}
		s := byID[id]
		if s == nil {
			s = &SiteModuleSecrets{Module: id, Title: id, Declared: []string{}, Set: []string{}}
			byID[id] = s
		}
		for name := range v {
			s.Set = append(s.Set, name)
		}
		sort.Strings(s.Set)
	}
	out := make([]SiteModuleSecrets, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out, nil
}

// secretsFor is what the live site is handed when it starts: for each module,
// the values of the secrets its module.json declares and that are set.
func (m *SiteManager) secretsFor(mods []sandbox.SiteModule) ([]byte, error) {
	m.secretsMu.Lock()
	defer m.secretsMu.Unlock()
	out := map[string]map[string]string{}
	for _, mod := range mods {
		if len(mod.Secrets) == 0 || !siteModuleIDRE.MatchString(mod.ID) {
			continue
		}
		saved, err := m.loadSecretsLocked(mod.ID)
		if err != nil {
			return nil, err
		}
		own := map[string]string{}
		for _, name := range mod.Secrets {
			if v, ok := saved[name]; ok {
				own[name] = v
			}
		}
		if len(own) > 0 {
			out[mod.ID] = own
		}
	}
	return json.Marshal(out)
}

// pipeWith returns the read end of a pipe that holds data, then EOF: for a
// child process to read once at startup, so that the data is never in a file
// it could read again, nor in its environment.
func pipeWith(data []byte) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	go func() {
		// Fails only when the child is gone without reading; nothing then
		// has the data.
		_, _ = w.Write(data)
		w.Close()
	}()
	return r, nil
}
