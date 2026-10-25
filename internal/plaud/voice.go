package plaud

// Voice mode of the Plaud Sync app (docs/PLAUD.md, "Voice mode"): the
// owner asks by voice, and the phone reads the answer aloud.
//
// The app sends a question it transcribed through the site, like a
// transcript, signed with its keystore key (aud fridolin:voice). The JWT
// names the question's id, which the app makes, its SHA-256, and a second
// public key of the phone's keystore, one for key agreement only. The
// bridge hands the question to plaud.voice_brain, in that brain's private
// chat, where the answer goes as usual unless plaud.voice_quiet. The
// question itself never goes to the chat: the brain files it with its
// notes (voicePrompt). The
// answer is encrypted to the phone's key (sealVoiceReply) and waits here,
// in memory, until the phone fetches it with a JWT naming the question
// (aud fridolin:voice-reply). The site, which runs module code, carries
// only ciphertext: nobody without the phone's keystore reads the answer.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	plaudVoiceAud      = "fridolin:voice"
	plaudVoiceReplyAud = "fridolin:voice-reply"
	// VoiceQuestionMax bounds a voice question.
	VoiceQuestionMax = 16 << 10
	// VoiceReplyWait is how long a fetch of an answer waits for it; the
	// site waits a little longer for the bridge (ingest.ts, ANSWER_MS).
	VoiceReplyWait = 15 * time.Second
	// voiceKeep is how long a question and its answer are kept.
	voiceKeep = 30 * time.Minute
	// maxVoiceWaiting bounds the questions kept at once.
	maxVoiceWaiting = 64
	// voiceSealInfo is HKDF's info for the key an answer is encrypted with.
	voiceSealInfo = "fridolin voice reply v1"
	// voiceSender is who a voice question comes from, for the brain.
	voiceSender = "voice"
)

// The states of a voice question.
const (
	voiceWaiting = "waiting"
	voiceDone    = "done"
	voiceFailed  = "failed"
	voiceUnknown = "unknown"
)

// voicePrompt goes before a voice question: the brain answers it as it
// would a message, but for the ear, with a spoken version in a
// [SPOKEN] block when the full answer is longer (spokenAnswer).
const voicePrompt = `[Voice question from the owner's phone (Plaud Sync voice mode). Your answer is read aloud there.
Answer for the ear: one to three short spoken sentences, the most important point first. No lists, tables, links, code, markdown or emoji. Write numbers, dates and times the way they are said ("half past nine on Tuesday", not "09:30, Tue"). Answer in the language of the question.
If the answer needs more detail than that, put the spoken sentences in a block of their own, [SPOKEN]like this[/SPOKEN], then write the details as usual after it, and say in the spoken part that the details are in WhatsApp. Only the block is read aloud; the details go to this chat.
The question is not shown in this chat. Keep it with your notes, as a voice question with today's date, the way skills/plaud.md files recordings (without that skill, wherever your notes go), and do not mention that in your answer.]`

// maxSpokenWords caps what the phone reads of an answer that came back
// long anyway: it is cut at the last sentence that fits.
const maxSpokenWords = 60

var spokenRE = regexp.MustCompile(`(?s)\[SPOKEN\](.*?)\[/SPOKEN\]`)

// spokenAnswer splits a brain's answer to a voice question into what the
// phone reads aloud and what the chat gets: the [SPOKEN] block and the
// rest, or without one the whole answer for both. What is read is at most
// maxSpokenWords, cut at a sentence's end.
func spokenAnswer(text string) (spoken, chat string) {
	chat = text
	spoken = text
	if m := spokenRE.FindStringSubmatchIndex(text); m != nil {
		spoken = text[m[2]:m[3]]
		chat = strings.TrimSpace(strings.TrimSpace(text[:m[0]]) + "\n\n" + strings.TrimSpace(text[m[1]:]))
		if chat == "" {
			chat = strings.TrimSpace(spoken)
		}
	}
	return shortenSpoken(strings.TrimSpace(spoken), maxSpokenWords), chat
}

// shortenSpoken is text cut to whole sentences of at most max words
// together; a first sentence longer than that is cut at max words.
func shortenSpoken(text string, max int) string {
	if len(strings.Fields(text)) <= max {
		return text
	}
	var out strings.Builder
	words := 0
	start := 0
	for i, r := range text {
		end := i + utf8.RuneLen(r)
		if !strings.ContainsRune(".!?…", r) || (end < len(text) && !unicode.IsSpace(rune(text[end]))) {
			continue
		}
		sentence := text[start:end]
		n := len(strings.Fields(sentence))
		if words+n > max {
			break
		}
		out.WriteString(sentence)
		words += n
		start = end
	}
	if words == 0 {
		return strings.Join(strings.Fields(text)[:max], " ") + "…"
	}
	return strings.TrimSpace(out.String())
}

