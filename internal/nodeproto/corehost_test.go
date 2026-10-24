package nodeproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// core-host is approved for one core-host.json: a new hash (a new pin,
// turned on again) waits for the owner, and meanwhile the node may serve
// none of it.
func TestCoreHostCoveredByItsHash(t *testing.T) {
	declared := Capabilities{Host: true, CoreHost: &CoreHostCap{Hash: hashA, SendTo: []string{"0123456789ab"}}}
	if err := declared.Check(); err != nil {
		t.Fatal(err)
	}
	if !declared.Allows(MethodCoreSecretsSend) || (Capabilities{Host: true}).Allows(MethodCoreCheck) {
		t.Fatal("Allows")
	}
	if declared.Empty() || (Capabilities{CoreHost: &CoreHostCap{Hash: hashA}}).Empty() {
		t.Fatal("Empty")
	}
	if Covers(Capabilities{Host: true}, declared) {
		t.Fatal("covered without core-host approved")
	}
	approved := declared
	if !Covers(approved, declared) || Intersect(declared, approved).CoreHost == nil {
		t.Fatal("not covered by its own approval")
	}
	changed := Capabilities{Host: true, CoreHost: &CoreHostCap{Hash: hashB}}
	if Covers(approved, changed) {
		t.Fatal("a changed core-host.json is covered")
	}
	if eff := Intersect(changed, approved); eff.CoreHost != nil || !eff.Host {
		t.Fatalf("a changed core-host.json is served: %+v", eff)
	}
	if got := declared.Summary(); got != "host, core-host (trusts 1 target)" {
		t.Fatalf("summary %q", got)
	}
	if got := (Capabilities{CoreHost: &CoreHostCap{Hash: hashA, Box: true}}).Summary(); got != "core-host (box)" {
		t.Fatalf("summary %q", got)
	}
	for _, bad := range []CoreHostCap{{Hash: "x"}, {Hash: hashA, SendTo: []string{"nope"}}} {
		if err := (Capabilities{CoreHost: &bad}).Check(); err == nil {
			t.Errorf("%+v passes", bad)
		}
	}
}

func TestCoreSecretsOfferSignature(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	o := CoreSecretsOffer{Move: NewMoveID(time.Date(2026, 10, 23, 14, 3, 0, 0, time.UTC), "7f2a"), Target: DeviceIDFor(pub),
		Recipient: "age1pq1example", Expires: time.Now().Add(time.Minute).Truncate(time.Second)}
	if o.Move != "m-20261023-1403-7f2a" {
		t.Fatalf("move id %q", o.Move)
	}
	o.Signature = SignCoreSecretsOffer(key, o)
	if err := o.Check(); err != nil {
		t.Fatal(err)
	}
	if !VerifyCoreSecretsOffer(pub, o) {
		t.Fatal("does not verify")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyCoreSecretsOffer(other, o) {
		t.Fatal("verifies with another key")
	}
	swapped := o
	swapped.Recipient = "age1pq1attacker"
	if VerifyCoreSecretsOffer(pub, swapped) {
		t.Fatal("a swapped key verifies")
	}
	later := o
	later.Expires = o.Expires.Add(time.Hour)
	if VerifyCoreSecretsOffer(pub, later) {
		t.Fatal("a later expiry verifies")
	}
	if fp := Fingerprint(pub); len(strings.Fields(fp)) != 8 {
		t.Fatalf("fingerprint %q", fp)
	}
}
