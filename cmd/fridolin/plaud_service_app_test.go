package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// serviceTestPhone is a phone's key pair, as the app makes it in its keystore.
type serviceTestPhone struct {
	key *ecdsa.PrivateKey
	jwk plaudJWK
}

func newServiceTestPhone(t *testing.T) serviceTestPhone {
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
	return serviceTestPhone{k, plaudJWK{Kty: "EC", Crv: "P-256", X: enc(b[1:33]), Y: enc(b[33:])}}
}

// sign makes the compact ES256 JWT the app sends.
func (p serviceTestPhone) sign(t *testing.T, c plaudClaims) string {
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

func TestPlaudJWTIsChecked(t *testing.T) {
	phone, other := newServiceTestPhone(t), newServiceTestPhone(t)
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

// The service-generated code is persisted by hash and accepted only once.
// The command/site test independently supplies the same wire-format fixture
// so it can test real uploads without a private pairing-construction API.
func TestPlaudPairingCodeIsPersistedAndConsumed(t *testing.T) {
	tb, _ := plaudService(t, `, "phone_upload": true`)
	before := time.Now()
	code, expires, err := tb.plaud.newPairing()
	if err != nil {
		t.Fatal(err)
	}
	if code == "" || expires.Before(before.Add(plaudPairingLifetime)) {
		t.Fatalf("pairing = %q, %v", code, expires)
	}
	st, err := tb.plaud.loadDevices()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(code))
	if len(st.Pairings) != 1 || st.Pairings[0].Hash != hex.EncodeToString(sum[:]) || !st.Pairings[0].Expires.Equal(expires) {
		t.Fatalf("persisted pairing = %+v", st.Pairings)
	}
	phone := newServiceTestPhone(t)
	now := time.Now().Unix()
	assertion := phone.sign(t, plaudClaims{Aud: plaudPairAud, Iat: now, Exp: now + 300, Code: code, Key: &phone.jwk})
	if _, err := tb.plaud.DeviceRequest("pair", assertion, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.plaud.DeviceRequest("pair", assertion, nil); err == nil {
		t.Error("a consumed code was accepted twice")
	}
}
