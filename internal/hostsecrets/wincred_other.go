//go:build !windows

package hostsecrets

import "errors"

// systemCredAPI is the Credential Manager, which this system lacks.
type systemCredAPI struct{}

var errNoCredManager = errors.New("the Windows Credential Manager exists only on Windows")

func (systemCredAPI) Read(string) ([]byte, error)        { return nil, errNoCredManager }
func (systemCredAPI) Write(string, string, []byte) error { return errNoCredManager }
func (systemCredAPI) Delete(string) error                { return errNoCredManager }
