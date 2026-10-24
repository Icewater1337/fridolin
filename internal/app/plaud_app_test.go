package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

// testPhone is a phone's key pair, as the app makes it in its keystore.
type testPhone struct {
	key *ecdsa.PrivateKey
	jwk plaudPhoneFixtureKey
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
	return testPhone{k, plaudPhoneFixtureKey{Kty: "EC", Crv: "P-256", X: enc(b[1:33]), Y: enc(b[33:])}}
}

// sign makes the compact ES256 JWT the app sends.
func (p testPhone) sign(t *testing.T, c plaudPhoneFixtureClaims) string {
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
	return p.sign(t, plaudPhoneFixtureClaims{Aud: "fridolin:pair", Iat: now, Exp: now + 300, Code: code, Key: &p.jwk})
}

func (p testPhone) upload(t *testing.T, device, name, text string) string {
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(text))
	return p.sign(t, plaudPhoneFixtureClaims{Iss: device, Aud: "fridolin:plaud-transcript", Iat: now, Exp: now + 300, Name: name, SHA256: base64.RawURLEncoding.EncodeToString(sum[:])})
}

// A phone pairs with the code its build carries, once; then each
// transcript it signs reaches the brain like any phone recording, while
// one it did not sign, or one from a phone unpaired since, does not.
func TestPlaudPhoneUploadsThroughTheSite(t *testing.T) {
	t.Parallel()
	tb, fp := plaudBridge(t, `, "phone_upload": true`)
	ctx := context.Background()
	phone := newTestPhone(t)
	if err := tb.plaud.Poll(ctx); err != nil { // the first check: the inbox is empty
		t.Fatal(err)
	}

	if _, err := tb.plaud.DeviceRequest("pair", phone.pairing(t, "never-made"), nil); err == nil {
		t.Fatal("a made-up pairing code paired")
	}
	code, err := plaudFixturePairing(t, tb)
	if err != nil {
		t.Fatal(err)
	}
	ans, err := tb.plaud.DeviceRequest("pair", phone.pairing(t, code), nil)
	if err != nil {
		t.Fatal(err)
	}
	device, _ := ans["device"].(string)
	if !strings.HasPrefix(device, "dev_") {
		t.Fatalf("pairing answered %v", ans)
	}
	if _, err := tb.plaud.DeviceRequest("pair", newTestPhone(t).pairing(t, code), nil); err == nil {
		t.Error("a pairing code worked twice")
	}
	// The site's copy has the public key and nothing else.
	site, err := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "site", "devices.json"))
	if err != nil || !strings.Contains(string(site), device) || !strings.Contains(string(site), phone.jwk.X) || strings.Contains(string(site), "hash") {
		t.Errorf("site devices.json = %s, %v", site, err)
	}

	text := "[00:00 - 00:04] Speaker 1: Let's ship on Friday.\n[00:04 - 02:10] Speaker 2: Agreed.\n"
	name := "2026-10-18_10-00-00"
	if _, err := tb.plaud.DeviceRequest("transcript", phone.upload(t, device, name, text), []byte("[00:00 - 00:01] Speaker 1: Something else.")); err == nil {
		t.Error("a transcript the phone did not sign was taken")
	}
	if _, err := tb.plaud.DeviceRequest("transcript", newTestPhone(t).upload(t, device, name, text), []byte(text)); err == nil {
		t.Error("a transcript signed with another key was taken")
	}
	if _, err := tb.plaud.DeviceRequest("transcript", phone.upload(t, device, "../../escape", text), []byte(text)); err == nil {
		t.Error("a transcript with a path for a name was taken")
	}
	if _, err := tb.plaud.DeviceRequest("transcript", phone.upload(t, device, name, text), []byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := tb.plaud.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	tb.waitPlaudSeen(t, "phone_"+name)
	testutil.WaitFor(t, 5*time.Second, "brain reply", func() bool { return len(tb.dev.Outbox()) == 1 })
	if out := tb.dev.Outbox()[0]; !strings.Contains(out.Text, `[Plaud] New recording "Phone recording" (2026-10-18 10:00, 02:10)`) {
		t.Errorf("reply = %+v", out)
	}
	if calls := fp.Calls(); calls != "" {
		t.Errorf("the CLI ran:\n%s", calls)
	}
	if st := *tb.plaud.Status().App; len(st.Devices) != 1 || st.Devices[0].LastUpload.IsZero() {
		t.Errorf("app status = %+v", st)
	}

	if err := tb.plaud.RevokeDevice(device); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.plaud.DeviceRequest("transcript", phone.upload(t, device, "2026-10-18_11-00-00", text), []byte(text)); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Errorf("an unpaired phone's upload: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(tb.cfgs.Get().DataDir, "site", "devices.json")); strings.Contains(string(data), device) {
		t.Error("the site still lists the unpaired phone")
	}
}

