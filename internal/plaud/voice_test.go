package plaud

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// voiceTestAsk records what the voice brain was asked.
type voiceTestAsk struct {
	mu    sync.Mutex
	asked []string
	quiet []bool
	jids  []string
	ans   func(string) string
	done  func(error)
}

// voiceTest is a Manager with voice mode on, a paired phone and a voice
// brain that records its questions.
func voiceTest(t *testing.T, extra string) (*Manager, *plaudServiceFixture, *voiceTestAsk, serviceTestPhone, *ecdh.PrivateKey) {
	t.Helper()
	f, _ := plaudService(t, `, "phone_upload": true, "voice": true`+extra)
	rec := &voiceTestAsk{}
	target := &Target{BrainID: "p", ReplyChat: func() string { return "" }, OtherOwner: func(string) string { return "" },
		Submit: func(_, _, _ string, _ []string, done func(error)) error { done(nil); return nil },
		Ask: func(jid, sender, text string, quiet bool, answer func(string) string, done func(error)) error {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if sender != voiceSender {
				t.Errorf("sender = %q", sender)
			}
			rec.asked, rec.quiet, rec.jids = append(rec.asked, text), append(rec.quiet, quiet), append(rec.jids, jid)
			rec.ans, rec.done = answer, done
			return nil
		}}
	opts := f.options
	opts.Target = func(id string) *Target {
		if id == "" || id == "p" {
			return target
		}
		return nil
	}
	p := New(opts)
	t.Cleanup(p.Stop)
	phone := newServiceTestPhone(t)
	if err := p.updateDevices(func(st *plaudDevices) error {
		st.Devices = append(st.Devices, plaudDevice{ID: "dev_1", Key: phone.jwk, Paired: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	enc, err := ecdh.P256().GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return p, f, rec, phone, enc
}

func voiceJWK(k *ecdh.PrivateKey) *plaudJWK {
	b := k.PublicKey().Bytes()
	e := base64.RawURLEncoding.EncodeToString
	return &plaudJWK{Kty: "EC", Crv: "P-256", X: e(b[1:33]), Y: e(b[33:])}
}

const voiceTestRid = "q_0123456789abcdef"

func voiceAsk(t *testing.T, p *Manager, phone serviceTestPhone, enc *ecdh.PrivateKey, iss, rid, question string) (map[string]any, error) {
	t.Helper()
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(question))
	tok := phone.sign(t, plaudClaims{Iss: iss, Aud: plaudVoiceAud, Iat: now, Exp: now + 300, Rid: rid,
		SHA256: base64.RawURLEncoding.EncodeToString(sum[:]), Enc: voiceJWK(enc)})
	return p.DeviceRequest("voice", tok, []byte(question))
}

func voiceFetch(t *testing.T, p *Manager, phone serviceTestPhone, iss, rid string) (map[string]any, error) {
	t.Helper()
	now := time.Now().Unix()
	return p.DeviceRequest("voice_reply", phone.sign(t, plaudClaims{Iss: iss, Aud: plaudVoiceReplyAud, Iat: now, Exp: now + 300, Rid: rid}), nil)
}

// openVoiceReply is what the phone does with a sealed answer.
func openVoiceReply(t *testing.T, k *ecdh.PrivateKey, rid string, reply map[string]any) string {
	t.Helper()
	s, err := openVoiceReplyErr(k, rid, reply)
	if err != nil {
		t.Fatalf("opening the answer: %v", err)
	}
	return s
}

func openVoiceReplyErr(k *ecdh.PrivateKey, rid string, reply map[string]any) (string, error) {
	d := base64.RawURLEncoding.DecodeString
	epk := reply["epk"].(plaudJWK)
	x, _ := d(epk.X)
	y, _ := d(epk.Y)
	pub, err := ecdh.P256().NewPublicKey(append(append([]byte{4}, x...), y...))
	if err != nil {
		return "", err
	}
	secret, err := k.ECDH(pub)
	if err != nil {
		return "", err
	}
	key, _ := hkdf.Key(sha256.New, secret, nil, voiceSealInfo, 32)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	iv, _ := d(reply["iv"].(string))
	ct, _ := d(reply["ct"].(string))
	pt, err := gcm.Open(nil, iv, ct, []byte(rid))
	return string(pt), err
}

func TestVoiceQuestionIsAnsweredToThePhoneThatAsked(t *testing.T) {
	p, f, rec, phone, enc := voiceTest(t, "")
	got, err := voiceAsk(t, p, phone, enc, "dev_1", voiceTestRid, "  Was steht morgen an?\n")
	if err != nil || got["state"] != voiceWaiting {
		t.Fatalf("ask = %v, %v", got, err)
	}
	if len(rec.asked) != 1 || !strings.HasPrefix(rec.asked[0], voicePrompt) || !strings.HasSuffix(rec.asked[0], "\n\nWas steht morgen an?") {
		t.Fatalf("the brain was asked %q", rec.asked)
	}
	// Into the owner's private chat, with the question before the answer.
	if rec.jids[0] != "15551234567@s.whatsapp.net" || rec.quiet[0] {
		t.Errorf("asked in %q, quiet %v", rec.jids[0], rec.quiet[0])
	}
	if out := f.dev.Outbox(); len(out) != 1 || out[0].Text != "🎙️ Was steht morgen an?" || out[0].JID != rec.jids[0] {
		t.Errorf("chat got %+v", out)
	}

	// The same question again (its answer got lost) asks nothing new.
	if got, err := voiceAsk(t, p, phone, enc, "dev_1", voiceTestRid, "Was steht morgen an?"); err != nil || got["state"] != voiceWaiting || len(rec.asked) != 1 {
		t.Fatalf("again: %v, %v, asked %d times", got, err, len(rec.asked))
	}

	// A fetch waits for the answer.
	type result struct {
		m   map[string]any
		err error
	}
	fetched := make(chan result, 1)
	go func() {
		m, err := voiceFetch(t, p, phone, "dev_1", voiceTestRid)
		fetched <- result{m, err}
	}()
	time.Sleep(50 * time.Millisecond)
	if chat := rec.ans("Morgen um neun: Zahnarzt."); chat != "Morgen um neun: Zahnarzt." {
		t.Errorf("the chat gets %q", chat)
	}
	rec.done(nil)
	r := <-fetched
	if r.err != nil || r.m["state"] != voiceDone {
		t.Fatalf("fetch = %v, %v", r.m, r.err)
	}
	reply := r.m["reply"].(map[string]any)
	if s := openVoiceReply(t, enc, voiceTestRid, reply); s != "Morgen um neun: Zahnarzt." {
		t.Errorf("answer = %q", s)
	}
	// Bound to its question: under another id it does not open.
	if _, err := openVoiceReplyErr(enc, "q_another_question_id", reply); err == nil {
		t.Error("the answer opened as another question's")
	}
	if strings.Contains(reply["ct"].(string), "Zahnarzt") {
		t.Error("the answer went out in clear")
	}

	// Another paired phone does not learn of it.
	other := newServiceTestPhone(t)
	if err := p.updateDevices(func(st *plaudDevices) error {
		st.Devices = append(st.Devices, plaudDevice{ID: "dev_2", Key: other.jwk, Paired: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if m, err := voiceFetch(t, p, other, "dev_2", voiceTestRid); err != nil || m["state"] != voiceUnknown || m["reply"] != nil {
		t.Errorf("another phone's fetch = %v, %v", m, err)
	}
	// Nor does a key that claims to be this phone.
	if _, err := voiceFetch(t, p, other, "dev_1", voiceTestRid); err == nil {
		t.Error("a fetch signed by another key was answered")
	}
}

func TestVoiceAnswerWaitsAtMostVoiceReplyWait(t *testing.T) {
	p, _, _, phone, enc := voiceTest(t, "")
	if _, err := voiceAsk(t, p, phone, enc, "dev_1", voiceTestRid, "Wie spät ist es?"); err != nil {
		t.Fatal(err)
	}
	// Stopping the manager ends a wait as the timeout would.
	go func() { time.Sleep(50 * time.Millisecond); p.Stop() }()
	if m, err := voiceFetch(t, p, phone, "dev_1", voiceTestRid); err != nil || m["state"] != voiceWaiting {
		t.Fatalf("fetch = %v, %v", m, err)
	}
}

func TestVoiceQuietKeepsTheAnswerOutOfTheChat(t *testing.T) {
	p, f, rec, phone, enc := voiceTest(t, `, "voice_quiet": true`)
	if _, err := voiceAsk(t, p, phone, enc, "dev_1", voiceTestRid, "Hi"); err != nil {
		t.Fatal(err)
	}
	if !rec.quiet[0] || len(f.dev.Outbox()) != 0 {
		t.Errorf("quiet = %v, chat got %+v", rec.quiet[0], f.dev.Outbox())
	}
}

func TestVoiceFailedRunSaysSoWithoutItsOutput(t *testing.T) {
	p, _, rec, phone, enc := voiceTest(t, "")
	if _, err := voiceAsk(t, p, phone, enc, "dev_1", voiceTestRid, "Hi"); err != nil {
		t.Fatal(err)
	}
	rec.done(errors.New("claude: secret stderr"))
	m, err := voiceFetch(t, p, phone, "dev_1", voiceTestRid)
	if err != nil || m["state"] != voiceFailed || strings.Contains(m["error"].(string), "secret") || m["reply"] != nil {
		t.Fatalf("fetch = %v, %v", m, err)
	}
	// What ended stays ended: a late answer changes nothing.
	rec.ans("late")
	if m, _ := voiceFetch(t, p, phone, "dev_1", voiceTestRid); m["state"] != voiceFailed {
		t.Errorf("after a late answer: %v", m)
	}
}

func TestVoiceQuestionsAreRefused(t *testing.T) {
	p, _, rec, phone, enc := voiceTest(t, "")
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte("Hi"))
	sha := base64.RawURLEncoding.EncodeToString(sum[:])
	good := plaudClaims{Iss: "dev_1", Aud: plaudVoiceAud, Iat: now, Exp: now + 300, Rid: voiceTestRid, SHA256: sha, Enc: voiceJWK(enc)}
	for what, c := range map[string]func(c plaudClaims) (plaudClaims, string){
		"no id":            func(c plaudClaims) (plaudClaims, string) { c.Rid = ""; return c, "Hi" },
		"a bad id":         func(c plaudClaims) (plaudClaims, string) { c.Rid = "../../x/aaaaaaaaaaaaaaaa"; return c, "Hi" },
		"another text":     func(c plaudClaims) (plaudClaims, string) { return c, "Ho" },
		"no answer key":    func(c plaudClaims) (plaudClaims, string) { c.Enc = nil; return c, "Hi" },
		"a transcript aud": func(c plaudClaims) (plaudClaims, string) { c.Aud = plaudTranscriptAud; return c, "Hi" },
		"an unpaired phone": func(c plaudClaims) (plaudClaims, string) {
			c.Iss = "dev_9"
			return c, "Hi"
		},
		"an off-curve answer key": func(c plaudClaims) (plaudClaims, string) {
			k := *c.Enc
			k.Y = k.X
			c.Enc = &k
			return c, "Hi"
		},
		"an empty question": func(c plaudClaims) (plaudClaims, string) {
			s := sha256.Sum256([]byte("  "))
			c.SHA256 = base64.RawURLEncoding.EncodeToString(s[:])
			return c, "  "
		},
	} {
		claims, body := c(good)
		if _, err := p.DeviceRequest("voice", phone.sign(t, claims), []byte(body)); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	if len(rec.asked) != 0 {
		t.Errorf("the brain was asked %q", rec.asked)
	}

	off, f, rec2, phone2, enc2 := voiceTest(t, "")
	if err := f.updateSettings(func(s *Snapshot) error { s.Plaud.Voice = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := voiceAsk(t, off, phone2, enc2, "dev_1", voiceTestRid, "Hi"); !errors.Is(err, errVoiceOff) {
		t.Errorf("voice off: %v", err)
	}
	if _, err := voiceFetch(t, off, phone2, "dev_1", voiceTestRid); !errors.Is(err, errVoiceOff) {
		t.Errorf("voice off, fetch: %v", err)
	}
	if len(rec2.asked) != 0 {
		t.Error("asked with voice off")
	}
}

// The vector the app's VoiceCryptoTest opens (plaud-android): a fixed
// phone key, ephemeral key and nonce.
const (
	voiceVectorPhoneKey = "c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721"
	voiceVectorEphKey   = "1f8a7e7ad06b4e0ea8a0d0ea43ba2e8b8a3b8f0f0e5e8a3e1b0d1a9c2b3c4d5e"
	voiceVectorNonce    = "00112233445566778899aabb"
	voiceVectorRid      = "q_0123456789abcdef"
	voiceVectorText     = "Morgen um neun: Zahnarzt. Tomorrow at nine: dentist."
	voiceVectorEpkX     = "6sbOU2oH2ezKauk0mRkYD0CP6y6WYs4p1lDM1M7w9as"
	voiceVectorEpkY     = "7Xc3SI93jdrsFoTSCAxC4tcIBgH0CS5FiaP0AOrdW9c"
	voiceVectorCT       = "nG2tTagAHfiwF61tyW2ejORNK8zm0wyhGK1M6dz0FP-YiLDZuyobJqDiq6GXqX82iCFxXKupFQUWwEXYVTT1olRb9WM"
)

func TestVoiceSealVector(t *testing.T) {
	h := func(s string) []byte { b, _ := hex.DecodeString(s); return b }
	phone, err := ecdh.P256().NewPrivateKey(h(voiceVectorPhoneKey))
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.P256().NewPrivateKey(h(voiceVectorEphKey))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := sealVoiceReply(voiceJWK(phone), voiceVectorRid, []byte(voiceVectorText), eph, h(voiceVectorNonce))
	if err != nil {
		t.Fatal(err)
	}
	epk := reply["epk"].(plaudJWK)
	if epk.X != voiceVectorEpkX || epk.Y != voiceVectorEpkY {
		t.Errorf("epk = %s, %s", epk.X, epk.Y)
	}
	if reply["ct"] != voiceVectorCT {
		t.Errorf("ct = %s, want %s", reply["ct"], voiceVectorCT)
	}
	if s := openVoiceReply(t, phone, voiceVectorRid, reply); s != voiceVectorText {
		t.Errorf("opened %q", s)
	}
}

func TestVoiceSpokenBlockIsReadAndTheRestGoesToTheChat(t *testing.T) {
	p, _, rec, phone, enc := voiceTest(t, "")
	if _, err := voiceAsk(t, p, phone, enc, "dev_1", voiceTestRid, "Was steht diese Woche an?"); err != nil {
		t.Fatal(err)
	}
	chat := rec.ans("[SPOKEN]Drei Termine, der wichtigste: Zahnarzt am Dienstag um halb zehn. Details in WhatsApp.[/SPOKEN]\n\n- Di 09:30 Zahnarzt\n- Mi 14:00 Call\n- Fr 18:00 Essen")
	if strings.Contains(chat, "SPOKEN") || !strings.HasPrefix(chat, "- Di 09:30 Zahnarzt") {
		t.Errorf("the chat gets %q", chat)
	}
	m, err := voiceFetch(t, p, phone, "dev_1", voiceTestRid)
	if err != nil {
		t.Fatal(err)
	}
	if s := openVoiceReply(t, enc, voiceTestRid, m["reply"].(map[string]any)); s != "Drei Termine, der wichtigste: Zahnarzt am Dienstag um halb zehn. Details in WhatsApp." {
		t.Errorf("the phone hears %q", s)
	}
}

func TestSpokenAnswer(t *testing.T) {
	long := strings.Repeat("This sentence has exactly seven words here. ", 12)
	for _, c := range []struct{ in, spoken, chat string }{
		{"Kurz und gut.", "Kurz und gut.", "Kurz und gut."},
		{"[SPOKEN] Only this. [/SPOKEN]", "Only this.", "Only this."},
		{"Intro.\n[SPOKEN]Said.[/SPOKEN]\nDetails.", "Said.", "Intro.\n\nDetails."},
		// Long without a block: whole sentences up to 60 words.
		{long, strings.TrimSpace(strings.Repeat("This sentence has exactly seven words here. ", 8)), long},
	} {
		spoken, chat := spokenAnswer(c.in)
		if spoken != c.spoken || chat != c.chat {
			t.Errorf("spokenAnswer(%.40q) = %q, %q; want %q, %q", c.in, spoken, chat, c.spoken, c.chat)
		}
	}
	// One endless sentence is cut at 60 words.
	words := strings.TrimSpace(strings.Repeat("word ", 100))
	if s, _ := spokenAnswer(words); len(strings.Fields(s)) != maxSpokenWords || !strings.HasSuffix(s, "…") {
		t.Errorf("an endless sentence: %q", s)
	}
	// A full stop inside a number or a time is no sentence's end.
	if s := shortenSpoken("It costs 3.50 francs. "+long, 10); s != "It costs 3.50 francs." {
		t.Errorf("shortened to %q", s)
	}
}
