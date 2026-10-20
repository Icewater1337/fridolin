//go:build unix && !linux

package cli

import "syscall"

// dup2 points newfd at oldfd's file.
func dup2(oldfd, newfd int) error { return syscall.Dup2(oldfd, newfd) }

// logRotates: the open log can be renamed to <path>.1.
const logRotates = true
