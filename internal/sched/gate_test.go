package sched

import (
	"strings"
	"testing"
)

func TestParseGate(t *testing.T) {
	ok := []struct {
		out  string
		wake bool
		data string
	}{
		{`{"wake": false}`, false, ""},
		{"checking the inbox\n3 new files\n{\"wake\": true, \"data\": {\"files\": [\"a.pdf\", \"b.pdf\"]}}\n\n", true, "{\n  \"files\": [\n    \"a.pdf\",\n    \"b.pdf\"\n  ]\n}"},
		{`{"wake": true}`, true, ""},
		{`{"wake": true, "data": null}`, true, ""},
		{`  {"wake": true, "data": 3, "why": "more"}  `, true, "3"},
		// data counts only when it wakes the brain.
		{`{"wake": false, "data": {"x": 1}}`, false, ""},
	}
	for _, c := range ok {
		g, err := ParseGate(c.out)
		if err != nil || g.Wake != c.wake || string(g.Data) != c.data {
			t.Errorf("%q: %+v %q %v", c.out, g, g.Data, err)
		}
	}
	bad := map[string]string{
		"":                               "wrote nothing",
		"  \n ":                          "wrote nothing",
		"done":                           "no JSON object",
		`{"wake": true}` + "\nall done":  "no JSON object",
		`{"wake": "yes"}`:                "no JSON object",
		`{"data": {}}`:                   `no "wake"`,
		`{"wake": true} {"wake": false}`: "no JSON object",
		`{"wake": true, "data": "` + strings.Repeat("x", MaxGateData) + `"}`: "more than the 64 KiB",
	}
	for out, want := range bad {
		if _, err := ParseGate(out); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40q: %v, want %q", out, err, want)
		}
	}
}

func TestGateOutputStopsAtItsCap(t *testing.T) {
	over := 0
	o := &GateOutput{Over: func() { over++ }}
	out, errw := o.Stdout(), o.Stderr()
	_, _ = out.Write([]byte("hello\n"))
	_, _ = errw.Write([]byte("a warning\n"))
	if s, e := o.Text(); s != "hello\n" || e != "a warning\n" || o.Exceeded() {
		t.Fatalf("%q %q %v", s, e, o.Exceeded())
	}
	chunk := make([]byte, MaxGateOutput/2)
	_, _ = out.Write(chunk)
	_, _ = errw.Write(chunk)
	_, _ = out.Write([]byte("more"))
	if !o.Exceeded() || over != 1 {
		t.Errorf("exceeded %v, Over called %d times", o.Exceeded(), over)
	}
	if s, _ := o.Text(); len(s) > MaxGateOutput {
		t.Errorf("kept %d bytes", len(s))
	}
}
