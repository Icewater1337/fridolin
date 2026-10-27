package nodeagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// login puts the token into the sandbox on stdin, never in sbx's
// arguments; status reports it without the token, and without booting a
// stopped sandbox; rm removes the sandbox and the state, not the base.
func TestFrideSandboxLoginStatusRemove(t *testing.T) {
	t.Parallel()
	o, fake, _ := createdSandbox(t)
	ctx := context.Background()
	const tok = "sk-ant-oat01-abc_DEF-123" // personal-data:ignore (a made-up token)
	if err := FrideSandboxLogin(ctx, o, bom+" "+tok+"\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := fakeFile(fake, "token"); got != tok+"\n" {
		t.Errorf("the token in the sandbox: %q", got)
	}
	calls, _ := os.ReadFile(filepath.Join(fake, "calls.log"))
	if strings.Contains(string(calls), "abc_DEF") {
		t.Error("the token is in sbx's arguments")
	}
	if err := FrideSandboxLogin(ctx, o, "4/0Ab#code"); err == nil {
		t.Error("the browser's code was taken for a token")
	}
	r, err := FrideSandboxStatus(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.State == nil || r.Sandbox != "running" || r.Inside["login"] != "yes" || r.Inside["git"] != "yes" || r.Inside["commit"] != testFridePin || r.Answers || r.Paired {
		t.Errorf("status %+v", r)
	}
	if strings.Contains(strings.Join(mapValues(r.Inside), " "), "abc_DEF") {
		t.Error("status carries the token")
	}
	if err := FrideSandboxRemove(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !called(fakeCalls(t, fake), "rm", "-f", FrideSandboxName) {
		t.Error("rm did not remove the sandbox")
	}
	if st, err := LoadFrideSandbox(o.Dir); st != nil || err != nil {
		t.Errorf("state after rm: %v %v", st, err)
	}
	if _, err := os.Stat(o.Base); err != nil {
		t.Errorf("rm took the base folder: %v", err)
	}
	if err := FrideSandboxLogin(ctx, o, tok); err == nil || !strings.Contains(err.Error(), "create") {
		t.Errorf("login without a sandbox: %v", err)
	}
}

// bom is UTF-8's byte order mark, which Windows PowerShell can put
// before what it pipes.
var bom = string(rune(0xfeff))

func TestCheckClaudeToken(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"sk-ant-oat01-abc":         "sk-ant-oat01-abc",   // personal-data:ignore (made up)
		" sk-ant-oat01-abc\r\n":    "sk-ant-oat01-abc",   // personal-data:ignore (made up)
		bom + "sk-ant-oat01-a_b-c": "sk-ant-oat01-a_b-c", // personal-data:ignore (made up)
		"":                         "",
		"sk-ant-oat":               "",
		"sk-ant-api03-abc":         "", // personal-data:ignore (made up)
		"code#state":               "",
		"sk-ant-oat01-a!b":         "", // personal-data:ignore (made up)
	} {
		got, err := CheckClaudeToken(in)
		if got != want || (err == nil) != (want != "") {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}
