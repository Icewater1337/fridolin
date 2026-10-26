package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/doctor"
)

// The vault's doctor checks (M3's gate): they read what the running
// vault last wrote to core/vault-status.json, and apply only with
// vault.enabled.
var vaultDoctorChecks = []doctor.Check{
	{ID: "vault", Title: "vault reachable", Required: true, Applies: vaultOn, Run: checkVaultReachable},
	{ID: "vault-upload", Title: "vault uploads within the hour", Required: true, Applies: vaultOn, Run: checkVaultUpload},
	{ID: "vault-snapshot", Title: "snapshot within 26 hours", Required: true, Applies: vaultOn, Run: checkVaultSnapshot},
	{ID: "vault-brain-backup", Title: "brain folders follow their backup settings", Required: true, Applies: vaultOn, Run: checkVaultBrainBackup},
	{ID: "vault-litestream", Title: "Litestream replicating bridge.db and vault.db", Required: true, Applies: vaultOn, Run: checkVaultLitestream},
	{ID: "vault-secret-folders", Title: "secret folders backed up within 2 days", Required: true, Applies: vaultSecretFoldersSet, Run: checkVaultSecretFolders},
}

// The limits of the vault's checks.
const (
	vaultStatusFresh  = 5 * time.Minute  // the running vault writes its status each minute
	vaultUploadWithin = time.Hour        // a file waits at most this long for its upload
	vaultSnapWithin   = 26 * time.Hour   // a nightly snapshot, with two hours to spare
	vaultReachWithin  = 45 * time.Minute // the vault shows the cold tier answering at least every 30 minutes
	// vaultSecretsWithin: a version goes up each day; two days without
	// one is a warning.
	vaultSecretsWithin = 48 * time.Hour
)

func vaultOn(env *doctor.Env) bool { return env.Config != nil && env.Config.Vault.Enabled }

// vaultStatusFor reads the status, or says why it is no good.
func vaultStatusFor(env *doctor.Env) (vaultStatus, *doctor.Result) {
	data, err := ReadVaultStatusFile(env.Config.DataDir, env.ReadFile)
	if err != nil {
		return vaultStatus{}, &doctor.Result{Status: doctor.Fail, Detail: err.Error(), Fix: "start the bridge (fridolin service restart) and look at its log"}
	}
	st, err := parseVaultStatus(data)
	if err != nil {
		return st, &doctor.Result{Status: doctor.Fail, Detail: err.Error()}
	}
	if age := env.Time().Sub(st.UpdatedAt); age > vaultStatusFresh {
		return st, &doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("the vault last wrote its status %s ago: the bridge does not run it", age.Round(time.Second)), Fix: "fridolin service restart, then fridolin vault status"}
	}
	if st.State != vaultRunning {
		return st, &doctor.Result{Status: doctor.Fail, Detail: "the vault is " + st.State + ": " + st.Error, Fix: "fridolin vault status, and the bridge's log"}
	}
	return st, nil
}

func checkVaultReachable(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	r := st.Reachable
	switch {
	case r == nil:
		return doctor.Result{Status: doctor.Warn, Detail: "the vault has not reached the cold tier yet"}
	case !r.OK:
		return doctor.Result{Status: doctor.Fail, Detail: "the cold tier does not answer: " + r.Error, Fix: "check the B2 keys (fridolin.b2.vault.keyid, .key) and the network"}
	case env.Time().Sub(r.At) > vaultReachWithin:
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("last reached %s ago", env.Time().Sub(r.At).Round(time.Second))}
	}
	return doctor.Result{Status: doctor.OK, Detail: fmt.Sprintf("reached %s ago", env.Time().Sub(r.At).Round(time.Second))}
}

