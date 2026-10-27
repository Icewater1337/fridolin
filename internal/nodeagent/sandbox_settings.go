package nodeagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Fridolin owns sbx's global settings on the node's machine (#149): the
// network policy deny-all, the SSH agent and the host's browser out of
// reach. `fridolin node sandbox setup` makes them hold, and the agent
// sets them again at each check when they drifted.

// sbxRestartSettings are owned settings that take full effect only after
// `sbx daemon restart` (sbx says so when one is set).
var sbxRestartSettings = map[string]bool{"ssh.agentForwardingEnabled": true}

// errPolicyNotDenyAll is a global network policy other than deny-all,
// which only setup replaces: `sbx policy reset` deletes every rule and
// stops the daemon, and every sandbox with it.
var errPolicyNotDenyAll = errors.New("sbx's global network policy is not deny-all: run fridolin node sandbox setup on this machine")

// sbxPolicyUnset reports whether sbx's error says the global network
// policy was never initialized.
func sbxPolicyUnset(stderr []byte) bool {
	return strings.Contains(strings.ToLower(string(stderr)), "not been initialized")
}

// sbxEnforce makes fridolin's global settings hold: it sets each owned
// setting that drifted, and initializes the network policy to deny-all
// where there is none. Another policy is replaced only when replace says
// so (setup). It returns what it changed, and whether one of the changes
// waits for a daemon restart.
func sbxEnforce(ctx context.Context, d sbxDriver, replace bool) (changed []string, restart bool, err error) {
	settings, stderr, err := d.run(ctx, nil, nil, "settings", "ls", "--json")
	if err != nil {
		return nil, false, fmt.Errorf("sbx settings ls: %w", sbxError(err, stderr))
	}
	for _, k := range sbxSettingsDrift(settings) {
		v := sbxOwnedSettings[k]
		if _, stderr, err := d.run(ctx, nil, nil, "settings", "set", k, v); err != nil {
			return changed, restart, fmt.Errorf("sbx settings set %s %s: %w", k, v, sbxError(err, stderr))
		}
		changed = append(changed, k+"="+v)
		restart = restart || sbxRestartSettings[k]
	}
	policy, stderr, err := d.run(ctx, nil, nil, "policy", "ls", "--json")
	switch {
	case err == nil && sbxDenyAll(policy):
		return changed, restart, nil
	case err != nil && !sbxPolicyUnset(stderr):
		return changed, restart, fmt.Errorf("sbx policy ls: %w", sbxError(err, stderr))
	case err == nil && !replace:
		return changed, restart, errPolicyNotDenyAll
	case err == nil:
		if _, stderr, err := d.run(ctx, nil, nil, "policy", "reset", "--force"); err != nil {
			return changed, restart, fmt.Errorf("sbx policy reset: %w", sbxError(err, stderr))
		}
		changed = append(changed, "network policy reset")
	}
	if _, stderr, err := d.run(ctx, nil, nil, "policy", "init", "deny-all"); err != nil {
		return changed, restart, fmt.Errorf("sbx policy init deny-all: %w", sbxError(err, stderr))
	}
	return append(changed, "network policy deny-all"), restart, nil
}

// sbxSettingsLook reports whether fridolin's global settings hold,
// changing nothing (doctor), and says why not when they do not.
func sbxSettingsLook(ctx context.Context, d sbxDriver) (bool, string) {
	settings, stderr, err := d.run(ctx, nil, nil, "settings", "ls", "--json")
	if err != nil {
		return false, "sbx settings ls: " + sbxError(err, stderr).Error()
	}
	var why []string
	for _, k := range sbxSettingsDrift(settings) {
		why = append(why, k+" is not "+sbxOwnedSettings[k])
	}
	policy, stderr, err := d.run(ctx, nil, nil, "policy", "ls", "--json")
	switch {
	case err != nil && sbxPolicyUnset(stderr):
		why = append(why, "the network policy is not initialized")
	case err != nil:
		why = append(why, "sbx policy ls: "+sbxError(err, stderr).Error())
	case !sbxDenyAll(policy):
		why = append(why, "the network policy is not deny-all")
	}
	return len(why) == 0, strings.Join(why, "; ")
}

// sandboxSettings makes fridolin's global settings hold where that stops
// no sandbox, and says why not when they do not.
func (a *Agent) sandboxSettings(ctx context.Context, d sbxDriver) (bool, string) {
	changed, restart, err := sbxEnforce(ctx, d, false)
	if len(changed) > 0 {
		note := ""
		if restart {
			note = " (in full after sbx's next daemon restart)"
		}
		a.logf("node: set sbx's %s%s", strings.Join(changed, ", "), note)
	}
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}