func TestPlaudPhoneUploadOffRefusesPhones(t *testing.T) {
	t.Parallel()
	tb, _ := plaudBridge(t, "")
	phone := newTestPhone(t)
	code, _ := plaudFixturePairing(t, tb)
	if _, err := tb.plaud.DeviceRequest("pair", phone.pairing(t, code), nil); err == nil || !strings.Contains(err.Error(), "phone_upload") {
		t.Errorf("pairing with uploads off: %v", err)
	}
}

// These are the phone app's wire inputs, independent of the service decoder.
type plaudPhoneFixtureKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}
type plaudPhoneFixtureClaims struct {
	Iss    string                `json:"iss,omitempty"`
	Aud    string                `json:"aud"`
	Iat    int64                 `json:"iat"`
	Exp    int64                 `json:"exp"`
	Code   string                `json:"code,omitempty"`
	Key    *plaudPhoneFixtureKey `json:"key,omitempty"`
	Name   string                `json:"name,omitempty"`
	SHA256 string                `json:"sha256,omitempty"`
	Rid    string                `json:"rid,omitempty"`
	Enc    *plaudPhoneFixtureKey `json:"enc,omitempty"`
}

// A fresh app build persists the hash of its one-use pairing code. Arrangement
// happens before any device request; the test then exercises actual pairing.
func plaudFixturePairing(t *testing.T, tb *testBridge) (string, error) {
	t.Helper()
	code := "fixture-pairing-code"
	sum := sha256.Sum256([]byte(code))
	data, err := json.Marshal(map[string]any{"devices": []any{}, "pairings": []map[string]any{{"hash": hex.EncodeToString(sum[:]), "expires": time.Now().Add(time.Hour)}}})
	if err != nil {
		return "", err
	}
	dir := tb.cfgs.Boot().PlaudDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return code, os.WriteFile(filepath.Join(dir, "devices.json"), data, 0600)
}

// voicePair pairs phone and returns its device id.
func voicePair(t *testing.T, tb *testBridge, phone testPhone) string {
	t.Helper()
	code, err := plaudFixturePairing(t, tb)
	if err != nil {
		t.Fatal(err)
	}
	ans, err := tb.plaud.DeviceRequest("pair", phone.pairing(t, code), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ans["device"].(string)
}

// voiceQuestion asks question by voice as the app does, with enc as the
// key its answer is encrypted to.
func voiceQuestion(t *testing.T, tb *testBridge, phone testPhone, device, rid, question string, enc *ecdh.PrivateKey) (map[string]any, error) {
	t.Helper()
	b := enc.PublicKey().Bytes()
	e := base64.RawURLEncoding.EncodeToString
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(question))
	return tb.plaud.DeviceRequest("voice", phone.sign(t, plaudPhoneFixtureClaims{Iss: device, Aud: "fridolin:voice", Iat: now, Exp: now + 300, Rid: rid,
		SHA256: e(sum[:]), Enc: &plaudPhoneFixtureKey{Kty: "EC", Crv: "P-256", X: e(b[1:33]), Y: e(b[33:])}}), []byte(question))
}

