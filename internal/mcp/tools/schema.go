package tools

import "encoding/json"

// Schema is a JSON Schema object with props, of which required must be
// given, and nothing else.
func Schema(props map[string]any, required ...string) json.RawMessage {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	data, _ := json.Marshal(s)
	return data
}

// Str is a string property described by desc, for Schema.
func Str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
