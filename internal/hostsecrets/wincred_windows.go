//go:build windows

package hostsecrets

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// advapi32's credential functions (wincred.h).
var (
	advapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1 // CRED_TYPE_GENERIC
	credPersistLocalMachine = 2 // CRED_PERSIST_LOCAL_MACHINE
)

// credentialW is CREDENTIALW; Go lays it out as C does, on 32 and 64 bits.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// systemCredAPI is the Credential Manager of the user this runs as.
type systemCredAPI struct{}

func credErr(fn string, e error) error {
	if errors.Is(e, windows.ERROR_NOT_FOUND) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", fn, e)
}

func (systemCredAPI) Read(target string) ([]byte, error) {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var cred *credentialW
	r, _, e := procCredReadW.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&cred)))
	if r == 0 {
		return nil, credErr("CredReadW", e)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	blob := make([]byte, cred.CredentialBlobSize)
	if cred.CredentialBlobSize > 0 {
		copy(blob, unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize))
	}
	return blob, nil
}

func (systemCredAPI) Write(target, user string, blob []byte) error {
	if len(blob) == 0 {
		return errors.New("an empty value")
	}
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	u, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	cred := credentialW{
		Type:               credTypeGeneric,
		TargetName:         t,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           u,
	}
	r, _, e := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	runtime.KeepAlive(blob)
	runtime.KeepAlive(t)
	runtime.KeepAlive(u)
	if r == 0 {
		return credErr("CredWriteW", e)
	}
	return nil
}

func (systemCredAPI) Delete(target string) error {
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, e := procCredDeleteW.Call(uintptr(unsafe.Pointer(t)), credTypeGeneric, 0)
	if r == 0 {
		return credErr("CredDeleteW", e)
	}
	return nil
}