// voiceAnswer fetches the answer to rid until it is there, and opens it.
func voiceAnswer(t *testing.T, tb *testBridge, phone testPhone, device, rid string, enc *ecdh.PrivateKey) string {
	t.Helper()
	var ans map[string]any
	testutil.WaitFor(t, 20*time.Second, "the voice answer", func() bool {
		now := time.Now().Unix()
		var err error
		ans, err = tb.plaud.DeviceRequest("voice_reply", phone.sign(t, plaudPhoneFixtureClaims{Iss: device, Aud: "fridolin:voice-reply", Iat: now, Exp: now + 300, Rid: rid}), nil)
		if err != nil {
			t.Fatal(err)
		}
		return ans["state"] != "waiting"
	})
	if ans["state"] != "done" {
		t.Fatalf("answer = %v", ans)
	}
	// Through JSON, as the site passes it on.
	data, _ := json.Marshal(ans["reply"])
	var r struct {
		Epk    plaudPhoneFixtureKey `json:"epk"`
		IV, CT string
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	d := base64.RawURLEncoding.DecodeString
	x, _ := d(r.Epk.X)
	y, _ := d(r.Epk.Y)
	pub, err := ecdh.P256().NewPublicKey(append(append([]byte{4}, x...), y...))
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := enc.ECDH(pub)
	key, _ := hkdf.Key(sha256.New, secret, nil, "fridolin voice reply v1", 32)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	iv, _ := d(r.IV)
	ct, _ := d(r.CT)
	pt, err := gcm.Open(nil, iv, ct, []byte(rid))
	if err != nil {
		t.Fatal(err)
	}
	return string(pt)
}

// A question asked by voice reaches the DM brain as a hand-off, with the
// instruction to answer for the ear; the spoken part of its answer comes
// back to the phone, sealed for it, and the rest goes to WhatsApp after
// the question, unless voice_quiet keeps it all on the phone. (The fake
// CLI answers with its prompt, whose example of a [SPOKEN] block stands
// in for the brain's.)
func TestPlaudVoiceQuestionIsAnsweredThroughABrain(t *testing.T) {
	t.Parallel()
	for _, quiet := range []bool{false, true} {
		runs := fakeRuns(t)
		extra := `, "phone_upload": true, "voice": true`
		if quiet {
			extra += `, "voice_quiet": true`
		}
		tb, _ := plaudBridge(t, extra)
		phone := newTestPhone(t)
		device := voicePair(t, tb, phone)
		enc, _ := ecdh.P256().GenerateKey(rand.Reader)
		rid := "q_voice_question_1"
		if ans, err := voiceQuestion(t, tb, phone, device, rid, "Was steht morgen an?", enc); err != nil || ans["state"] != "waiting" {
			t.Fatalf("quiet %v: ask = %v, %v", quiet, ans, err)
		}
		if answer := voiceAnswer(t, tb, phone, device, rid, enc); answer != "like this" {
			t.Errorf("quiet %v: the phone heard %q", quiet, answer)
		}
		if !quiet {
			// What the brain is told, as its run's prompt has it.
			r := runs()[len(runs())-1]
			i, j := strings.Index(r.Prompt, "[Voice question"), strings.Index(r.Prompt, "Was steht morgen an?")
			if i < 0 || j < i {
				t.Fatalf("the run's prompt:\n%s", r.Prompt)
			}
			assertGolden(t, "run/prompt-voice.golden", []byte(r.Prompt[i:j+len("Was steht morgen an?")]+"\n"))
		}
		out := tb.dev.Outbox()
		if quiet {
			if len(out) != 0 {
				t.Errorf("quiet: WhatsApp got %+v", out)
			}
			continue
		}
		testutil.WaitFor(t, 5*time.Second, "the WhatsApp answer", func() bool { return len(tb.dev.Outbox()) == 2 })
		out = tb.dev.Outbox() // newest first
		if out[1].Text != "🎙️ Was steht morgen an?" || out[0].JID != out[1].JID ||
			strings.Contains(out[0].Text, "[SPOKEN]") || !strings.Contains(out[0].Text, "Was steht morgen an?") {
			t.Errorf("WhatsApp got %.80q, then %.200q", out[1].Text, out[0].Text)
		}
	}
}
