package cli

import "syscall"

// dup2 points newfd at oldfd's file. linux/arm64 has no dup2 call.
func dup2(oldfd, newfd int) error { return syscall.Dup3(oldfd, newfd, 0) }

// logRotates: the open log can be renamed to <path>.1.
const logRotates = true
