package nodeagent

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

// toastScript is the Windows PowerShell script that shows a toast with
// title and msg. The text never goes into the script as text: each is
// carried as base64 of its UTF-8 bytes, an alphabet that cannot end a
// PowerShell string, and becomes an XML text node through the DOM, so
// quotes, $(…), backticks and markup in it are just characters. With
// show false the script prints the toast's XML (as base64 of UTF-8)
// instead of showing it, which the tests read.
//
// The toast is shown under PowerShell's own AppUserModelID, so Windows
// lists it as Windows PowerShell's; registering an id of fridolin's own
// would need a Start menu shortcut.
func toastScript(title, msg string, show bool) string {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	last := `[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($x))`
	if !show {
		last = `[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($x.GetXml()))`
	}
	return strings.Join([]string{
		`$ErrorActionPreference = 'Stop'`,
		// Module loading writes progress records, which PowerShell sends as
		// CLIXML on stderr when it is not attached to a console.
		`$ProgressPreference = 'SilentlyContinue'`,
		`function D([string]$b) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)) }`,
		`$title = D '` + enc(title) + `'`,
		`$text = D '` + enc(msg) + `'`,
		`[void][Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime]`,
		`[void][Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime]`,
		`$x = New-Object Windows.Data.Xml.Dom.XmlDocument`,
		`$x.LoadXml('<toast><visual><binding template="ToastGeneric"><text></text><text></text></binding></visual></toast>')`,
		`$t = $x.GetElementsByTagName('text')`,
		`[void]$t.Item(0).AppendChild($x.CreateTextNode($title))`,
		`[void]$t.Item(1).AppendChild($x.CreateTextNode($text))`,
		`$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'`,
		last,
	}, "\n")
}

// powershellArgs runs script with Windows PowerShell (5.1, which loads
// the WinRT types; pwsh 7 does not) without a profile or prompts, as
// -EncodedCommand: base64 of the script's UTF-16LE, so nothing in it is
// parsed by a command line.
func powershellArgs(script string) []string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return []string{"-NoProfile", "-NonInteractive",
		"-WindowStyle", "Hidden", "-EncodedCommand", base64.StdEncoding.EncodeToString(b)}
}
