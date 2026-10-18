package main

import "fridolin/internal/platform"

// Aliases for what moved into packages below internal/, until the call
// sites name the packages themselves.
type instanceLock = platform.Lock

var (
	acquireInstanceLock = platform.AcquireLock
	isolateProcessGroup = platform.IsolateProcessGroup
	killProcessGroup    = platform.KillProcessGroup
	openBrowser         = platform.OpenBrowser
	isTerminal          = platform.IsTerminal
	folderPicker        = platform.FolderPicker
	execInPlace         = platform.Exec
)
