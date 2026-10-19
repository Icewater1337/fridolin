package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// procStart says when process pid started, in clock ticks since boot, as
// /proc/<pid>/stat has it: what tells it from a later process given the
// same PID. A process that does not exist is syscall.ESRCH.
func procStart(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return "", syscall.ESRCH
	}
	if err != nil {
		return "", err
	}
	// The command name in parentheses may hold anything, spaces and ")"
	// included; the fields after its last ")" start with the state, the
	// third field, so the start time, the 22nd, is the 20th of them.
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return "", fmt.Errorf("/proc/%d/stat: no command name", pid)
	}
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) < 20 {
		return "", fmt.Errorf("/proc/%d/stat: too few fields", pid)
	}
	return fields[19], nil
}

// bootID names this boot of the machine: no process outlives it.
func bootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