var errVoiceOff = errors.New("voice mode is off (plaud.voice)")

var voiceIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// voiceQuestion is a question and, once there, its sealed answer.
type voiceQuestion struct {
	state string
	asked time.Time
	key   plaudJWK
	reply map[string]any
	err   string
}

// voiceBook is the questions of this start, by phone and id.
type voiceBook struct {
	mu sync.Mutex
	// changed is closed and replaced whenever a question's state changes,
	// so that the fetches waiting for one look again.
	changed chan struct{}
	byID    map[string]*voiceQuestion
}

func voiceKey(device, rid string) string { return device + "/" + rid }

// lookup returns the question and a channel that is closed at its next
// change; it drops what is older than voiceKeep first.
func (b *voiceBook) lookup(key string, now time.Time) (*voiceQuestion, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	for k, q := range b.byID {
		if now.Sub(q.asked) > voiceKeep {
			delete(b.byID, k)
		}
	}
	return b.byID[key], b.changed
}

func (b *voiceBook) init() {
	if b.byID == nil {
		b.byID = map[string]*voiceQuestion{}
		b.changed = make(chan struct{})
	}
}

// add keeps q under key, unless there are too many.
func (b *voiceBook) add(key string, q *voiceQuestion) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if len(b.byID) >= maxVoiceWaiting {
		return errors.New("too many voice questions at once; try again in a few minutes")
	}
	b.byID[key] = q
	return nil
}

// end settles the question under key, unless it was settled already,
// with fn, and wakes the fetches waiting for it.
func (b *voiceBook) end(key string, fn func(q *voiceQuestion)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	q := b.byID[key]
	if q == nil || q.state != voiceWaiting {
		return
	}
	fn(q)
	close(b.changed)
	b.changed = make(chan struct{})
}

// remove forgets the question under key.
func (b *voiceBook) remove(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.byID, key)
}

// answer is what a phone learns of q.
func (q *voiceQuestion) answer() map[string]any {
	out := map[string]any{"state": q.state}
	if q.reply != nil {
		out["reply"] = q.reply
	}
	if q.err != "" {
		out["error"] = q.err
	}
	return out
}

// voiceDevice checks that a paired phone signed assertion for aud and
// returns its claims.
func (p *Manager) voiceDevice(assertion, aud string, now time.Time) (plaudClaims, error) {
	cfg := p.cfg()
	if !cfg.Plaud.PhoneUpload {
		return plaudClaims{}, errPhoneUploadOff
	}
	if !cfg.Plaud.Voice {
		return plaudClaims{}, errVoiceOff
	}
	c, err := parsePlaudJWT(assertion)
	if err != nil {
		return c, err
	}
	st, err := p.loadDevices()
	if err != nil {
		return c, err
	}
	i := -1
	for j, d := range st.Devices {
		if d.ID == c.Iss {
			i = j
		}
	}
	if c.Iss == "" || i < 0 {
		return c, errors.New("this phone is not paired, or was unpaired")
	}
	if c, err = verifyPlaudJWT(assertion, st.Devices[i].Key, aud, now); err != nil {
		return c, err
	}
	if !voiceIDRE.MatchString(c.Rid) {
		return c, errors.New("the question has no valid id")
	}
	return c, nil
}

