package app

import (
	"context"
	"fmt"
	"strings"
)

// The encryption check (#158): every off-machine store the running vault
// last described has two layers with independent keys, and the exports'
// asymmetric layer is post-quantum. It warns, not fails: a store that
// still has one layer is readable and backed up, and fridolin vault
// migrate-encryption moves it.
var encryptionDoctorCheck = DoctorCheck{ID: "vault-encryption", Title: "every off-machine store encrypted twice, post-quantum where asymmetric",
	Applies: vaultOn, Run: checkVaultEncryption}

func init() { DoctorRegistry = append(DoctorRegistry, encryptionDoctorCheck) }

func checkVaultEncryption(_ context.Context, env *DoctorEnv) DoctorResult {
	st, bad := env.vaultStatus()
	if bad != nil {
		return *bad
	}
	if len(st.Encryption) == 0 {
		return DoctorResult{Status: DoctorWarn, Detail: "the vault has not described its stores yet (a bridge from before #158, or not started)",
			Fix: "fridolin service restart, then fridolin doctor"}
	}
	var parts, one, copying []string
	for _, l := range st.Encryption {
		desc := l.Store + ": " + strings.Join(l.Layers, " + ")
		if len(l.Layers) < 2 {
			one = append(one, l.Store)
		}
		if l.Old == "copying" {
			copying = append(copying, l.Store)
		}
		parts = append(parts, desc)
	}
	for _, t := range st.Targets {
		switch t.Layers {
		case "restic+crypt":
			parts = append(parts, "target "+t.Name+": "+layerCrypt+" + "+layerRestic)
		case "restic":
			parts = append(parts, "target "+t.Name+": "+layerRestic)
			one = append(one, "target "+t.Name)
		}
	}
	detail := strings.Join(parts, "; ")
	switch {
	case len(one) > 0:
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("one layer only: %s. %s", strings.Join(one, ", "), detail),
			Fix: "fridolin core start (on the host) makes the missing keys; a node target needs a new, empty folder; then fridolin vault migrate-encryption --run"}
	case len(copying) > 0:
		return DoctorResult{Status: DoctorWarn, Detail: fmt.Sprintf("the single-layer copies of %s are still there. %s", strings.Join(copying, ", "), detail),
			Fix: "fridolin vault migrate-encryption --dry-run, then --run --delete-old"}
	}
	return DoctorResult{Status: DoctorOK, Detail: detail}
}
