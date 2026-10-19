package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// testPhone is a phone's key pair, as the app makes it in its keystore.
type testPhone struct {
	key *ecdsa.PrivateKey
	jwk plaudJWK
}

func newTestPhone(t *testing.T) testPhone {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.PublicKey.Bytes() // 0x04 || x || y
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return testPhone{k, plaudJWK{Kty: "EC", Crv: "P-256", X: enc(b[1:33]), Y: enc(b[33:])}}
}

// sign makes the compact ES256 JWT the app sends.
func (p testPhone) sign(t *testing.T, c plaudClaims) string {
	t.Helper()
	payload, _ := json.Marshal(c)
	input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, p.key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (p testPhone) pairing(t *testing.T, code string) string {
	now := time.Now().Unix()
	return p.sign(t, plaudClaims{Aud: plaudPairAud, Iat: now, Exp: now + 300, Code: code, Key: &p.jwk})
}

func (p testPhone) upload(t *testing.T, device, name, text string) string {
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(text))
	return p.sign(t, plaudClaims{Iss: device, Aud: plaudTranscriptAud, Iat: now, Exp: now + 300, Name: name, SHA256: base64.RawURLEncoding.EncodeToString(sum[:])})
}

func TestPlaudJWTIsChecked(t *testing.T) {
	phone, other := newTestPhone(t), newTestPhone(t)
	now := time.Now()
	at := func(d time.Duration) int64 { return now.Add(d).Unix() }
	good := plaudClaims{Aud: plaudPairAud, Iat: at(0), Exp: at(5 * time.Minute)}
	if _, err := verifyPlaudJWT(phone.sign(t, good), phone.jwk, plaudPairAud, now); err != nil {
		t.Fatalf("a good JWT: %v", err)
	}
	tok := phone.sign(t, good)
	parts := strings.Split(tok, ".")
	claims, _ := json.Marshal(plaudClaims{Aud: plaudPairAud, Iat: at(0), Exp: at(5 * time.Minute), Code: "changed"})
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(claims) + "." + parts[2]
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + "."
	offCurve := phone.jwk
	offCurve.Y = phone.jwk.X
	for what, c := range map[string]struct {
		tok string
		key plaudJWK
	}{
		"changed claims":      {tampered, phone.jwk},
		"another key":         {tok, other.jwk},
		"alg none":            {none, phone.jwk},
		"a key off the curve": {tok, offCurve},
		"another audience":    {phone.sign(t, plaudClaims{Aud: plaudTranscriptAud, Iat: at(0), Exp: at(time.Minute)}), phone.jwk},
		"expired":             {phone.sign(t, plaudClaims{Aud: plaudPairAud, Iat: at(-20 * time.Minute), Exp: at(-10 * time.Minute)}), phone.jwk},
		"from the future":     {phone.sign(t, plaudClaims{Aud: plaudPairAud, Iat: at(10 * time.Minute), Exp: at(12 * time.Minute)}), phone.jwk},
		"living too long":     {phone.sign(t, plaudClaims{Aud: plaudPairAud, Iat: at(0), Exp: at(time.Hour)}), phone.jwk},
		"not a JWT":           {"abc", phone.jwk},
	} {
		if _, err := verifyPlaudJWT(c.tok, c.key, plaudPairAud, now); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
}

// A phone pairs with the code its build carries, once; then each
// transcript it signs reaches the brain like any phone recording, while
// one it did not sign, or one from a phone unpaired since, does not.
func TestPlaudPhoneUploadsThroughTheSite(t *testing.T) {
	tb, fp := plaudBridge(t, `, "phone_upload": true`)
	ctx := context.Background()
	phone := newTestPhone(t)
	if err := tb.plaud.Poll(ctx); err != nil { // the first check: the inbox is empty
		t.Fatal(err)
	}

	if _, err := tb.plaud.deviceRequest("pair", phone.pairing(t, "never-made"), nil); err == nil {
		t.Fatal("a made-up pairing code paired")
	}
	code, _, err := tb.plaud.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	ans, err := tb.plaud.deviceRequest("pair", phone.pairing(t, code), nil)
	if err != nil {
		t.Fatal(err)
	}
	device, _ := ans["device"].(string)
	if !strings.HasPrefix(device, "dev_") {
		t.Fatalf("pairing answered %v", ans)
	}
	if _, err := tb.plaud.deviceRequest("pair", newTestPhone(t).pairing(t, code), nil); err == nil {
		t.Error("a pairing code worked twice")
	}
	// The site's copy has the public key and nothing else.
	site, err := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "site", "devices.json"))
	if err != nil || !strings.Contains(string(site), device) || !strings.Contains(string(site), phone.jwk.X) || strings.Contains(string(site), "hash") {
		t.Errorf("site devices.json = %s, %v", site, err)
	}

	text := "[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n[00:04 - 02:10] Speaker 2: Agreed.\n"
	name := "2026-10-18_10-00-00"
	if _, err := tb.plaud.deviceRequest("transcript", phone.upload(t, device, name, text), []byte("[00:00 - 00:01] Speaker 1: Something else.")); err == nil {
		t.Error("a transcript the phone did not sign was taken")
	}
	if _, err := tb.plaud.deviceRequest("transcript", newTestPhone(t).upload(t, device, name, text), []byte(text)); err == nil {
		t.Error("a transcript signed with another key was taken")
	}
	if _, err := tb.plaud.deviceRequest("transcript", phone.upload(t, device, "../../escape", text), []byte(text)); err == nil {
		t.Error("a transcript with a path for a name was taken")
	}
	if _, err := tb.plaud.deviceRequest("transcript", phone.upload(t, device, name, text), []byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	tb.waitPlaudSeen(t, plaudPhonePrefix+name)
	testutil.WaitFor(t, 5*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) == 1 })
	if out := tb.dev.Outbox()[0]; !strings.Contains(out.Text, `[Plaud] New recording "Phone recording" (2026-10-18 10:00, 02:10)`) {
		t.Errorf("reply = %+v", out)
	}
	if calls := fp.calls(); calls != "" {
		t.Errorf("the CLI ran:\n%s", calls)
	}
	if st := tb.plaud.appStatus(); len(st.Devices) != 1 || st.Devices[0].LastUpload.IsZero() {
		t.Errorf("app status = %+v", st)
	}

	if err := tb.plaud.RevokeDevice(device); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.plaud.deviceRequest("transcript", phone.upload(t, device, "2026-10-18_11-00-00", text), []byte(text)); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Errorf("an unpaired phone's upload: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "site", "devices.json")); strings.Contains(string(data), device) {
		t.Error("the site still lists the unpaired phone")
	}
}

func TestPlaudPhoneUploadOffRefusesPhones(t *testing.T) {
	tb, _ := plaudBridge(t, "")
	phone := newTestPhone(t)
	code, _, _ := tb.plaud.newPairing()
	if _, err := tb.plaud.deviceRequest("pair", phone.pairing(t, code), nil); err == nil || !strings.Contains(err.Error(), "phone_upload") {
		t.Errorf("pairing with uploads off: %v", err)
	}
}

// The site passes a phone's request on through its spool; the bridge
// answers with what the request's handler says.
func TestSiteSpoolPassesPhoneRequestsOn(t *testing.T) {
	m := newTestSite(t)
	var got []string
	m.hooks.device = func(op, assertion string, body []byte) (map[string]any, error) {
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
