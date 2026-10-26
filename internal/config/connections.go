package config

import (
	"fmt"
	"math"
	"regexp"

	"github.com/Icewater1337/fridolin/internal/manifest"
)

// ConnectionsConfig is which brains may use Fridolin's connections to
// OpenAI and ElevenLabs, and what each may spend a month
// (docs/CONNECTIONS.md). The keys are not here: the bridge keeps them in
// <data_dir>/connections, set on the dashboard's Integrations page.
type ConnectionsConfig struct {
	OpenAI     ConnectionConfig `json:"openai,omitzero"`
	ElevenLabs ElevenLabsConfig `json:"elevenlabs,omitzero"`
}

// ConnectionConfig is one connection's policy.
type ConnectionConfig struct {
	// Brains are the brains whose runs may use the connection through
	// their MCP tools. None by default.
	Brains []string `json:"brains,omitempty"`
	// BrainMonthlyUSD is each brain's estimated monthly limit; 0 is none
	// of Fridolin's.
	BrainMonthlyUSD float64 `json:"brain_monthly_usd,omitempty"`
}

// ElevenLabsConfig is ElevenLabs' policy, with the voice text-to-speech
// uses when a caller names none.
type ElevenLabsConfig struct {
	Brains          []string `json:"brains,omitempty"`
	BrainMonthlyUSD float64  `json:"brain_monthly_usd,omitempty"`
	Voice           string   `json:"voice,omitempty"`
}

// Policy is the part ElevenLabs shares with OpenAI.
func (c ElevenLabsConfig) Policy() ConnectionConfig {
	return ConnectionConfig{Brains: c.Brains, BrainMonthlyUSD: c.BrainMonthlyUSD}
}

// MaxConnectionMonthlyUSD bounds brain_monthly_usd.
const MaxConnectionMonthlyUSD = 10000

// voiceIDRE is an ElevenLabs voice id.
var voiceIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{0,64}$`)

// Validate refuses a policy the bridge cannot apply.
func (c ConnectionsConfig) Validate() error {
	for _, conn := range []struct {
		name string
		c    ConnectionConfig
	}{{"openai", c.OpenAI}, {"elevenlabs", c.ElevenLabs.Policy()}} {
		for _, id := range conn.c.Brains {
			if !manifest.ValidID(id) {
				return fmt.Errorf("connections.%s.brains: %q is not a brain id", conn.name, id)
			}
		}
		if v := conn.c.BrainMonthlyUSD; math.IsNaN(v) || v < 0 || v > MaxConnectionMonthlyUSD {
			return fmt.Errorf("connections.%s.brain_monthly_usd must be between 0 (no limit) and %d", conn.name, MaxConnectionMonthlyUSD)
		}
	}
	if !voiceIDRE.MatchString(c.ElevenLabs.Voice) {
		return fmt.Errorf("connections.elevenlabs.voice %q is not a voice id: letters, digits, _ and -", c.ElevenLabs.Voice)
	}
	return nil
}
