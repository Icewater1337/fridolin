package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kardianos/service"
)

// The node agent's service names. On macOS the launchd label differs from
// the bridge's (ai.fridolin.bridge), so a core and a node on one Mac are
// two agents.
const (
	ServiceName        = "fridolin-node"
	ServiceLabelDarwin = "ai.fridolin.node"
)

// serviceNameRe is node.json's service: a second node's service name.
var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// OwnBinary is the fridolin a second node (node.json's service) runs
// from its state dir, so that the core's updates to it never replace the
// machine's node's binary.
func OwnBinary(dir string) string {
	name := "fridolin"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, "bin", name)
}

// NodeServiceConfig is the service of the node in dir: ServiceConfig, or
// for a second node (node.json's service) its own service, run from its
// own binary once that is there.
func NodeServiceConfig(goos, exe, dir string) *service.Config {
	cfg, err := LoadConfig(dir)
	if err != nil || cfg.Service == "" {
		return ServiceConfig(goos, exe, dir)
	}
	if fi, err := os.Stat(OwnBinary(dir)); err == nil && fi.Mode().IsRegular() {
		exe = OwnBinary(dir)
	}
	return ServiceConfigNamed(goos, exe, dir, cfg.Service)
}

// ServiceConfig is the service that runs `exe node run --state-dir dir`
// at login (macOS, as a launchd user agent), as a systemd user unit
// (Linux) or as a Windows Service started at boot, restarted when it
// stops.
func ServiceConfig(goos, exe, dir string) *service.Config {
	return ServiceConfigNamed(goos, exe, dir, "")
}

// ServiceConfigNamed is ServiceConfig for a second node on the machine,
// whose service is named after suffix ("" for the machine's node).
func ServiceConfigNamed(goos, exe, dir, suffix string) *service.Config {
	name, label, display := ServiceName, ServiceLabelDarwin, "Fridolin node"
	if suffix != "" {
		name, label, display = name+"-"+suffix, label+"."+suffix, display+" ("+suffix+")"
	}
	c := &service.Config{
		Name:        name,
		DisplayName: display,
		Description: "Connects this machine to the Fridolin core and serves what its owner approved.",
		Executable:  exe,
		Arguments:   []string{"node", "run", "--state-dir", dir},
		Option:      service.KeyValue{},
	}
	switch goos {
	case "darwin":
		c.Name = label
		c.Option["UserService"] = true
		c.Option["KeepAlive"] = true
		c.Option["RunAtLoad"] = true
		c.Option["LogDirectory"] = dir
	case "linux":
		c.Option["UserService"] = true
		c.Option["Restart"] = "always"
		c.Option["SystemdScript"] = systemdScript
	case "windows":
		c.Option["StartType"] = "automatic"
		c.Option["OnFailure"] = "restart"
		c.Option["OnFailureDelayDuration"] = "10s"
		c.Option["OnFailureResetPeriod"] = 600
	}
	return c
}

// LinuxBootTarget is the target the systemd user unit is enabled into. A
// user manager starts default.target at boot (with lingering on) and at
// login; it never reaches multi-user.target, which is the system
// manager's, so a unit wanted by that one does not start after a reboot.
const LinuxBootTarget = "default.target"

// legacyBootTarget is what kardianos/service's own template says.
const legacyBootTarget = "multi-user.target"

// systemdScript is kardianos/service v1.3.0's systemd template, in its
// template syntax, with one change: WantedBy names LinuxBootTarget
// instead of multi-user.target (issue #171).
const systemdScript = `[Unit]
Description={{Description}}
ConditionFileIsExecutable={{Path | cmdEscape}}
{{range Dependencies}}{{.}}
{{end}}
[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if ChRoot}}RootDirectory={{ChRoot | cmd}}
{{end}}{{if WorkingDirectory}}WorkingDirectory={{WorkingDirectory | cmdEscape}}
{{end}}{{if UserName}}User={{UserName}}
{{end}}{{if ReloadSignal}}ExecReload=/bin/kill -{{ReloadSignal}} "$MAINPID"
{{end}}{{if PIDFile}}PIDFile={{PIDFile | cmd}}
{{end}}{{if OutputFileSupport}}StandardOutput=file:{{LogDirectory}}/{{Name}}.out
StandardError=file:{{LogDirectory}}/{{Name}}.err
{{end}}{{if LimitNOFILE}}LimitNOFILE={{LimitNOFILE}}
{{end}}{{if Restart}}Restart={{Restart}}
{{end}}{{if SuccessExitStatus}}SuccessExitStatus={{SuccessExitStatus}}
{{end}}RestartSec=120
EnvironmentFile=-/etc/sysconfig/{{Name}}

{{range EnvVars}}{{.}}
{{end}}[Install]
WantedBy=` + LinuxBootTarget + `
`

