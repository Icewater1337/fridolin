package plaud

import (
	"encoding/json"
	"go/version"
	"runtime"
	"testing"
)

// These literal strings were captured before the package move on both
// supported Go versions. Keep each runtime's diagnostic contract unchanged.
func TestPlaudJSONDiagnostics(t *testing.T) {
	cases := []struct {
		name, input string
		value       any
	}{
		{"state pending map", `{"version":2,"pending":false}`, &plaudState{}},
		{"state pending item", `{"version":2,"pending":{"a":false}}`, &plaudState{}},
		{"state pending field", `{"version":2,"pending":{"a":{"runs":"x"}}}`, &plaudState{}},
		{"state login", `{"version":2,"login":false}`, &plaudState{}},
		{"state given up", `{"version":2,"given_up":false}`, &plaudState{}},
		{"state given up item", `{"version":2,"given_up":[false]}`, &plaudState{}},
		{"state forwarded", `{"version":2,"forwarded":false}`, &plaudState{}},
		{"state seeded", `{"version":2,"seeded":"x"}`, &plaudState{}},
		{"state old pending", `{"pending":{"a":false}}`, &plaudState{}},
		{"devices root", `false`, &plaudDevices{}},
		{"devices list", `{"devices":false}`, &plaudDevices{}},
		{"devices key", `{"devices":[{"key":false}]}`, &plaudDevices{}},
		{"pairings list", `{"pairings":false}`, &plaudDevices{}},
		{"developer root", `false`, &Developer{}},
		{"developer field", `{"client_id":false}`, &Developer{}},
	}
	want := map[string]string{
		"state pending map":   "json: cannot unmarshal bool into Go struct field current.pending of type map[string]*main.plaudPending",
		"state pending item":  "json: cannot unmarshal bool into Go struct field current.pending.a of type main.plaudPending",
		"state pending field": "json: cannot unmarshal string into Go struct field current.pending.a.runs of type int",
		"state login":         "json: cannot unmarshal bool into Go struct field current.login of type main.plaudLogin",
		"state given up":      "json: cannot unmarshal bool into Go struct field current.given_up of type []main.plaudGivenUp",
		"state given up item": "json: cannot unmarshal bool into current.given_up.0 of type main.plaudGivenUp",
		"state forwarded":     "json: cannot unmarshal bool into Go struct field current.forwarded of type []main.PlaudForward",
		"state seeded":        "json: cannot unmarshal string into Go struct field current.seeded of type bool",
		"state old pending":   "json: cannot unmarshal bool into Go struct field .pending.a of type float64",
		"devices root":        "json: cannot unmarshal bool into Go value of type main.plaudDevices",
		"devices list":        "json: cannot unmarshal bool into Go struct field plaudDevices.devices of type []main.plaudDevice",
		"devices key":         "json: cannot unmarshal bool into Go struct field plaudDevices.devices.0.key of type main.plaudJWK",
		"pairings list":       "json: cannot unmarshal bool into Go struct field plaudDevices.pairings of type []main.plaudPairing",
		"developer root":      "json: cannot unmarshal bool into Go value of type main.plaudDeveloper",
		"developer field":     "json: cannot unmarshal bool into Go struct field plaudDeveloper.client_id of type string",
	}
	if version.Compare(version.Lang(runtime.Version()), "go1.27") < 0 {
		want["state pending item"] = "json: cannot unmarshal bool into Go struct field current.pending of type main.plaudPending"
		want["state pending field"] = "json: cannot unmarshal string into Go struct field plaudPending.pending.runs of type int"
		want["state given up item"] = "json: cannot unmarshal bool into Go struct field current.given_up of type main.plaudGivenUp"
		want["state old pending"] = "json: cannot unmarshal bool into Go struct field .pending of type float64"
		want["devices key"] = "json: cannot unmarshal bool into Go struct field plaudDevice.devices.key of type main.plaudJWK"
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DecodeError(json.Unmarshal([]byte(tc.input), tc.value))
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			if err.Error() != want[tc.name] {
				t.Fatalf("diagnostic = %q, want %q", err.Error(), want[tc.name])
			}
		})
	}
}
