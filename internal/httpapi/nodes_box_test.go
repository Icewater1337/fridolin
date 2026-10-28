package httpapi

import (
	"strings"
	"testing"
)

func TestBoxCommands(t *testing.T) {
	src := BoxSourceFromEnv(func(name string) string {
		return map[string]string{"FRIDOLIN_BOX_SOURCE": " https://example.com/fridolin/abc/ ", "FRIDOLIN_BOX_REGISTRY": "ghcr.io/owner"}[name]
	}, "abc12345")
	sh, ps := BoxCommands("fj-code", "core.example.ts.net:8766", src)
	if sh != "curl -fsSL https://example.com/fridolin/abc/container/box.sh | sh -s -- --registry ghcr.io/owner --version abc12345 --core core.example.ts.net:8766 --code fj-code" {
		t.Errorf("sh: %s", sh)
	}
	if !strings.HasPrefix(ps, "& ([scriptblock]::Create((irm https://example.com/fridolin/abc/container/box.ps1))) -Registry ghcr.io/owner ") {
		t.Errorf("PowerShell: %s", ps)
	}
	if sh, ps := BoxCommands("fj-code", "core:8766", BoxSourceFromEnv(func(string) string { return "" }, "abc12345")); sh != "" || ps != "" {
		t.Errorf("a core that does not know its box source offers %q, %q", sh, ps)
	}
}

func TestNativeCommands(t *testing.T) {
	src := BoxSourceFromEnv(func(name string) string {
		return map[string]string{"FRIDOLIN_BOX_SOURCE": "https://example.com/fridolin/abc"}[name]
	}, "abc12345")
	sh, ps := NativeCommands("fj-code", "core.example.ts.net:8766", src)
	if sh != "curl -fsSL https://example.com/fridolin/abc/container/node.sh | sh -s -- --core core.example.ts.net:8766 --code fj-code" {
		t.Errorf("sh: %s", sh)
	}
	if ps != "& ([scriptblock]::Create((irm https://example.com/fridolin/abc/container/node.ps1))) -Core core.example.ts.net:8766 -Code fj-code" {
		t.Errorf("PowerShell: %s", ps)
	}
	if sh, ps := NativeCommands("fj-code", "core:8766", BoxSourceFromEnv(func(string) string { return "" }, "abc12345")); sh != "" || ps != "" {
		t.Errorf("a core that does not know where its scripts are offers %q, %q", sh, ps)
	}
}
