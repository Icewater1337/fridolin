package app

import (
	"context"
	"fmt"
	"time"
)

// The vault's doctor checks (M3's gate): they read what the running
// vault last wrote to core/vault-status.json, and apply only with
// vault.enabled.
var vaultDoctorChecks = []DoctorCheck{
	{ID: "vault", Title: "vault reachable", Required: true, Applies: vaultOn, Run: checkVaultReachable},
	{ID: "vault-upload", Title: "vault uploads within the hour", Required: true, Applies: vaultOn, Run: checkVaultUpload},
	{ID: "vault-snapshot", Title: "snapshot within 26 hours", Required: true, Applies: vaultOn, Run: checkVaultSnapshot},
	{ID: "vault-litestream", Title: "Litestream replicating bridge.db and vault.db", Required: true, Applies: vaultOn, Run: checkVaultLitestream},
}

func init() { DoctorRegistry = append(DoctorRegistry, vaultDoctorChecks...) }

// The limits of the vault's checks.
const (
	vaultStatusFresh  = 5 * time.Minute  // the running vault writes its status each minute
	vaultUploadWithin = time.Hour        // a file waits at most this long for its upload
	vaultSnapWithin   = 26 * time.Hour   // a nightly snapshot, with two hours to spare
	vaultReachWithin  = 30 * time.Minute // the vault pings the cold tier every 10 minutes
)

func vaultOn(env *DoctorEnv) bool { return env.Config != nil && env.Config.Vault.Enabled }

func (env *DoctorEnv) now() time.Time {
	if env.Now != nil {
		return env.Now()
	}
	return time.Now()
}

// vaultStatusFor reads the status, or says why it is no good.
func (env *DoctorEnv) vaultStatus() (vaultStatus, *DoctorResult) {
	data, err := ReadVaultStatusFile(env.Config.DataDir, env.ReadFile)
	if err != nil {
		return vaultStatus{}, &DoctorResult{Status: DoctorFail, Detail: err.Error(), Fix: "start the bridge (fridolin service restart) and look at its log"}
	}
	st, err := parseVaultStatus(data)
	if err != nil {
		return st, &DoctorResult{Status: DoctorFail, Detail: err.Error()}
	}
	if age := env.now().Sub(st.UpdatedAt); age > vaultStatusFresh {
		return st, &DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("the vault last wrote its status %s ago: the bridge does not run it", age.Round(time.Second)), Fix: "fridolin service restart, then fridolin vault status"}
	}
	if st.State != vaultRunning {
		return st, &DoctorResult{Status: DoctorFail, Detail: "the vault is " + st.State + ": " + st.Error, Fix: "fridolin vault status, and the bridge's log"}
	}
	return st, nil
}

func checkVaultReachable(_ context.Context, env *DoctorEnv) DoctorResult {
	st, bad := env.vaultStatus()
	if bad != nil {
		return *bad
	}
	r := st.Reachable
	switch {
	case r == nil:
		return DoctorResult{Status: DoctorWarn, Detail: "the vault has not reached the cold tier yet"}
	case !r.OK:
		return DoctorResult{Status: DoctorFail, Detail: "the cold tier does not answer: " + r.Error, Fix: "check the B2 keys (fridolin.b2.vault.keyid, .key) and the network"}
	case env.now().Sub(r.At) > vaultReachWithin:
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("last reached %s ago", env.now().Sub(r.At).Round(time.Second))}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("reached %s ago", env.now().Sub(r.At).Round(time.Second))}
}

// checkVaultUpload: nothing waits longer than an hour for its upload. A
// quiet vault, with every file in the cold tier, passes however long ago
// it last uploaded.
func checkVaultUpload(_ context.Context, env *DoctorEnv) DoctorResult {
	st, bad := env.vaultStatus()
	if bad != nil {
		return *bad
	}
	last := "never"
	if !st.LastUpload.IsZero() {
		last = env.now().Sub(st.LastUpload).Round(time.Second).String() + " ago"
	}
	if st.Files.Hot == 0 {
		return DoctorResult{Status: DoctorOK, Detail: "every file is in the cold tier; last upload " + last}
	}
	if !st.LastUpload.IsZero() && env.now().Sub(st.LastUpload) <= vaultUploadWithin {
		return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("%d files wait for their upload; last upload %s", st.Files.Hot, last)}
	}
	detail := fmt.Sprintf("%d files wait for their upload, and the last upload was %s", st.Files.Hot, last)
	if r := st.LastSync; r != nil && r.Error != "" {
		detail += ": " + r.Error
	}
	return DoctorResult{Status: DoctorFail, Detail: detail, Fix: "fridolin vault sync, then fridolin vault status"}
}

func checkVaultSnapshot(_ context.Context, env *DoctorEnv) DoctorResult {
	st, bad := env.vaultStatus()
	if bad != nil {
		return *bad
	}
	sn := st.Snapshots
	if !sn.Enabled {
		return DoctorResult{Status: DoctorFail, Detail: "snapshots are off: " + sn.Error, Fix: "add the snapshots bucket's B2 key to the host secrets (fridolin.b2.snapshots.keyid, .key) and install restic"}
	}
	var last *snapshotRecord
	for _, r := range []*snapshotRecord{sn.LastNightly, sn.LastHourly} {
		if r != nil && r.ID != "" && (last == nil || r.At.After(last.At)) {
			last = r
		}
	}
	if last == nil {
		return DoctorResult{Status: DoctorFail, Detail: "no snapshot yet", Fix: "fridolin vault status; the first one runs within a minute of the vault's start"}
	}
	age := env.now().Sub(last.At)
	if age > vaultSnapWithin {
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("the last snapshot (%s, %s) is %s old", last.Kind, last.ID, age.Round(time.Minute)), Fix: "fridolin vault status and the bridge's log"}
	}
	detail := fmt.Sprintf("%s snapshot %s, %s ago", last.Kind, last.ID, age.Round(time.Second))
	if h := sn.DeletionHold; h != nil {
		return DoctorResult{Status: DoctorWarn, Detail: detail + fmt.Sprintf("; pruning is held: snapshot %s has %d files, the one before %d", h.Snapshot, h.Files, h.PrevFiles), Fix: "look at what was deleted, then Allow pruning on the dashboard's Vault page"}
	}
	return DoctorResult{Status: DoctorOK, Detail: detail}
}

func checkVaultLitestream(_ context.Context, env *DoctorEnv) DoctorResult {
	st, bad := env.vaultStatus()
	if bad != nil {
		return *bad
	}
	l := st.Litestream
	switch {
	case !l.Enabled:
		return DoctorResult{Status: DoctorFail, Detail: "Litestream does not run: it is not installed, or the snapshots bucket has no key", Fix: installFix(env.GOOS, "benbjohnson/litestream/litestream", "")}
	case !l.Running:
		return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("Litestream is not running (%d restarts): %s", l.Restarts, l.LastError), Fix: "the bridge's log says why; it restarts Litestream by itself"}
	}
	return DoctorResult{Status: DoctorOK, Detail: fmt.Sprintf("pid %d, %d restarts", l.PID, l.Restarts)}
}