// checkVaultUpload: nothing waits longer than an hour for its upload. A
// quiet vault, with every file in the cold tier, passes however long ago
// it last uploaded.
func checkVaultUpload(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	last := "never"
	if !st.LastUpload.IsZero() {
		last = env.Time().Sub(st.LastUpload).Round(time.Second).String() + " ago"
	}
	if st.Files.Hot == 0 {
		return doctor.Result{Status: doctor.OK, Detail: "every file is in the cold tier; last upload " + last}
	}
	if !st.LastUpload.IsZero() && env.Time().Sub(st.LastUpload) <= vaultUploadWithin {
		return doctor.Result{Status: doctor.OK, Detail: fmt.Sprintf("%d files wait for their upload; last upload %s", st.Files.Hot, last)}
	}
	detail := fmt.Sprintf("%d files wait for their upload, and the last upload was %s", st.Files.Hot, last)
	if r := st.LastSync; r != nil && r.Error != "" {
		detail += ": " + r.Error
	}
	return doctor.Result{Status: doctor.Fail, Detail: detail, Fix: "fridolin vault sync, then fridolin vault status"}
}

func checkVaultSnapshot(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	sn := st.Snapshots
	if !sn.Enabled {
		return doctor.Result{Status: doctor.Fail, Detail: "snapshots are off: " + sn.Error, Fix: "add the snapshots bucket's B2 key to the host secrets (fridolin.b2.snapshots.keyid, .key) and install restic"}
	}
	var last *snapshotRecord
	for _, r := range []*snapshotRecord{sn.LastNightly, sn.LastHourly} {
		if r != nil && r.ID != "" && (last == nil || r.At.After(last.At)) {
			last = r
		}
	}
	if last == nil {
		return doctor.Result{Status: doctor.Fail, Detail: "no snapshot yet", Fix: "fridolin vault status; the first one runs within a minute of the vault's start"}
	}
	age := env.Time().Sub(last.At)
	if age > vaultSnapWithin {
		return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("the last snapshot (%s, %s) is %s old", last.Kind, last.ID, age.Round(time.Minute)), Fix: "fridolin vault status and the bridge's log"}
	}
	detail := fmt.Sprintf("%s snapshot %s, %s ago", last.Kind, last.ID, age.Round(time.Second))
	if h := sn.DeletionHold; h != nil {
		return doctor.Result{Status: doctor.Warn, Detail: detail + fmt.Sprintf("; pruning is held: snapshot %s has %d files, the one before %d", h.Snapshot, h.Files, h.PrevFiles), Fix: "look at what was deleted, then Allow pruning on the dashboard's Vault page"}
	}
	return doctor.Result{Status: doctor.OK, Detail: detail}
}

// litestreamProblemRecent is how long a warning or error that Litestream
// logged keeps its check from passing: it logs a lasting one again every
// few minutes.
const litestreamProblemRecent = 15 * time.Minute

// litestreamStartGrace is how long after the vault's start its check
// waits for a word from Litestream before it fails: the vault writes
// Litestream's state as soon as its first child runs.
const litestreamStartGrace = 5 * time.Minute

func checkVaultLitestream(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	l := st.Litestream
	switch {
	case !l.Enabled:
		return doctor.Result{Status: doctor.Fail, Detail: "Litestream does not run: it is not installed, or the snapshots bucket has no key", Fix: doctor.InstallFix(env.GOOS, "benbjohnson/litestream/litestream", "")}
	case !st.StartedAt.IsZero() && l.At.Before(st.StartedAt):
		// The last run's status, or none yet: not known.
		since := env.Time().Sub(st.StartedAt).Round(time.Second)
		if since < litestreamStartGrace {
			return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("the vault started %s ago; Litestream has not said yet whether it runs", since), Fix: "run fridolin doctor again in a minute"}
		}
		return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("the vault started %s ago, and Litestream has not said since whether it runs", since), Fix: "the bridge's log says why"}
	case !l.Running:
		return doctor.Result{Status: doctor.Fail, Detail: fmt.Sprintf("Litestream is not running (%d restarts): %s", l.Restarts, l.LastError), Fix: "the bridge's log says why; it restarts Litestream by itself"}
	case !l.LastProblemAt.IsZero() && env.Time().Sub(l.LastProblemAt) < litestreamProblemRecent:
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("pid %d runs, but logged %s ago: %s", l.PID, env.Time().Sub(l.LastProblemAt).Round(time.Second), l.LastProblem),
			Fix: "the bridge's log says more; a database whose replica lost a file it needs is started over by itself, once an hour at most"}
	}
	detail := fmt.Sprintf("pid %d, %d restarts", l.PID, l.Restarts)
	if l.Repairs > 0 {
		detail += fmt.Sprintf(", started over after a lost file: %d, the last %s", l.Repairs, l.LastRepair.Format(time.RFC3339))
	}
	return doctor.Result{Status: doctor.OK, Detail: detail}
}

