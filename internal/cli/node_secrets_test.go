package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/hostsecrets"
)

// fridolin node secrets lists the node's secret and stores the Claude
// token, typed or from stdin, checked first; it keeps nothing else, and
// replaces one only with --replace.
func TestNodeSecrets(t *testing.T) {
	t.Parallel()
	store := hostsecrets.Files{Dir: t.TempDir()}
	open := func(string) (hostsecrets.Store, string, error) { return store, "the test store", nil }
	run := func(args []string, replace bool, typed, stdin string) (int, string, string) {
		var out, errb bytes.Buffer
		var term terminalIO
		if typed != "" {
			term.readHidden = func(string) (string, error) { return typed, nil }
		}
		code := nodeSecrets(args, replace, t.TempDir(), open, appPackage{}, term, strings.NewReader(stdin), &out, &errb)
		return code, out.String(), errb.String()
	}
	const tok = "sk-ant-oat01-madeUp_cli-123" // personal-data:ignore (a made-up token)

	if code, out, _ := run(nil, false, "", ""); code != 0 || !strings.Contains(out, "missing  claude.oauth.token") || !strings.Contains(out, "the test store") {
		t.Errorf("list: %d %q", code, out)
	}
	if code, _, errs := run([]string{"set", "github.token"}, false, "", tok); code != 2 || !strings.Contains(errs, "a node keeps only claude.oauth.token") {
		t.Errorf("another secret: %d %q", code, errs)
	}
	if code, _, errs := run([]string{"set", "claude.oauth.token"}, false, "", "ABCD-1234#state"); code != 1 || !strings.Contains(errs, "sign-in code from the browser") {
		t.Errorf("the browser's code: %d %q", code, errs)
	}
	if ok, _ := store.Has(hostsecrets.ClaudeOAuthToken); ok {
		t.Fatal("a token that was refused was stored")
	}
	if code, out, errs := run([]string{"set", "claude.oauth.token"}, false, tok, ""); code != 0 || !strings.Contains(out, "Stored claude.oauth.token in the test store") {
		t.Fatalf("typed: %d %q %q", code, out, errs)
	}
	if got, _ := store.Get(hostsecrets.ClaudeOAuthToken); got != tok {
		t.Errorf("stored %q", got)
	}
	if code, _, errs := run([]string{"set", "fridolin.claude.oauth.token"}, false, "", tok+"\n"); code != 1 || !strings.Contains(errs, "pass --replace") {
		t.Errorf("again without --replace: %d %q", code, errs)
	}
	if code, _, errs := run([]string{"set", "claude.oauth.token"}, true, "", "  "+tok+"x\r\n"); code != 0 {
		t.Errorf("--replace from stdin: %d %q", code, errs)
	}
	if got, _ := store.Get(hostsecrets.ClaudeOAuthToken); got != tok+"x" {
		t.Errorf("replaced with %q", got)
	}
	if code, out, _ := run([]string{"list"}, false, "", ""); code != 0 || !strings.Contains(out, "stored   claude.oauth.token") {
		t.Errorf("list after: %d %q", code, out)
	}
	if code, _, _ := run([]string{"rm", "claude.oauth.token"}, false, "", ""); code != 2 {
		t.Errorf("an unknown subcommand: %d", code)
	}
	failing := func(string) (hostsecrets.Store, string, error) {
		return nil, "", errors.New("reading the node's key: denied")
	}
	var errb bytes.Buffer
	if code := nodeSecrets([]string{"list"}, false, t.TempDir(), failing, appPackage{}, terminalIO{}, strings.NewReader(""), &bytes.Buffer{}, &errb); code != 1 || !strings.Contains(errb.String(), "reading the node's key") {
		t.Errorf("no store: %d %q", code, errb.String())
	}
}
