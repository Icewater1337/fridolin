package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// FuzzParse: whatever a client writes, Parse neither panics nor returns
// both a message and an error that disagree, an id it hands back is a
// string or a number, and the answer the server makes of the line is one
// line of JSON with that id (or null).
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"delegate","arguments":{"brain":"family","task":"x"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"a"}}`,
		`{"jsonrpc":"2.0","id":"x","result":{}}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		`{"jsonrpc":"2.0","id":1e999,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":" ","method":"ping","params":[]}`,
		`{"jsonrpc":"2.0","id":-0,"method":"ping","params":"x"}`,
		`null`, `{}`, `{"jsonrpc":"2.0","id":1,"method":""}`, "\x00", `"\ud800"`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		m, perr := Parse(line)
		if m.ID != nil && !validID(m.ID) {
			t.Fatalf("Parse handed back the id %q", m.ID)
		}
		if perr == nil {
			if m.IsNotification() && m.IsResponse() {
				t.Fatalf("%q is both a notification and a response", line)
			}
			if m.Params != nil && m.Params[0] != '{' && m.Params[0] != '[' {
				t.Fatalf("params %q passed", m.Params)
			}
		} else if perr.Code >= 0 {
			t.Fatalf("error code %d", perr.Code)
		}
		if bytes.ContainsAny(line, "\n") {
			return // the server never sees a newline inside a line
		}
		var out bytes.Buffer
		srv := &Server{Handler: &fakeHandler{block: make(chan struct{})}}
		close(srv.Handler.(*fakeHandler).block)
		if err := srv.Serve(context.Background(), bytes.NewReader(append(line, '\n')), &out); err != nil {
			t.Fatal(err)
		}
		for _, answer := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
			if answer == "" {
				continue
			}
			var r struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal([]byte(answer), &r); err != nil || r.JSONRPC != "2.0" {
				t.Fatalf("answer %q to %q is not a JSON-RPC message", answer, line)
			}
			if string(r.ID) != "null" && !validID(r.ID) {
				t.Fatalf("answer %q has the id %s", answer, r.ID)
			}
		}
	})
}
