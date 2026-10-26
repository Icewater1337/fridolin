package config

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestConnectionsConfig(t *testing.T) {
	t.Parallel()
	var c File
	if err := json.Unmarshal([]byte(`{"connections": {"openai": {"brains": ["personal"], "brain_monthly_usd": 10},
		"elevenlabs": {"brains": ["personal", "work"], "brain_monthly_usd": 5, "voice": "21m00Tcm4TlvDq8ikWAM"}}}`), &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Connections.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Connections.ElevenLabs.Voice != "21m00Tcm4TlvDq8ikWAM" || len(c.Connections.ElevenLabs.Brains) != 2 || c.Connections.OpenAI.BrainMonthlyUSD != 10 {
		t.Errorf("parsed %+v", c.Connections)
	}
	// Nothing set writes nothing but the section.
	data, _ := json.Marshal(ConnectionsConfig{})
	if string(data) != "{}" {
		t.Errorf("an empty section: %s", data)
	}
	for _, bad := range []ConnectionsConfig{
		{OpenAI: ConnectionConfig{Brains: []string{"Not An ID"}}},
		{OpenAI: ConnectionConfig{BrainMonthlyUSD: -1}},
		{OpenAI: ConnectionConfig{BrainMonthlyUSD: math.NaN()}},
		{ElevenLabs: ElevenLabsConfig{BrainMonthlyUSD: MaxConnectionMonthlyUSD + 1}},
		{ElevenLabs: ElevenLabsConfig{Voice: "../voice"}},
	} {
		err := bad.Validate()
		if err == nil || !strings.HasPrefix(err.Error(), "connections.") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}
