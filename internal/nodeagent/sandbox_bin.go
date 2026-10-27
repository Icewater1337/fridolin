package nodeagent

import (
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The linux fridolin of the core's version, which every brain's sandbox
// mounts read-only and links to /opt/fridolin/bin/fridolin (its runs'
// `fridolin mcp --run <id>`). The core sends it as it sends updates
// (sandbox.bin_write, then sandbox.bin with its signature), whenever the
// node declares another version (SandboxCap.Bin); `fridolin node sandbox
// setup --bin` puts one in place by hand.

// Files of the binary in the state dir's sandbox folder.
const (
	// sandboxBinDir holds the binary; the sandboxes mount it read-only.
	sandboxBinDir = "bin"
	// sandboxBinFile is the binary in it, as in the VM.
	sandboxBinFile = "fridolin"
	// sandboxBinMeta is its version, beside the folder (not in the mount).
	sandboxBinMeta = "bin.json"
)

// sandboxBinInfo is bin.json.
type sandboxBinInfo struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	SHA256  string `json:"sha256"`
}

// sandboxBinHostDir is the folder the sandboxes mount, on the host.
func (a *Agent) sandboxBinHostDir() string {
	return filepath.Join(a.Dir, sandboxDir, sandboxBinDir)
}

// sandboxBinVersion is the version of the binary in place, "" for none
// (or a bin.json this node does not trust).
func (a *Agent) sandboxBinVersion() string {
	if a.Dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(a.Dir, sandboxDir, sandboxBinMeta))
	if err != nil {
		return ""
	}
	var bi sandboxBinInfo
	if json.Unmarshal(b, &bi) != nil || (&nodeproto.SandboxCap{Bin: bi.Version}).Check() != nil {
		return ""
	}
	if fi, err := os.Stat(filepath.Join(a.sandboxBinHostDir(), sandboxBinFile)); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return bi.Version
}

// elfMachines is the ELF machine of each architecture a VM runs.
var elfMachines = map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64}

// checkLinuxBinary reports whether path is a Go program for linux/arch,
// and its fridolin version from the build's -ldflags ("" when unstamped).
func checkLinuxBinary(path, arch string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s is not a linux binary: %w", path, err)
	}
	defer f.Close()
	if want, ok := elfMachines[arch]; !ok || f.Machine != want {
		return "", fmt.Errorf("%s is for %s, not linux/%s", path, f.Machine, arch)
	}
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s is not a Go program: %w", path, err)
	}
	if bi.Main.Path != "github.com/Icewater1337/fridolin" {
		return "", fmt.Errorf("%s is %s, not fridolin", path, bi.Main.Path)
	}
	for _, s := range bi.Settings {
		if s.Key == "-ldflags" {
			if m := ldflagsVersionRe.FindStringSubmatch(s.Value); m != nil {
				return m[1], nil
			}
		}
	}
	// -trimpath (make build) leaves -ldflags out of the build info: read
	// the stamped string where the symbol table says it is.
	if v := elfVersion(f); v != "" {
		return v, nil
	}
	return "", nil
}

// versionSymbol is the variable make stamps the version into.
const versionSymbol = "github.com/Icewater1337/fridolin/internal/version.version"

// elfVersion is the string versionSymbol holds in f, "" when f has no
// symbols (a stripped build) or it is not a version.
func elfVersion(f *elf.File) string {
	syms, err := f.Symbols()
	if err != nil {
		return ""
	}
	for _, s := range syms {
		if s.Name != versionSymbol {
			continue
		}
		hdr := elfRead(f, s.Value, 16) // a string: pointer, length
		if hdr == nil {
			return ""
		}
		n := binary.LittleEndian.Uint64(hdr[8:])
		if n == 0 || n > 64 {
			return ""
		}
		v := string(elfRead(f, binary.LittleEndian.Uint64(hdr[:8]), n))
		if v == "dev" || (&nodeproto.SandboxCap{Bin: v}).Check() != nil {
			return ""
		}
		return v
	}
	return ""
}

// elfRead is n bytes of f at the virtual address addr, nil when no
// section with data holds them.
func elfRead(f *elf.File, addr, n uint64) []byte {
	for _, sec := range f.Sections {
		if sec.Type == elf.SHT_NOBITS || addr < sec.Addr || addr+n > sec.Addr+sec.Size {
			continue
		}
		b := make([]byte, n)
		if _, err := sec.ReadAt(b, int64(addr-sec.Addr)); err != nil {
			return nil
		}
		return b
	}
	return nil
}

var ldflagsVersionRe = regexp.MustCompile(`internal/version\.version=([A-Za-z0-9.+_-]{1,64})`)

// installSandboxBin moves the binary at tmp, in the bin folder, into
// place and records what it is.
func (a *Agent) installSandboxBin(tmp string, bi sandboxBinInfo) error {
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(a.sandboxBinHostDir(), sandboxBinFile)); err != nil {
		return err
	}
	b, err := json.MarshalIndent(bi, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(a.Dir, sandboxDir, sandboxBinMeta), append(b, '\n'), 0o600)
}

// placeSandboxBin unpacks the gzip'd transfer at path into the bin
// folder, checks it is what p signs and a fridolin for linux, and puts it
// in place.
func (a *Agent) placeSandboxBin(path string, p nodeproto.UpdateApplyParams) error {
	dir := a.sandboxBinHostDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := unpackBinary(path, filepath.Join(dir, sandboxBinFile), p)
	if err != nil {
		return err
	}
	check := a.checkSandboxBin
	if check == nil {
		check = checkLinuxBinary
	}
	if _, err := check(tmp, p.Arch); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := a.installSandboxBin(tmp, sandboxBinInfo{Version: p.Version, Arch: p.Arch, SHA256: p.SHA256}); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// copyToTemp copies the file at src to a new file in dir, and returns
// its path and SHA-256.
func copyToTemp(src, dir string) (string, string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".fridolin.new-*")
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), io.LimitReader(in, nodeproto.MaxBinaryBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(out.Name())
		return "", "", err
	}
	return out.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// errNoStateDir is an agent without a state dir.
var errNoStateDir = errors.New("the agent has no state dir")
