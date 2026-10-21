//go:build windows

package nodeagent

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestToastXMLOnWindows runs the toast script with Windows PowerShell up
// to the point of showing it and checks that the hostile text arrived in
// the toast's XML as text, unchanged.
func TestToastXMLOnWindows(t *testing.T) {
	ps := windowsPowerShell()
	if _, err := os.Stat(ps); err != nil {
		t.Skipf("no Windows PowerShell: %v", err)
	}
	title, msg := clean(hostileText, 128), clean(hostileText, 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, ps, powershellArgs(toastScript(title, msg, false))...).CombinedOutput()
	if err != nil {
		t.Fatalf("powershell: %v\n%s", err, out)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	var toast struct {
		Binding struct {
			Template string   `xml:"template,attr"`
			Text     []string `xml:"text"`
		} `xml:"visual>binding"`
	}
	if err := xml.Unmarshal(raw, &toast); err != nil {
		t.Fatalf("the toast's XML %s: %v", raw, err)
	}
	if toast.Binding.Template != "ToastGeneric" || len(toast.Binding.Text) != 2 ||
		toast.Binding.Text[0] != title || toast.Binding.Text[1] != msg {
		t.Fatalf("toast = %+v from %s", toast, raw)
	}
}
