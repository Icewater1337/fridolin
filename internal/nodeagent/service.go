package nodeagent

import (
	"context"
	"errors"
	"fmt"
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

// ServiceConfig is the service that runs `exe node run --state-dir dir`
// at login (macOS, as a launchd user agent), as a systemd user unit
// (Linux) or as a Windows Service started at boot, restarted when it
// stops.
func ServiceConfig(goos, exe, dir string) *service.Config {
	c := &service.Config{
		Name:        ServiceName,
		DisplayName: "Fridolin node",
		Description: "Connects this machine to the Fridolin core and serves what its owner approved.",
		Executable:  exe,
		Arguments:   []string{"node", "run", "--state-dir", dir},
		Option:      service.KeyValue{},
	}
	switch goos {
	case "darwin":
		c.Name = ServiceLabelDarwin
		c.Option["UserService"] = true
		c.Option["KeepAlive"] = true
		c.Option["RunAtLoad"] = true
		c.Option["LogDirectory"] = dir
	case "linux":
		c.Option["UserService"] = true
		c.Option["Restart"] = "always"
	case "windows":
		c.Option["StartType"] = "automatic"
		c.Option["OnFailure"] = "restart"
		c.Option["OnFailureDelayDuration"] = "10s"
		c.Option["OnFailureResetPeriod"] = 600
	}
	return c
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

// ServiceStatus says whether the service is installed and running.
func ServiceStatus(cfg *service.Config) (string, error) {
	s, err := service.New(&program{}, cfg)
	if err != nil {
		return "", err
	}
	st, err := s.Status()
	if errors.Is(err, service.ErrNotInstalled) {
		return "not installed", nil
	}
	if err != nil {
		return "", err
	}
	switch st {
	case service.StatusRunning:
		return "running", nil
	case service.StatusStopped:
		return "installed, stopped", nil
	}
	return fmt.Sprintf("unknown (%d)", st), nil
}