// checkVaultBrainBackup warns about the brain folders the snapshots and
// the cold tier leave out because their brain.json cannot be read: the
// vault fails closed rather than upload what they might exclude, so
// nothing of those folders reaches the cloud until it is fixed. A folder
// whose backup is off is left out as asked, and counts as fine.
func checkVaultBrainBackup(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	var unreadable, off []string
	for _, b := range st.Snapshots.SkippedBrains {
		if b.Unreadable {
			unreadable = append(unreadable, fmt.Sprintf("%s (%s): %s", b.ID, b.Dir, b.Skip))
		} else {
			off = append(off, b.ID)
		}
	}
	if len(unreadable) > 0 {
		return doctor.Result{Status: doctor.Warn, Detail: "left out of the snapshots and the cold tier: " + strings.Join(unreadable, "; "),
			Fix: "fix the brain.json (fridolin check says what is wrong with it); the next snapshot takes the folder again"}
	}
	if len(off) > 0 {
		return doctor.Result{Status: doctor.OK, Detail: "every brain.json read; left out as their backup is off: " + strings.Join(off, ", ")}
	}
	return doctor.Result{Status: doctor.OK, Detail: "every brain.json read"}
}

// vaultSecretFoldersSet: the vault runs and a secret folder is set.
func vaultSecretFoldersSet(env *doctor.Env) bool {
	return vaultOn(env) && len(secretFolderPaths(env.Config)) > 0
}

// checkVaultSecretFolders warns while the secret folders have no version
// in B2 younger than two days, or the last try failed.
func checkVaultSecretFolders(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	sf := st.SecretFolders
	fix := "fridolin vault backup-secrets, then fridolin vault status"
	switch {
	case sf == nil:
		return doctor.Result{Status: doctor.Warn, Detail: "the vault has not looked at the secret folders yet", Fix: "run fridolin doctor again in a minute"}
	case !sf.Enabled:
		return doctor.Result{Status: doctor.Warn, Detail: "the secret folders are not backed up: " + sf.Error, Fix: "fridolin core start makes the missing key; then restart the core"}
	case sf.LastOK == nil:
		detail := "no backup of the secret folders yet"
		if sf.Last != nil && sf.Last.Error != "" {
			detail += ": " + sf.Last.Error
		}
		return doctor.Result{Status: doctor.Warn, Detail: detail, Fix: fix}
	}
	age := env.Time().Sub(sf.LastOK.At)
	detail := fmt.Sprintf("%d folders, %d files, last backed up %s ago (%s)", len(sf.Folders), sf.LastOK.Files, age.Round(time.Second), sf.LastOK.Version)
	switch {
	case age > vaultSecretsWithin:
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("the last backup of the secret folders is %s old", age.Round(time.Minute)) + lastSecretError(sf), Fix: fix}
	case sf.Last != nil && sf.Last.Error != "":
		return doctor.Result{Status: doctor.Warn, Detail: detail + lastSecretError(sf), Fix: fix}
	case sf.PruneHold != nil:
		return doctor.Result{Status: doctor.Warn, Detail: detail + fmt.Sprintf("; pruning is held: version %s has %d files, the one before %d", sf.PruneHold.Snapshot, sf.PruneHold.Files, sf.PruneHold.PrevFiles),
			Fix: "look at what was deleted, then Allow pruning on the dashboard's Vault page"}
	}
	return doctor.Result{Status: doctor.OK, Detail: detail}
}

func lastSecretError(sf *secretFoldersStatus) string {
	if sf.Last != nil && sf.Last.Error != "" {
		return "; the last try failed: " + sf.Last.Error
	}
	return ""
}
