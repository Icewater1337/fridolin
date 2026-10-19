package backup

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// backupSecretExcludes are the files of a brain folder a backup leaves out
// whatever the brain's exclude list says, unless include_secret_files is on:
// a brain may keep API keys in a .env, private keys, and a calendar token
// or CLI login copied into its folder, and a copy in Drive is one more
// place they can leak from. Every '+' line of an exclude list is dropped
// (see filterLines), so none can include them again. "*.key" is not among
// them: it is also Keynote's extension. docs/BACKUP.md and the dashboard's
// Backup page list them; TestBackupSecretExcludesAreDocumented keeps the
// docs in step.
var SecretExcludes = []string{
	".env", ".env.*",
	"*.pem", "*.p12",
	"id_rsa*", "id_ecdsa*", "id_ed25519*",
	"google_calendar_*.json",
	".credentials.json",
}

// rcloneSection returns the key/values of one [name] section of an rclone
// config file, or nil when it is absent.
func RcloneSection(conf, name string) map[string]string {
	var cur map[string]string
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if cur != nil {
				return cur
			}
			if line[1:len(line)-1] == name {
				cur = map[string]string{}
			}
			continue
		}
		if cur != nil {
			if k, v, ok := strings.Cut(line, "="); ok {
				cur[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return cur
}

// dayLayout names a day in backupState.LastScheduledDay.
const DayLayout = "2006-01-02"

// dueOn is daily_at on day's date, in day's time zone. On a day whose
// clocks skip that time it is the moment the clocks jump to, as time.Date
// has it. daily_at is valid HH:MM: effective refuses anything else.
func dueOn(day time.Time, dailyAt string) time.Time {
	t, _ := time.Parse("15:04", dailyAt)
	y, m, d := day.Date()
	return time.Date(y, m, d, t.Hour(), t.Minute(), 0, 0, day.Location())
}

// nextDue is the daily schedule, for the scheduler and the status page
// alike: the next scheduled run is today's at daily_at, unless it already
// ran today (lastRunDay) or the schedule was switched on or moved after
// today's time (armedAt). Then it is tomorrow's. runNow says that today's
// is due: the time has come, perhaps while the machine slept.
func Due(now time.Time, dailyAt, lastRunDay string, armedAt time.Time) (due time.Time, runNow bool) {
	due = dueOn(now, dailyAt)
	if lastRunDay == now.Format(DayLayout) || !due.After(armedAt) {
		y, m, d := now.Date()
		return dueOn(time.Date(y, m, d+1, 12, 0, 0, 0, now.Location()), dailyAt), false
	}
	return due, !now.Before(due)
}

// filterLines turns exclude globs into an rclone filter file.
func FilterLines(excludes []string) string {
	var sb strings.Builder
	for _, x := range excludes {
		x = strings.TrimSpace(x)
		if x == "" || strings.ContainsAny(x, "\r\n") || strings.HasPrefix(x, "+") {
			continue
		}
		x = strings.TrimPrefix(x, "- ")
		if strings.HasSuffix(x, "/") {
			x += "**"
		}
		sb.WriteString("- " + x + "\n")
	}
	return sb.String()
}

// brainFilter is the rclone filter file for a brain folder: the secret
// files first, unless includeSecrets, then the brain's own excludes. rclone
// stops at the first rule that matches.
func BrainFilter(excludes []string, includeSecrets bool) string {
	if includeSecrets {
		return FilterLines(excludes)
	}
	return FilterLines(SecretExcludes) + FilterLines(excludes)
}

// versionLayout names a version folder after the time of its run, in UTC
// (versionName). Local time comes twice in the hour the clocks go back, and
// would sort a later run's folder before an earlier one's or give two runs
// one name. legacyVersionLayout is the local time the folders made before
// are named in. Prune and the restore list read the run time back out of
// either.
const (
	versionLayout       = "20060102-150405Z"
	legacyVersionLayout = "20060102-150405"
)

// versionName is the version folder name of a run that started at t.
func VersionName(t time.Time) string { return t.UTC().Format(versionLayout) }

// parseVersion reads the run time out of a version folder name, of
// either layout.
func ParseVersion(name string) (time.Time, bool) {
	if t, err := time.Parse(versionLayout, name); err == nil {
		return t, true
	}
	t, err := time.ParseInLocation(legacyVersionLayout, name, time.Local)
	return t, err == nil
}

// expiredVersions picks the version folders whose run is more than days
// before now. A name that is not a version stamp is never picked, so prune
// only ever deletes folders the bridge made.
func ExpiredVersions(names []string, now time.Time, days int) []string {
	cutoff := now.AddDate(0, 0, -days)
	var out []string
	for _, name := range names {
		if t, ok := ParseVersion(name); ok && t.Before(cutoff) {
			out = append(out, name)
		}
	}
	return out
}

// sortVersions orders version folder names newest run first; names that
// are not version stamps go last.
func SortVersions(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		ti, iok := ParseVersion(names[i])
		tj, jok := ParseVersion(names[j])
		if iok != jok {
			return iok
		}
		if !iok {
			return names[i] > names[j]
		}
		return ti.After(tj)
	})
}

// backupNameOK reports whether id can name a backup in Drive: one path
// segment, not "." or "..", with no separator, colon or control character
// that would make the remote path name another folder. That covers the
// bridge's own (bridgeBackupID) and every brain id, and the names of brains
// backed up before ids were held to brainIDRe, which may still be restored.
func NameOK(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 100 {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool {
		return r == '/' || r == '\\' || r == ':' || unicode.IsControl(r)
	})
}

// restoreVersionRe matches what a restore may copy: the latest copy, or a
// version folder named as the runs name them, in either layout (see
// versionLayout).
var restoreVersionRe = regexp.MustCompile(`^(current|\d{8}-\d{6}Z?)$`)

// BackupVersionOK reports whether a version names the current copy or one saved run.
func VersionOK(s string) bool { return restoreVersionRe.MatchString(s) }
