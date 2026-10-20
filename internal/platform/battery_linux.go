//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Battery returns the charge of the first battery in percent, and false
// without one.
func Battery() (int, bool) {
	caps, _ := filepath.Glob("/sys/class/power_supply/BAT*/capacity")
	for _, p := range caps {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n >= 0 && n <= 100 {
			return n, true
		}
	}
	return 0, false
}
