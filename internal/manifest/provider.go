package manifest

// Provider is the agent CLI a brain's runs start: config's agent_provider
// for every brain, and a brain.json's provider for that brain alone.
type Provider string

const (
	// ProviderClaude is Claude Code, the default.
	ProviderClaude Provider = "claude"
	// ProviderCodex is OpenAI's Codex CLI.
	ProviderCodex Provider = "codex"
	// ProviderAntigravity is Google's Antigravity CLI (agy), which runs
	// Gemini models.
	ProviderAntigravity Provider = "antigravity"
)

// Providers are the providers, in the order the dashboard offers them.
func Providers() []Provider {
	return []Provider{ProviderClaude, ProviderCodex, ProviderAntigravity}
}

// ValidProvider reports whether p names a provider; "" (inherit, or the
// default) is valid too.
func ValidProvider(p Provider) bool {
	switch p {
	case "", ProviderClaude, ProviderCodex, ProviderAntigravity:
		return true
	}
	return false
}

// ProviderValues are the values agent_provider and a brain's provider
// take, "" included, for the schema test.
func ProviderValues() []string {
	out := []string{""}
	for _, p := range Providers() {
		out = append(out, string(p))
	}
	return out
}

// ProviderOf is the provider brain m runs on when the bridge's own is
// global: the brain's provider, when its brain.json names one.
func ProviderOf(global Provider, m Manifest) Provider {
	if m.Provider != "" {
		return m.Provider
	}
	if global == "" {
		return ProviderClaude
	}
	return global
}
