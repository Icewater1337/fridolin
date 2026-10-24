package platform

import (
	"errors"
	"os"
	"testing"
	"time"
)

func TestHostZone(t *testing.T) {
	none := func(string) (string, error) { return "", os.ErrNotExist }
	noFile := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	noRun := func(string, ...string) (string, error) { return "", errors.New("no") }
	env := func(tz string) func(string) string {
		return func(k string) string {
			if k == "TZ" {
				return tz
			}
			return ""
		}
	}
	link := func(target string) func(string) (string, error) {
		return func(p string) (string, error) {
			if p == "/etc/localtime" {
				return target, nil
			}
			return "", os.ErrNotExist
		}
	}
	cases := []struct {
		name string
		src  zoneSource
		want string
	}{
		{"TZ wins", zoneSource{goos: "darwin", getenv: env("America/New_York"), readlink: link("/var/db/timezone/zoneinfo/Europe/Zurich"), readFile: noFile}, "America/New_York"},
		{"TZ with colon", zoneSource{goos: "linux", getenv: env(":Asia/Tokyo"), readlink: none, readFile: noFile}, "Asia/Tokyo"},
		{"TZ a path is skipped", zoneSource{goos: "linux", getenv: env(":/etc/localtime"), readlink: link("../usr/share/zoneinfo/Europe/Zurich"), readFile: noFile}, "Europe/Zurich"},
		{"macOS link", zoneSource{goos: "darwin", getenv: env(""), readlink: link("/var/db/timezone/zoneinfo/Europe/Zurich"), readFile: noFile}, "Europe/Zurich"},
		{"linux /etc/timezone", zoneSource{goos: "linux", getenv: env(""), readlink: none, readFile: func(string) ([]byte, error) { return []byte("Europe/Berlin\n"), nil }}, "Europe/Berlin"},
		{"linux timedatectl", zoneSource{goos: "linux", getenv: env(""), readlink: none, readFile: noFile, output: func(string, ...string) (string, error) { return "Europe/Paris\n", nil }}, "Europe/Paris"},
		{"windows", zoneSource{goos: "windows", getenv: env(""), readlink: none, readFile: noFile, output: func(string, ...string) (string, error) { return "W. Europe Standard Time\r\n", nil }}, "Europe/Berlin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.src.hostZone()
			if err != nil || got != c.want {
				t.Errorf("hostZone = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := (zoneSource{goos: "windows", getenv: env(""), readlink: none, readFile: noFile, output: noRun}).hostZone(); !errors.Is(err, ErrNoHostZone) {
		t.Errorf("unknown windows zone: %v", err)
	}
}

// Every IANA name of the Windows table loads.
func TestWindowsZonesLoad(t *testing.T) {
	for win, iana := range windowsZones() {
		if _, err := time.LoadLocation(iana); err != nil {
			t.Errorf("%s: %s: %v", win, iana, err)
		}
	}
	if WindowsZone("W. Europe Standard Time_dstoff") != "Europe/Berlin" {
		t.Error("_dstoff")
	}
}
