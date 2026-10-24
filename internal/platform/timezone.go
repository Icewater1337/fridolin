package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoHostZone is a machine whose time zone HostZone cannot tell.
const ErrNoHostZone = zoneError("could not tell this machine's time zone")

// zoneError is a constant error: platform keeps no package state.
type zoneError string

func (e zoneError) Error() string { return string(e) }

// zoneSource is what HostZone reads, a variable part for tests.
type zoneSource struct {
	goos     string
	getenv   func(string) string
	readlink func(string) (string, error)
	readFile func(string) ([]byte, error)
	// output runs a program and returns what it printed.
	output func(name string, args ...string) (string, error)
}

// HostZone is this machine's time zone as an IANA name, such as
// "Europe/Zurich": TZ when it names one; on macOS and Linux the zone
// /etc/localtime links to, on Linux also /etc/timezone and timedatectl;
// on Windows its zone mapped to the IANA name (CLDR's windowsZones).
// ErrNoHostZone when none of them says.
func HostZone() (string, error) {
	return zoneSource{goos: runtime.GOOS, getenv: os.Getenv, readlink: os.Readlink, readFile: os.ReadFile,
		output: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).Output()
			return string(out), err
		}}.hostZone()
}

func (s zoneSource) hostZone() (string, error) {
	if tz := strings.TrimPrefix(strings.TrimSpace(s.getenv("TZ")), ":"); validZone(tz) {
		return tz, nil
	}
	switch s.goos {
	case "windows":
		if s.output != nil {
			if out, err := s.output("tzutil", "/g"); err == nil {
				if name := WindowsZone(out); name != "" {
					return name, nil
				}
			}
		}
		return "", ErrNoHostZone
	case "linux":
		if b, err := s.readFile("/etc/timezone"); err == nil {
			if tz := strings.TrimSpace(string(b)); validZone(tz) {
				return tz, nil
			}
		}
	}
	if target, err := s.readlink("/etc/localtime"); err == nil {
		// …/zoneinfo/Europe/Zurich, also /var/db/timezone/zoneinfo/… on
		// macOS and ../usr/share/zoneinfo/… relative.
		if _, name, ok := strings.Cut(filepath.ToSlash(target), "zoneinfo/"); ok {
			name = strings.TrimPrefix(name, "posix/")
			if validZone(name) {
				return name, nil
			}
		}
	}
	if s.goos == "linux" && s.output != nil {
		if out, err := s.output("timedatectl", "show", "-p", "Timezone", "--value"); err == nil {
			if tz := strings.TrimSpace(out); validZone(tz) {
				return tz, nil
			}
		}
	}
	return "", ErrNoHostZone
}