// takeVoice hands a phone's voice question to the voice brain. The same
// question again (a retry whose answer got lost) asks nothing new.
func (p *Manager) takeVoice(assertion string, body []byte, now time.Time) (map[string]any, error) {
	c, err := p.voiceDevice(assertion, plaudVoiceAud, now)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if c.SHA256 != base64.RawURLEncoding.EncodeToString(sum[:]) {
		return nil, errors.New("the signature is for another question")
	}
	if c.Enc == nil {
		return nil, errors.New("the question has no key to encrypt the answer to")
	}
	if _, err := c.Enc.publicKey(); err != nil {
		return nil, fmt.Errorf("the answer's key: %w", err)
	}
	question := strings.TrimSpace(string(body))
	switch {
	case len(body) > VoiceQuestionMax:
		return nil, errors.New("the question is too long")
	case !utf8.ValidString(question):
		return nil, errors.New("the question is not text")
	case question == "":
		return nil, errors.New("the question is empty")
	}
	key := voiceKey(c.Iss, c.Rid)
	if q, _ := p.voice.lookup(key, now); q != nil {
		return map[string]any{"state": q.state}, nil
	}
	cfg := p.cfg()
	br := p.options.Target(cfg.Plaud.VoiceBrain)
	if br == nil {
		if id := cfg.Plaud.VoiceBrain; id != "" {
			return nil, fmt.Errorf("brain %s, picked for voice questions, was removed or is not approved", id)
		}
		return nil, errors.New("no brain to answer voice questions")
	}
	if br.Ask == nil {
		return nil, fmt.Errorf("brain %s takes no voice questions", br.ID())
	}
	jid := p.summaryChat(cfg, br)
	if jid == "" {
		return nil, fmt.Errorf("no private chat for brain %s's answers: add your number under Chats → Allowed numbers", br.ID())
	}
	if err := p.voice.add(key, &voiceQuestion{state: voiceWaiting, asked: now, key: *c.Enc}); err != nil {
		return nil, err
	}
	quiet := cfg.Plaud.VoiceQuiet
	rid := c.Rid
	answer := func(text string) string {
		spoken, chat := spokenAnswer(text)
		reply, err := sealVoiceReply(c.Enc, rid, []byte(spoken), nil, nil)
		p.voice.end(key, func(q *voiceQuestion) {
			if err != nil {
				q.state, q.err = voiceFailed, "the answer could not be encrypted for this phone"
				return
			}
			q.state, q.reply = voiceDone, reply
		})
		return chat
	}
	done := func(err error) {
		p.voice.end(key, func(q *voiceQuestion) {
			switch {
			case err == nil:
				// Answered with nothing to say: a question it asked in
				// the chat said it all.
				q.state = voiceDone
			case p.options.RunInterrupted != nil && p.options.RunInterrupted(err):
				q.state, q.err = voiceFailed, "the question was stopped before it was answered"
			default:
				// The error in full goes to the log only: it can hold the
				// CLI's output.
				q.state, q.err = voiceFailed, "the brain's run failed; the dashboard says why"
			}
		})
	}
	if err := br.Ask(jid, voiceSender, voicePrompt+"\n\n"+question, quiet, answer, done); err != nil {
		p.voice.remove(key)
		return nil, fmt.Errorf("brain %s did not take the question: %w", br.ID(), err)
	}
	log.Printf("plaud: phone %s asked brain %s by voice", c.Iss, br.ID())
	return map[string]any{"state": voiceWaiting}, nil
}

// voiceReply is the answer to a phone's question once there: it waits up
// to VoiceReplyWait while the question waits for its answer.
func (p *Manager) voiceReply(assertion string, now time.Time) (map[string]any, error) {
	c, err := p.voiceDevice(assertion, plaudVoiceReplyAud, now)
	if err != nil {
		return nil, err
	}
	key := voiceKey(c.Iss, c.Rid)
	timer := time.NewTimer(VoiceReplyWait)
	defer timer.Stop()
	for {
		q, changed := p.voice.lookup(key, time.Now())
		if q == nil {
			return map[string]any{"state": voiceUnknown}, nil
		}
		p.voice.mu.Lock()
		out, state := q.answer(), q.state
		p.voice.mu.Unlock()
		if state != voiceWaiting {
			return out, nil
		}
		select {
		case <-changed:
		case <-timer.C:
			return out, nil
		case <-p.lifetime().Done():
			return out, nil
		}
	}
}

// sealVoiceReply encrypts text for the phone that holds the private half
// of to: ECDH (P-256) with a fresh key, HKDF-SHA256 of the shared secret
// (no salt, voiceSealInfo) to an AES-256-GCM key, with rid as associated
// data. eph and nonce are for tests; nil makes fresh ones.
func sealVoiceReply(to *plaudJWK, rid string, text []byte, eph *ecdh.PrivateKey, nonce []byte) (map[string]any, error) {
	pub, err := to.publicKey()
	if err != nil {
		return nil, err
	}
	peer, err := pub.ECDH()
	if err != nil {
		return nil, err
	}
	if eph == nil {
		if eph, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	secret, err := eph.ECDH(peer)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, secret, nil, voiceSealInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if nonce == nil {
		nonce = make([]byte, gcm.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
	}
	pt := eph.PublicKey().Bytes() // 0x04 ‖ x ‖ y
	b64 := base64.RawURLEncoding.EncodeToString
	return map[string]any{
		"alg": "ECDH-ES+HKDF-SHA256+A256GCM",
		"epk": plaudJWK{Kty: "EC", Crv: "P-256", X: b64(pt[1:33]), Y: b64(pt[33:])},
		"iv":  b64(nonce),
		"ct":  b64(gcm.Seal(nil, nonce, text, []byte(rid))),
	}, nil
}
