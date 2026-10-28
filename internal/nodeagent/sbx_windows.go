//go:build windows

package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// sbx on Windows (docs/NODES.md, "FrIDE instances on Windows"): sbx's
// login and VMs are the signed-in user's, while the node's service runs
// as LocalSystem in session 0. So the service runs every sbx call in the
// signed-in user's session, as that user: with their token (as the
// notifications do, notify_windows.go), their environment and their
// profile folder as the working folder, without a window; its pipes work
// as anywhere. Run as the user (a terminal, `fridolin node run`), the
// agent runs sbx itself.

// errSbxNobody is a service's sbx call while nobody is signed in.
var errSbxNobody = errors.New("nobody is signed in to Windows: sbx runs in the signed-in user's session")

// sbxAsService reports whether the agent runs as LocalSystem in session
// 0: the node's service, whose sbx calls run in the user's session.
func sbxAsService() bool {
	var session uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session) != nil || session != 0 {
		return false
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

// sbxInSession reports whether sbx runs in the signed-in user's session
// rather than the agent's.
func sbxInSession() bool { return sbxAsService() }

// sbxUser is the signed-in user, for a call of sbx from the service.
type sbxUser struct {
	tok     windows.Token
	env     []string
	profile string
}

// signedInUser is the signed-in user's token (the console's session
// first), environment and profile folder; the caller closes tok.
func signedInUser() (*sbxUser, error) {
	tok, err := userSessionToken()
	if err != nil {
		return nil, errSbxNobody
	}
	env, err := tok.Environ(false)
	if err != nil {
		tok.Close()
		return nil, fmt.Errorf("the signed-in user's environment: %w", err)
	}
	profile, err := tok.GetUserProfileDirectory()
	if err != nil {
		tok.Close()
		return nil, fmt.Errorf("the signed-in user's profile folder: %w", err)
	}
	return &sbxUser{tok: tok, env: env, profile: profile}, nil
}

// sbxCommand is sbx at path with args and the environment it starts
// from. From the service: in the signed-in user's session, as that user,
// path "" their sbx, and done closes their token once sbx started.
func sbxCommand(ctx context.Context, path string, args []string) (*exec.Cmd, []string, func(), error) {
	if !sbxAsService() {
		c := exec.CommandContext(ctx, path, args...)
		return c, c.Environ(), func() {}, nil
	}
	u, err := signedInUser()
	if err != nil {
		return nil, nil, nil, err
	}
	if path == "" {
		path = FindSbx(u.env)
	}
	if path == "" {
		u.tok.Close()
		return nil, nil, nil, errors.New("sbx is not installed for the signed-in user: " + SbxInstallHint())
	}
	c := exec.CommandContext(ctx, path, args...)
	c.Dir = u.profile
	c.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(u.tok), HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return c, u.env, func() { u.tok.Close() }, nil
}

// unavailable says why the service cannot run sbx now: nobody signed
// in, or no sbx of theirs.
func (s sbxCLI) unavailable() string {
	if !sbxAsService() {
		return ""
	}
	u, err := signedInUser()
	if err != nil {
		return err.Error()
	}
	defer u.tok.Close()
	if s.path == "" && FindSbx(u.env) == "" {
		return "sbx is not installed for the user signed in to Windows: " + SbxInstallHint()
	}
	return ""
}

// sbxUserSID is the SID of the user who runs sbx: the signed-in user's
// for the service, else the agent's own.
func sbxUserSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	if sbxAsService() {
		t, err := userSessionToken()
		if err != nil {
			return "", errSbxNobody
		}
		defer t.Close()
		tok = t
	}
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// hostFolder gives dir, and what is in it, to the user who runs sbx and
// SYSTEM alone, inheriting nothing from above.
func (sbxCLI) hostFolder(dir string) error {
	sid, err := sbxUserSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sbxFolderSDDL(sid))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("giving %s to the user who runs sbx: %w", dir, err)
	}
	return nil
}

// sbxSecretCommand is the host command sbx runs to read the token file:
// the node's own fridolin.exe with sbx-secret, by short (8.3) paths,
// which hold no space.
func sbxSecretCommand(file string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return sbxSecretCommandFor("windows", shortPath(exe), filepath.Join(shortPath(filepath.Dir(file)), filepath.Base(file)))
}

// shortPath is p's short (8.3) form when p holds a space and the volume
// has one, else p.
func shortPath(p string) string {
	if !strings.ContainsAny(p, " \t") {
		return p
	}
	pw, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	n, err := windows.GetShortPathName(pw, nil, 0)
	if err != nil || n == 0 {
		return p
	}
	buf := make([]uint16, n)
	if n, err = windows.GetShortPathName(pw, &buf[0], n); err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}
