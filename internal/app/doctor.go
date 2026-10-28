package app

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Icewater1337/fridolin/internal/config"
	"github.com/Icewater1337/fridolin/internal/doctor"
	"github.com/Icewater1337/fridolin/internal/hostsecrets"
	"github.com/Icewater1337/fridolin/internal/memindex"
	"github.com/Icewater1337/fridolin/internal/platform"
)

// DoctorRegistry is every check of `fridolin doctor`, in the order it
// prints them: the shared ones of internal/doctor and each feature's own
// (its doctor_*.go here, until the feature moves into a package and
// hands its checks over from there).
var DoctorRegistry = newDoctorRegistry()

func newDoctorRegistry() *doctor.Registry {
	var r doctor.Registry
	r.Add(doctor.SetupChecks(brainLoginVars)...)
	r.Add(nodesDoctorChecks...)
	r.Add(memoryDoctorCheck)
	r.Add(doctor.OwnerChecks()...)
	r.Add(timezoneDoctorCheck, nodesFrideDoctorCheck, frideInstancesDoctorCheck)
	r.Add(b2DoctorChecks...)
	r.Add(calendarDoctorChecks...)
	r.Add(coreDoctorChecks...)
	r.Add(driveDoctorChecks...)
	r.Add(encryptionDoctorCheck)
	r.Add(housekeepingDoctorChecks...)
	r.Add(plaudDoctorCheck)
	r.Add(connectionDoctorChecks...)
	r.Add(targetDoctorChecks...)
	r.Add(vaultDoctorChecks...)
	return &r
}

// WriteDoctor runs the checks of DoctorRegistry on this system, for the
// config file the --config value names (or the default one), and writes
// the report to w as o says. It returns doctor.ErrFailed when a required
// check fails.
func WriteDoctor(w io.Writer, configFlag string, o doctor.Options) error {
	return doctor.Write(w, o, SystemDoctorEnv(configFlag), DoctorRegistry.Checks())
}

// SystemDoctorEnv is a doctor.Env on the real system, for the config file
// the --config value names (or the default one); doctor.Write loads it.
func SystemDoctorEnv(configFlag string) *doctor.Env {
	inputs := ConfigOptions()
	path, _ := ConfigPathFromPaths(configFlag, inputs.Paths)
	return &doctor.Env{
		GOOS:       runtime.GOOS,
		ConfigGOOS: ConfigGOOS(),
		Home:       inputs.Paths.Home,
		ConfigPath: path,
		LoadConfig: loadDoctorConfig,
		Getenv:     os.Getenv,
		LookPath:   exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = nil
			return cmd.Output()
		},
		Stat:      os.Stat,
		ReadFile:  os.ReadFile,
		Glob:      filepath.Glob,
		FreeBytes: platform.FreeBytes,
		MemorySelfTest: func(ctx context.Context) error {
			dir, err := os.MkdirTemp("", "fridolin-doctor-memory-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			return memindex.SelfTest(ctx, dir)
		},
		MemoryStale:   memindex.Stale,
		HasHostSecret: hostsecrets.WinCred{}.Has,
		AndroidToolchain: func() error {
			_, _, err := androidToolchain()
			return err
		},
		WriteProbe: func(dir string) error {
			f, err := os.CreateTemp(dir, ".fridolin-doctor-*")
			if err != nil {
				return err
			}
			name := f.Name()
			cerr := f.Close()
			if err := os.Remove(name); err != nil {
				return err
			}
			return cerr
		},
		Dial: func(ctx context.Context, address string) error {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
			if err != nil {
				return err
			}
			return c.Close()
		},
		Now:             time.Now,
		LiveNodes:       askBridgeNodes,
		LiveGet:         askBridge,
		CheckConnection: checkConnectionKey,
	}
}

// loadDoctorConfig loads the config file as the bridge would, and puts
// the PATH in place that it would run the CLIs with.
func loadDoctorConfig(path string) (*config.Settings, error) {
	cfg, err := config.LoadExisting(path, ConfigOptions())
	if err != nil {
		return nil, err
	}
	// As check does, before any CLI is probed: a Claude planted in a
	// brain's PATH folder must not be the one found.
	SetBridgePATH(cfg)
	return cfg, nil
}