// validZone reports whether name is an IANA zone this build can load:
// not a path, not "Local".
func validZone(name string) bool {
	if name == "" || name == "Local" || filepath.IsAbs(name) || strings.HasPrefix(name, ".") {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// WindowsZone is the IANA name of a Windows time zone as tzutil /g prints
// it ("W. Europe Standard Time"), from CLDR's windowsZones table (each
// zone's "001" territory); "" for one it does not know.
func WindowsZone(id string) string {
	id = strings.TrimSuffix(strings.TrimSpace(id), "_dstoff")
	return windowsZones()[id]
}

// windowsZones is CLDR's windowsZones.xml, territory 001: the main IANA
// zone of each Windows zone.
func windowsZones() map[string]string {
	return map[string]string{
		"Dateline Standard Time":          "Etc/GMT+12",
		"UTC-11":                          "Etc/GMT+11",
		"Aleutian Standard Time":          "America/Adak",
		"Hawaiian Standard Time":          "Pacific/Honolulu",
		"Alaskan Standard Time":           "America/Anchorage",
		"Pacific Standard Time (Mexico)":  "America/Tijuana",
		"Pacific Standard Time":           "America/Los_Angeles",
		"US Mountain Standard Time":       "America/Phoenix",
		"Mountain Standard Time (Mexico)": "America/Mazatlan",
		"Mountain Standard Time":          "America/Denver",
		"Central America Standard Time":   "America/Guatemala",
		"Central Standard Time":           "America/Chicago",
		"Central Standard Time (Mexico)":  "America/Mexico_City",
		"Canada Central Standard Time":    "America/Regina",
		"SA Pacific Standard Time":        "America/Bogota",
		"Eastern Standard Time (Mexico)":  "America/Cancun",
		"Eastern Standard Time":           "America/New_York",
		"US Eastern Standard Time":        "America/Indiana/Indianapolis",
		"Venezuela Standard Time":         "America/Caracas",
		"Paraguay Standard Time":          "America/Asuncion",
		"Atlantic Standard Time":          "America/Halifax",
		"Central Brazilian Standard Time": "America/Cuiaba",
		"SA Western Standard Time":        "America/La_Paz",
		"Pacific SA Standard Time":        "America/Santiago",
		"Newfoundland Standard Time":      "America/St_Johns",
		"E. South America Standard Time":  "America/Sao_Paulo",
		"Argentina Standard Time":         "America/Argentina/Buenos_Aires",
		"SA Eastern Standard Time":        "America/Cayenne",
		"Greenland Standard Time":         "America/Nuuk",
		"Montevideo Standard Time":        "America/Montevideo",
		"UTC-02":                          "Etc/GMT+2",
		"Azores Standard Time":            "Atlantic/Azores",
		"Cape Verde Standard Time":        "Atlantic/Cape_Verde",
		"UTC":                             "Etc/UTC",
		"GMT Standard Time":               "Europe/London",
		"Greenwich Standard Time":         "Atlantic/Reykjavik",
		"Morocco Standard Time":           "Africa/Casablanca",
		"W. Europe Standard Time":         "Europe/Berlin",
		"Central Europe Standard Time":    "Europe/Budapest",
		"Romance Standard Time":           "Europe/Paris",
		"Central European Standard Time":  "Europe/Warsaw",
		"W. Central Africa Standard Time": "Africa/Lagos",
		"Jordan Standard Time":            "Asia/Amman",
		"GTB Standard Time":               "Europe/Bucharest",
		"Middle East Standard Time":       "Asia/Beirut",
		"Egypt Standard Time":             "Africa/Cairo",
		"E. Europe Standard Time":         "Europe/Chisinau",
		"Syria Standard Time":             "Asia/Damascus",
		"South Africa Standard Time":      "Africa/Johannesburg",
		"FLE Standard Time":               "Europe/Kiev",
		"Israel Standard Time":            "Asia/Jerusalem",
		"Kaliningrad Standard Time":       "Europe/Kaliningrad",
		"Arabic Standard Time":            "Asia/Baghdad",
		"Turkey Standard Time":            "Europe/Istanbul",
		"Arab Standard Time":              "Asia/Riyadh",
		"Belarus Standard Time":           "Europe/Minsk",
		"Russian Standard Time":           "Europe/Moscow",
		"E. Africa Standard Time":         "Africa/Nairobi",
		"Iran Standard Time":              "Asia/Tehran",
		"Arabian Standard Time":           "Asia/Dubai",
		"Azerbaijan Standard Time":        "Asia/Baku",
		"Georgian Standard Time":          "Asia/Tbilisi",
		"Caucasus Standard Time":          "Asia/Yerevan",
		"Afghanistan Standard Time":       "Asia/Kabul",
		"Ekaterinburg Standard Time":      "Asia/Yekaterinburg",
		"Pakistan Standard Time":          "Asia/Karachi",
		"West Asia Standard Time":         "Asia/Tashkent",
		"India Standard Time":             "Asia/Kolkata",
		"Sri Lanka Standard Time":         "Asia/Colombo",
		"Nepal Standard Time":             "Asia/Kathmandu",
		"Central Asia Standard Time":      "Asia/Almaty",
		"Bangladesh Standard Time":        "Asia/Dhaka",
		"Myanmar Standard Time":           "Asia/Yangon",
		"SE Asia Standard Time":           "Asia/Bangkok",
		"North Asia Standard Time":        "Asia/Krasnoyarsk",
		"China Standard Time":             "Asia/Shanghai",
		"North Asia East Standard Time":   "Asia/Irkutsk",
		"Singapore Standard Time":         "Asia/Singapore",
		"W. Australia Standard Time":      "Australia/Perth",
		"Taipei Standard Time":            "Asia/Taipei",
		"Ulaanbaatar Standard Time":       "Asia/Ulaanbaatar",
		"Tokyo Standard Time":             "Asia/Tokyo",
		"Korea Standard Time":             "Asia/Seoul",
		"Yakutsk Standard Time":           "Asia/Yakutsk",
		"Cen. Australia Standard Time":    "Australia/Adelaide",
		"AUS Central Standard Time":       "Australia/Darwin",
		"E. Australia Standard Time":      "Australia/Brisbane",
		"AUS Eastern Standard Time":       "Australia/Sydney",
		"West Pacific Standard Time":      "Pacific/Port_Moresby",
		"Tasmania Standard Time":          "Australia/Hobart",
		"Vladivostok Standard Time":       "Asia/Vladivostok",
		"Central Pacific Standard Time":   "Pacific/Guadalcanal",
		"New Zealand Standard Time":       "Pacific/Auckland",
		"Fiji Standard Time":              "Pacific/Fiji",
		"Tonga Standard Time":             "Pacific/Tongatapu",
		"Samoa Standard Time":             "Pacific/Apia",
	}
}
