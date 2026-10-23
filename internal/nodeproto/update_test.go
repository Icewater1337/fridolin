package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b   string
		cmp    int
		ok     bool
		outdat bool
	}{
		{"v1.2.3", "v1.2.3", 0, true, false},
		{"v1.2.3", "v1.2.4", -1, true, true},
		{"v1.10.0", "v1.9.9", 1, true, false},
		{"v1.2.3", "v1.2.3-4-gabc1234", -1, true, true},
		{"v1.2.3-5-gdef5678", "v1.2.3-4-gabc1234", 1, true, false},
		{"1.2.3", "v2.0.0", -1, true, true},
		{"v1.2.3-dirty", "v1.2.3", -1, false, true},
		{"dev", "v1.0.0", -1, false, true},
		{"abc1234", "def5678", -1, false, true},
		{"dev", "dev", 0, true, false},
		// One commit, abbreviated to different lengths by two clones, or
		// by a stamped and an unstamped build: the same build.
		{"0284f03", "0284f03e", 0, true, false},
		{"0284f03e", "0284f03", 0, true, false},
		{"dev-06ab6d8", "06ab6d82", 0, true, false},
		{"dev-06ab6d82", "06ab6d8", 0, true, false},
		{"0284f03e00112233445566778899aabbccddeeff", "0284f03e", 0, true, false},
		// Other commits, dirty builds and hashes too short to be one stay
		// different.
		{"0284f03e", "0284f04e", -1, false, true},
		{"dev-06ab6d8+dirty", "06ab6d82", -1, false, true},
		{"06ab6d82-dirty", "06ab6d8", -1, false, true},
		{"0284f0", "0284f03e", -1, false, true},
		{"dev-", "dev-0284f03", -1, false, true},
	} {
		cmp, ok := CompareVersions(c.a, c.b)
		if cmp != c.cmp || ok != c.ok {
			t.Errorf("CompareVersions(%q, %q) = %d, %v; want %d, %v", c.a, c.b, cmp, ok, c.cmp, c.ok)
		}
		if got := Outdated(c.a, c.b); got != c.outdat {
			t.Errorf("Outdated(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestUpdateSignature(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	sum := strings.Repeat("ab", 32)
	p := UpdateApplyParams{ID: strings.Repeat("0", 32), Version: "v1.2.3", OS: "windows", Arch: "arm64", SHA256: sum, Size: 10, TransferSHA256: sum,
		Signature: SignUpdate(key, "v1.2.3", "windows", "arm64", sum)}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	if !VerifyUpdate(pub, p) {
		t.Fatal("a good signature does not verify")
	}
	if VerifyUpdate(other, p) {
		t.Fatal("a signature verifies with another key")
	}
	for name, change := range map[string]func(*UpdateApplyParams){
		"version": func(q *UpdateApplyParams) { q.Version = "v1.2.4" },
		"os":      func(q *UpdateApplyParams) { q.OS = "linux" },
		"arch":    func(q *UpdateApplyParams) { q.Arch = "amd64" },
		"sha256":  func(q *UpdateApplyParams) { q.SHA256 = strings.Repeat("cd", 32) },
	} {
		q := p
		change(&q)
		if VerifyUpdate(pub, q) {
			t.Errorf("the signature still verifies with another %s", name)
		}
	}
	if VerifyUpdate(nil, p) {
		t.Fatal("no key verifies")
	}
	bad := p
	bad.Version = "v 1"
	if bad.Check() == nil {
		t.Fatal("a version with a space")
	}
}

func TestUpdateCapApproval(t *testing.T) {
	declared := Capabilities{Host: true, Update: &UpdateCap{}}
	if Covers(Capabilities{Host: true}, declared) {
		t.Fatal("an approval without update covers a node that offers it")
	}
	if !Covers(declared, declared) {
		t.Fatal("update approved does not cover update")
	}
	if got := Intersect(declared, Capabilities{Host: true}); got.Update != nil || got.Allows(MethodUpdateApply) {
		t.Fatal("update without its approval may be served")
	}
	if got := Intersect(declared, declared); !got.Allows(MethodUpdateWrite) || !got.Allows(MethodUpdateApply) {
		t.Fatal("approved update may not be served")
	}
	if got := declared.Summary(); got != "host, update" {
		t.Fatalf("Summary = %q", got)
	}
	if (Capabilities{Update: &UpdateCap{}}).Empty() {
		t.Fatal("a node offering update offers nothing")
	}
}
