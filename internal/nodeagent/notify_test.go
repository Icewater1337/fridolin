package nodeagent

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

// hostileText is notification text that would break out of a PowerShell
// string or the toast's XML if it went in as text.
const hostileText = "it's \"done\" $(Remove-Item x) `n ${env:PATH} '; Write-Host pwned; ' <text>&amp;</text> ]]> <!-- \u00e4\u00df \U0001F680\nline two"

// decodeEncodedCommand undoes powershellArgs' -EncodedCommand.
func decodeEncodedCommand(t *testing.T, args []string) string {
	t.Helper()
	if len(args) < 2 || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args = %q, want -EncodedCommand last", args)
	}
	b, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil || len(b)%2 != 0 {
		t.Fatalf("encoded command: %v (%d bytes)", err, len(b))
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func TestToastScriptCarriesTheTextAsBase64Only(t *testing.T) {
	script := toastScript(hostileText, hostileText, true)
	for _, part := range []string{"$(Remove-Item", "pwned", "<text>&amp;", "]]>", "\u00e4"} {
		if strings.Contains(script, part) {
			t.Errorf("the script contains the text %q as text:\n%s", part, script)
		}
	}
	want := "'" + base64.StdEncoding.EncodeToString([]byte(hostileText)) + "'"
	if strings.Count(script, want) != 2 {
		t.Errorf("the script does not carry title and text as base64 literals:\n%s", script)
	}
	if !strings.Contains(script, `template="ToastGeneric"`) || !strings.Contains(script, "CreateTextNode") ||
		!strings.Contains(script, ".Show(") {
		t.Errorf("not a ToastGeneric toast built through the DOM:\n%s", script)
	}
	if dry := toastScript("a", "b", false); strings.Contains(dry, ".Show(") || !strings.Contains(dry, "GetXml()") {
		t.Errorf("the dry run shows the toast:\n%s", dry)
	}
}

func TestPowershellArgsRoundTrip(t *testing.T) {
	script := toastScript(hostileText, hostileText, true)
	args := powershellArgs(script)
	if got := decodeEncodedCommand(t, args); got != script {
		t.Fatalf("the encoded command decodes to\n%s\nwant\n%s", got, script)
	}
	for _, f := range []string{"-NoProfile", "-NonInteractive"} {
		if !strings.Contains(strings.Join(args, " "), f) {
			t.Errorf("args %q lack %s", args, f)
		}
	}
}

// TestLongestToastFitsACommandLine: the longest title and message clean()
// lets through, of four-byte characters, stay under Windows' limit of
// 32767 characters for a command line.
func TestLongestToastFitsACommandLine(t *testing.T) {
	title := clean(strings.Repeat("\U0001F680", 500), 128)
	msg := clean(strings.Repeat("\U0001F680", 5000), 1024)
	n := len(`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`)
	for _, a := range powershellArgs(toastScript(title, msg, true)) {
		n += len(a) + 3
	}
	if n >= 32000 {
		t.Fatalf("the command line is %d characters long", n)
	}
}
