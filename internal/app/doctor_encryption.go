package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/Icewater1337/fridolin/internal/doctor"
)

// The encryption check (#158): every off-machine store the running vault
// last described has two layers with independent keys, and the exports'
// asymmetric layer is post-quantum. It warns, not fails: a store that
// still has one layer is readable and backed up, and fridolin vault
// migrate-encryption moves it.
var encryptionDoctorCheck = doctor.Check{ID: "vault-encryption", Title: "every off-machine store encrypted twice, post-quantum where asymmetric",
	Applies: vaultOn, Run: checkVaultEncryption}

func checkVaultEncryption(_ context.Context, env *doctor.Env) doctor.Result {
	st, bad := vaultStatusFor(env)
	if bad != nil {
		return *bad
	}
	if len(st.Encryption) == 0 {
		return doctor.Result{Status: doctor.Warn, Detail: "the vault has not described its stores yet (a bridge from before #158, or not started)",
			Fix: "fridolin service restart, then fridolin doctor"}
	}
	var parts, one, copying, byHand []string
	for _, l := range st.Encryption {
		desc := l.Store + ": " + strings.Join(l.Layers, " + ")
		if len(l.Layers) < 2 {
			one = append(one, l.Store)
		}
		if l.Old == "copying" {
			copying = append(copying, l.Store)
		}
		if l.Old == "retired" && l.Left != "" {
			// Retired: nothing fridolin reaches is left, so no warning;
			// what the owner may still delete by hand is said once here.
			byHand = append(byHand, l.Store+": "+l.Left)
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
	if len(byHand) > 0 {
		detail += ". By hand: " + strings.Join(byHand, "; ")
	}
	switch {
	case len(one) > 0:
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("one layer only: %s. %s", strings.Join(one, ", "), detail),
			Fix: "fridolin core start (on the host) makes the missing keys; a node target needs a new, empty folder; then fridolin vault migrate-encryption --run"}
	case len(copying) > 0:
		return doctor.Result{Status: doctor.Warn, Detail: fmt.Sprintf("the single-layer copies of %s are still there. %s", strings.Join(copying, ", "), detail),
			Fix: "fridolin vault migrate-encryption --dry-run, then --run --delete-old"}
	}
	return doctor.Result{Status: doctor.OK, Detail: detail}
}
