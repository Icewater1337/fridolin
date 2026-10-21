//go:build unix

package nodeagent

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// unixPTY is a process on a pseudo-terminal.
type unixPTY struct {
	f   *os.File
	cmd *exec.Cmd
}

// startPTY starts cmd on a new pseudo-terminal of cols × rows, as the
// leader of a session of its own, with the terminal as its controlling
// one.
func startPTY(cmd *exec.Cmd, cols, rows int) (ptyProc, error) {
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	return &unixPTY{f: f, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if err != nil && errors.Is(err, syscall.EIO) {
		// Linux reports the other side's end as EIO.
		err = errEndOfOutput
	}
	return n, err
}

func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *unixPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Kill ends the shell and every process of its session.
func (p *unixPTY) Kill() {
	if p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGHUP)
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

func (p *unixPTY) Wait() int {
	err := p.cmd.Wait()
	_ = p.f.Close()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}
