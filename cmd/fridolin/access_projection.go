package main

import "github.com/Icewater1337/fridolin/internal/access"

// policyOf is the command's read-only projection from a config snapshot.
// The policy never retains or rereads the config store.
func policyOf(cfg *Config) access.Policy {
	return access.Policy{AllowedNumbers: cfg.AllowedNumbers, AllowAnyDM: cfg.AllowsAnyDM(), AllowedGroups: cfg.AllowedGroups, GroupMembers: cfg.GroupMembers, Triggers: cfg.Triggers}
}
