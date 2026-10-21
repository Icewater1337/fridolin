//go:build windows

package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// errNoUserSession says that nobody is signed in to Windows to see a toast.
var errNoUserSession = errors.New("nobody is signed in to Windows")

// windowsToast shows a toast with Windows PowerShell. The node agent
// usually runs as a Windows Service, in session 0, which has no desktop:
// a toast from there is never seen. So in session 0 PowerShell is started
// in the signed-in user's session, as that user (the console's session
// first, else the first active one, such as a Remote Desktop session);
// when nobody is signed in the notification is only logged. Started from a
// terminal (`fridolin node run`) the agent is in the user's session
// already and runs PowerShell itself.
func windowsToast(ctx context.Context, title, msg string, logf func(string, ...any)) (nodeproto.NotifyResult, error) {
	ps := windowsPowerShell()
	args := powershellArgs(toastScript(title, msg, true))
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err == nil && session == 0 {
		err := runInUserSession(ctx, ps, args)
		if errors.Is(err, errNoUserSession) {
			logf("node: notification (%v): %s: %s", err, title, msg)
			return nodeproto.NotifyResult{Method: "log", Reason: err.Error()}, nil
		}
		return nodeproto.NotifyResult{Shown: err == nil, Method: "toast"}, err
	}
	cmd := exec.CommandContext(ctx, ps, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("powershell: %v: %s", err, strings.TrimSpace(clean(string(out), 300)))
	}
	return nodeproto.NotifyResult{Shown: err == nil, Method: "toast"}, err
}

// windowsPowerShell is Windows PowerShell 5.1 by its absolute path, so a
// powershell.exe earlier in the PATH is never the one run. The system
// folder comes from Windows, not from the environment.
func windowsPowerShell() string {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		sys = `C:\Windows\System32`
	}
	return filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe")
}

// userSessionToken is the token of the user signed in to the console's
// session, else of the first other active session. Only LocalSystem may
// ask for it (WTSQueryUserToken needs SeTcbPrivilege).
func userSessionToken() (windows.Token, error) {
	var infos *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &infos, &n); err != nil {
		return 0, fmt.Errorf("listing the sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(infos)))
	ids := []uint32{windows.WTSGetActiveConsoleSessionId()}
	for _, s := range unsafe.Slice(infos, n) {
		if s.State == windows.WTSActive {
			ids = append(ids, s.SessionID)
		}
	}
	for _, id := range ids {
		if id == 0 || id == 0xFFFFFFFF {
			continue
		}
		var tok windows.Token
		if windows.WTSQueryUserToken(id, &tok) == nil {
			return tok, nil
		}
	}
	return 0, errNoUserSession
}

// runInUserSession runs exe with args as the signed-in user, in their
// session and on their desktop, with their environment, hidden, and
// waits for it until ctx ends.
func runInUserSession(ctx context.Context, exe string, args []string) error {
	tok, err := userSessionToken()
	if err != nil {
		return err
	}
	defer tok.Close()
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, tok, false); err != nil {
		return fmt.Errorf("the user's environment: %w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)
	exeW, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	if err != nil {
		return err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{Desktop: desktop, Flags: windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(tok, exeW, line, nil, nil, false,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, env, nil, &si, &pi); err != nil {
		return fmt.Errorf("starting powershell in the user's session: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	wait := uint32(15000)
	if d, ok := ctx.Deadline(); ok {
		wait = uint32(max(time.Until(d), 0) / time.Millisecond)
	}
	ev, err := windows.WaitForSingleObject(pi.Process, wait)
	if err != nil {
		return fmt.Errorf("waiting for powershell: %w", err)
	}
	if ev != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(pi.Process, 1)
		return errors.New("powershell did not finish in time")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("powershell exited with status %d", code)
	}
	return nil
}