// LinuxUnitPath is where kardianos/service writes the systemd user unit
// under the home folder home.
func LinuxUnitPath(home string) string {
	return filepath.Join(home, ".config", "systemd", "user", ServiceName+".service")
}

// LinuxWantsLink is the link `systemctl --user enable` makes for the
// unit in target's .wants folder.
func LinuxWantsLink(home, target string) string {
	return filepath.Join(home, ".config", "systemd", "user", target+".wants", ServiceName+".service")
}

// RepairBootTarget moves a systemd user unit written before issue #171
// was fixed from multi-user.target to LinuxBootTarget: it disables the
// unit (which removes the old link), rewrites its WantedBy line and
// enables it again. systemctl runs `systemctl ARGS`. It says whether it
// changed anything; a missing or already right unit is left alone.
//
// compat: units that `fridolin node install` or `join` wrote with
// kardianos/service's template say WantedBy=multi-user.target; `node
// install`, `start`, `join` and `run` call this, so they move once.
func RepairBootTarget(home string, systemctl func(args ...string) error) (bool, error) {
	unit := LinuxUnitPath(home)
	data, err := os.ReadFile(unit)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	old := []byte("\nWantedBy=" + legacyBootTarget + "\n")
	if !bytes.Contains(data, old) {
		return false, nil
	}
	name := ServiceName + ".service"
	if err := systemctl("--user", "disable", name); err != nil {
		return false, fmt.Errorf("systemctl --user disable %s: %w", name, err)
	}
	fixed := bytes.Replace(data, old, []byte("\nWantedBy="+LinuxBootTarget+"\n"), 1)
	if err := os.WriteFile(unit, fixed, 0o644); err != nil {
		return false, err
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", name}} {
		if err := systemctl(args...); err != nil {
			return false, fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	return true, nil
}

// program runs the agent for the service manager.
type program struct {
	agent  *Agent
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	// From a terminal the agent restarts in place after an update; under
	// the service manager it exits for the manager to start it again.
	p.agent.upd.mu.Lock()
	p.agent.upd.interactive = service.Interactive()
	p.agent.upd.mu.Unlock()
	go func() {
		defer close(p.done)
		err := p.agent.Run(ctx)
		if ce, ok := errors.AsType[*CloseError](err); ok && ce.Final() {
			// Revoked or unknown: stay up without reconnecting, so the
			// service manager does not restart the agent into the same
			// refusal over and over. Join again to come back.
			p.agent.logf("node: idle until stopped; join again with a new code (fridolin node join)")
			<-ctx.Done()
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
	})
	if p.done == nil {
		return nil
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
	}
	return nil
}

// RunService runs the agent under the service manager, or in the
// foreground until interrupted when started from a terminal.
func RunService(a *Agent, cfg *service.Config) error {
	s, err := service.New(&program{agent: a}, cfg)
	if err != nil {
		return err
	}
	return s.Run()
}

// ControlService installs, starts, stops or uninstalls the service.
func ControlService(cfg *service.Config, action string) error {
	s, err := service.New(&program{}, cfg)
	if err != nil {
		return err
	}
	return service.Control(s, action)
}

// ServiceStatus says whether the service is installed and running. On
// Windows it needs no administrator.
func ServiceStatus(cfg *service.Config) (string, error) {
	return serviceStatus(cfg)
}

// ServiceStateDir is the state dir the installed service runs with, read
// back from the Windows Service's command line: "" for a service that is
// not installed, and on macOS and Linux.
func ServiceStateDir(cfg *service.Config) (string, error) {
	return serviceStateDir(cfg)
}

// stateDirArg is the --state-dir of the service's command line, "" for
// none.
func stateDirArg(args []string) string {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--state-dir="); ok {
			return v
		}
		if a == "--state-dir" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// RunForeground runs the agent until ctx ends, without a service manager:
// a node in a box, whose container is the service. A revoked or unknown
// node stays up without reconnecting, as under a service manager, so the
// container does not restart into the same refusal.
func RunForeground(ctx context.Context, a *Agent) error {
	err := a.Run(ctx)
	if ce, ok := errors.AsType[*CloseError](err); ok && ce.Final() {
		a.logf("node: idle until stopped; join again with a new code (fridolin node join --box)")
		<-ctx.Done()
		return nil
	}
	return err
}
