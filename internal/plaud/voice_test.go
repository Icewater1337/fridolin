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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// voiceTestAsk records what the voice brain was asked.
type voiceTestAsk struct {
	mu    sync.Mutex
	asked []string
	media [][]string
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
		Ask: func(jid, sender, text string, media []string, quiet bool, answer func(string) string, done func(error)) error {
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if sender != voiceSender {
				t.Errorf("sender = %q", sender)
			}
			rec.asked, rec.media, rec.quiet, rec.jids = append(rec.asked, text), append(rec.media, media), append(rec.quiet, quiet), append(rec.jids, jid)
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
	// Into the owner's private chat, where only the answer goes: the question never does.
	if rec.jids[0] != "15551234567@s.whatsapp.net" || rec.quiet[0] {
		t.Errorf("asked in %q, quiet %v", rec.jids[0], rec.quiet[0])
	}
	if out := f.dev.Outbox(); len(out) != 0 {
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

const voiceTestPinName = "2026-10-18_10-00-00"

// voicePin sends transcript as the app sends a recording from the pin in
// voice mode: through the voice route, with src "pin" and its start time.
func voicePin(t *testing.T, p *Manager, phone serviceTestPhone, enc *ecdh.PrivateKey, rid, name, transcript string) (map[string]any, error) {
	t.Helper()
	now := time.Now().Unix()
	sum := sha256.Sum256([]byte(transcript))
	tok := phone.sign(t, plaudClaims{Iss: "dev_1", Aud: plaudVoiceAud, Iat: now, Exp: now + 300, Rid: rid,
		SHA256: base64.RawURLEncoding.EncodeToString(sum[:]), Enc: voiceJWK(enc), Src: voiceSrcPin, Name: name})
	return p.DeviceRequest("voice", tok, []byte(transcript))
}

// A recording from the pin goes to the voice brain as a recording does,
// with its transcript whole in a file of the brain's inbox, and the brain
// decides whether it was spoken to; its answer comes back to the phone as
// a question's does.
func TestVoicePinRecordingReachesTheVoiceBrainAsARecording(t *testing.T) {
	p, f, rec, phone, enc := voiceTest(t, "")
	transcript := "[00:00 - 00:04] Speaker 1: Erinnere mich morgen um neun an den Zahnarzt.\n[00:04 - 00:07] Speaker 1: Danke."
	if got, err := voicePin(t, p, phone, enc, voiceTestRid, voiceTestPinName, transcript+"\n"); err != nil || got["state"] != voiceWaiting {
		t.Fatalf("pin = %v, %v", got, err)
	}
	if len(rec.asked) != 1 {
		t.Fatalf("asked %d times", len(rec.asked))
	}
	text := rec.asked[0]
	want := voicePinPrompt + "\n\n" + `[Plaud] New recording "Pin recording" (2026-10-18 10:00, 00:07).`
	if !strings.HasPrefix(text, want) || !strings.Contains(text, defaultPlaudMessage) || !strings.HasSuffix(text, "\n\n=== Transcript ===\n"+transcript) {
		t.Fatalf("the brain was told %q", text)
	}
	if strings.Contains(text, voicePrompt) {
		t.Error("a recording got the prompt of a question")
	}
	// The transcript, speaker labels and all, as a recording's file.
	if len(rec.media[0]) != 1 {
		t.Fatalf("media = %q", rec.media[0])
	}
	file := rec.media[0][0]
	if filepath.Base(filepath.Dir(file)) != "plaud" || filepath.Base(file) != "2026-10-18 - Pin recording - phone_"+voiceTestPinName+".txt" {
		t.Errorf("filed as %s", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "Plaud recording: Pin recording\nRecorded: 2026-10-18 10:00\nDuration: 00:07\nPlaud ID: phone_"+voiceTestPinName+"\n") ||
		!strings.HasSuffix(string(data), "=== Transcript ===\n"+transcript+"\n") {
		t.Errorf("the file says %q", data)
	}
	if rec.jids[0] != "15551234567@s.whatsapp.net" || len(f.dev.Outbox()) != 0 {
		t.Errorf("asked in %q, chat got %+v", rec.jids[0], f.dev.Outbox())
	}
	// It counts as the phone's last transcript.
	if st, err := p.loadDevices(); err != nil || st.Devices[0].LastUpload.IsZero() {
		t.Errorf("no last upload noted: %v", err)
	}

	// The answer: the spoken sentence to the phone, the summary to the chat.
	chat := rec.ans("[SPOKEN]Abgelegt: eine Erinnerung an den Zahnarzt.[/SPOKEN]\n\nErinnerung: morgen 09:00 Zahnarzt.")
	if chat != "Erinnerung: morgen 09:00 Zahnarzt." {
		t.Errorf("the chat gets %q", chat)
	}
	m, err := voiceFetch(t, p, phone, "dev_1", voiceTestRid)
	if err != nil || m["state"] != voiceDone {
		t.Fatalf("fetch = %v, %v", m, err)
	}
	if s := openVoiceReply(t, enc, voiceTestRid, m["reply"].(map[string]any)); s != "Abgelegt: eine Erinnerung an den Zahnarzt." {
		t.Errorf("the phone hears %q", s)
	}

	// Sent again (its answer got lost): nothing new.
	if got, err := voicePin(t, p, phone, enc, voiceTestRid, voiceTestPinName, transcript+"\n"); err != nil || got["state"] != voiceDone || len(rec.asked) != 1 {
		t.Errorf("again: %v, %v, asked %d times", got, err, len(rec.asked))
	}
}

// A recording may be as long as a transcript; past the length of a
// question it is in the file only, not in the prompt. Plaud's instruction
// for recordings (plaud.message) is the one the brain gets.
func TestVoicePinRecordingMayBeLong(t *testing.T) {
	p, _, rec, phone, enc := voiceTest(t, `, "message": "File it under meetings."`)
	line := "[00:00 - 00:10] Speaker 2: " + strings.Repeat("Wir besprechen das Budget. ", 10) + "\n"
	long := strings.TrimSpace(strings.Repeat(line, VoiceQuestionMax/len(line)+10))
	if len(long) <= VoiceQuestionMax {
		t.Fatalf("only %d bytes", len(long))
	}
	if _, err := voicePin(t, p, phone, enc, voiceTestRid, voiceTestPinName, long); err != nil {
		t.Fatal(err)
	}
	text := rec.asked[0]
	if !strings.HasSuffix(text, "File it under meetings.") || strings.Contains(text, "Wir besprechen") || strings.Contains(text, defaultPlaudMessage) {
		t.Errorf("the brain was told %.300q…", text)
	}
	if data, err := os.ReadFile(rec.media[0][0]); err != nil || !strings.Contains(string(data), long) {
		t.Errorf("the file: %v", err)
	}

	// A question of that length is too long.
	if _, err := voiceAsk(t, p, phone, enc, "dev_1", "q_another_question_1", long); err == nil || !strings.Contains(err.Error(), "question is too long") {
		t.Errorf("a long question: %v", err)
	}
	// A recording longer than a transcript is too.
	tooLong := strings.Repeat("x", TranscriptMax+1)
	if _, err := voicePin(t, p, phone, enc, "q_another_question_2", voiceTestPinName, tooLong); err == nil || !strings.Contains(err.Error(), "recording is too long") {
		t.Errorf("a recording past TranscriptMax: %v", err)
	}
	if len(rec.asked) != 1 {
		t.Errorf("asked %d times", len(rec.asked))
	}
}

func TestVoicePinRecordingsAreRefused(t *testing.T) {
	p, _, rec, phone, enc := voiceTest(t, "")
	text := "[00:00 - 00:02] Speaker 1: Hallo."
	sum := sha256.Sum256([]byte(text))
	now := time.Now().Unix()
	good := plaudClaims{Iss: "dev_1", Aud: plaudVoiceAud, Iat: now, Exp: now + 300, Rid: voiceTestRid,
		SHA256: base64.RawURLEncoding.EncodeToString(sum[:]), Enc: voiceJWK(enc), Src: voiceSrcPin, Name: voiceTestPinName}
	for what, change := range map[string]func(c *plaudClaims){
		"no name":               func(c *plaudClaims) { c.Name = "" },
		"a name that is a path": func(c *plaudClaims) { c.Name = "../../2026-10-18_10-00-00" },
		"another kind":          func(c *plaudClaims) { c.Src = "watch" },
	} {
		c := good
		change(&c)
		if _, err := p.DeviceRequest("voice", phone.sign(t, c), []byte(text)); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	if len(rec.asked) != 0 {
		t.Errorf("the brain was asked %q", rec.asked)
	}
}

// A full voice book is a temporary failure the site lets the phone retry;
// a refusal like an empty question is not.
func TestVoiceTooManyQuestionsIsTemporary(t *testing.T) {
	var b voiceBook
	var err error
	for i := 0; i <= maxVoiceWaiting && err == nil; i++ {
		err = b.add(voiceKey("dev_1", strings.Repeat("a", 15)+string(rune('A'+i%26))+string(rune('a'+i/26))), &voiceQuestion{})
	}
	var tmp interface{ Temporary() bool }
	if !errors.As(err, &tmp) || !tmp.Temporary() {
		t.Fatalf("full book: %v, want a temporary error", err)
	}
	if errors.As(errors.New("the question is empty"), &tmp) {
		t.Error("a plain error is temporary")
	}
}
