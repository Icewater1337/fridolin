//go:build windows

package nodeagent

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Icewater1337/fridolin/internal/platform"
)

// winPTY is a process on a pseudo console (ConPTY, Windows 10 1809 and
// later).
type winPTY struct {
	hpc   windows.Handle
	in    *os.File // what the console reads: the keys typed
	out   *os.File // what it writes: the screen
	proc  windows.Handle
	pid   int
	close sync.Once
}

// startPTY starts cmd on a new pseudo console of cols × rows. Only
// cmd.Path, Args, Env and Dir count.
func startPTY(cmd *exec.Cmd, cols, rows int) (ptyProc, error) {
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		_ = windows.CloseHandle(inR)
		_ = windows.CloseHandle(inW)
		return nil, err
	}
	var hpc windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &hpc)
	// The console holds its own copies of its ends.
	_ = windows.CloseHandle(inR)
	_ = windows.CloseHandle(outW)
	if err != nil {
		_ = windows.CloseHandle(inW)
		_ = windows.CloseHandle(outR)
		return nil, err
	}
	p := &winPTY{hpc: hpc, in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out")}
	if err := p.spawn(cmd); err != nil {
		windows.ClosePseudoConsole(hpc)
		_ = p.in.Close()
		_ = p.out.Close()
		return nil, err
	}
	return p, nil
}

func (p *winPTY) spawn(cmd *exec.Cmd) error {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	// The attribute's value is the console handle itself.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&p.hpc)), unsafe.Sizeof(p.hpc)); err != nil {
		return err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	// No standard handles of the agent's: the console is the shell's.
	si.Flags = windows.STARTF_USESTDHANDLES
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(cmd.Args))
	if err != nil {
		return err
	}
	var dir *uint16
	if cmd.Dir != "" {
		if dir, err = windows.UTF16PtrFromString(cmd.Dir); err != nil {
			return err
		}
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	block := envBlock(env)
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, line, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, &block[0], dir, &si.StartupInfo, &pi); err != nil {
		return err
	}
	_ = windows.CloseHandle(pi.Thread)
	p.proc, p.pid = pi.Process, int(pi.ProcessId)
	return nil
}

// envBlock is env as CreateProcess takes it: NUL-separated, two NULs at
// the end.
func envBlock(env []string) []uint16 {
	var out []uint16
	for _, kv := range env {
		out = append(out, utf16.Encode([]rune(kv))...)
		out = append(out, 0)
	}
	if len(out) == 0 {
		out = append(out, 0)
	}
	return append(out, 0)
}

func (p *winPTY) Read(b []byte) (int, error) {
	n, err := p.out.Read(b)
	if err != nil && (errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, os.ErrClosed)) {
		err = errEndOfOutput
	}
	return n, err
}

func (p *winPTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *winPTY) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(p.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

// Kill ends the shell and what it started.
func (p *winPTY) Kill() {
	_ = platform.KillProcessGroup(p.pid)
	_ = windows.TerminateProcess(p.proc, 1)
}

// Wait waits for the shell to end, then closes the console, which ends
// its output.
func (p *winPTY) Wait() int {
	_, _ = windows.WaitForSingleObject(p.proc, windows.INFINITE)
	var code uint32
	if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
		code = 1<<32 - 1
	}
	p.close.Do(func() {
		windows.ClosePseudoConsole(p.hpc)
		_ = p.in.Close()
		_ = windows.CloseHandle(p.proc)
	})
	return int(int32(code))
}
