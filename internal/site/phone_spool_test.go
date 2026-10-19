package site

import (
	"context"

	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The site passes a phone's request on through its spool; the bridge
// answers with what the request's handler says.
func TestSiteSpoolPassesPhoneRequestsOn(t *testing.T) {
	m := newTestSite(t)
	var got []string
	m.hooks.Device = func(op, assertion string, body []byte) (map[string]any, error) {
		got = append(got, op+"|"+assertion+"|"+string(body))
		return map[string]any{"device": "dev_1"}, nil
	}
	dir := filepath.Join(m.spoolDir(), strings.Repeat("d", 24))
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "body.txt"), []byte("text"), 0o600)
	os.WriteFile(filepath.Join(dir, "device.json"), []byte(`{"op":"transcript","assertion":"a.b.c"}`), 0o600)

	m.processSpool(context.Background())

	if !slices.Equal(got, []string{"transcript|a.b.c|text"}) {
		t.Errorf("the bridge got %v", got)
	}
	if a := readSpoolAnswer(t, dir); a["ok"] != true || a["device"] != "dev_1" {
		t.Errorf("answer = %v", a)
	}
}
