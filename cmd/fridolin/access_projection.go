package main

// policyOf is the command's read-only projection from a config snapshot.
// The policy never retains or rereads the config store.
func policyOf(cfg *Config) accessPolicy {
	return accessPolicy{AllowedNumbers: cfg.AllowedNumbers, AllowAnyDM: cfg.AllowsAnyDM(), AllowedGroups: cfg.AllowedGroups, GroupMembers: cfg.GroupMembers, Triggers: cfg.Triggers}
}
