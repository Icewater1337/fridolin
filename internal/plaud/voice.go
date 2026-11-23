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
// notes (voicePrompt).
//
// A recording from the pin comes the same way, as Plaud transcribed it,
// with src "pin" and its start time as its name in the JWT: the brain
// gets its transcript as it gets a recording, in a file in its inbox, and
// decides whether it was spoken to (a question, a task) or is a recording
// to file (a meeting, a dictation), and answers for the ear either way
// (voicePinPrompt). The
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
	// VoiceQuestionMax bounds a voice question; a recording from the pin
	// may be as long as a transcript (TranscriptMax), and is in the
	// prompt only while it fits in this.
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
	// voiceSrcPin is the src claim of a recording from the pin.
	voiceSrcPin = "pin"
	// voicePinName is what a recording from the pin is called, as
	// fetchPhone calls an upload "Phone recording".
	voicePinName = "Pin recording"
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

// voicePinPrompt goes before a recording from the pin, the
// recordingMessage of it and, while it is short, its transcript: the brain
// decides whether it was spoken to or is a recording to file, and answers
// for the ear either way, as voicePrompt says.
const voicePinPrompt = `[A recording from the owner's Plaud pin (Plaud Sync voice mode), transcribed by Plaud with speaker labels and timestamps. Your answer is read aloud on the owner's phone.
First decide what it is. When the owner speaks to you in it (a question, a task, a reminder, something to note or to add to the backlog), do it as you would a message from the owner, and answer that. Otherwise (a meeting, a conversation, a dictation, a note to self) it is a recording: handle it as the instruction after "[Plaud] New recording" below says, with the summary and the action items for this chat, and say in one spoken sentence what you filed.
Answer for the ear: one to three short spoken sentences, the most important point first. No lists, tables, links, code, markdown or emoji. Write numbers, dates and times the way they are said ("half past nine on Tuesday", not "09:30, Tue"). Answer in the language of the recording.
If the answer needs more detail than that, put the spoken sentences in a block of their own, [SPOKEN]like this[/SPOKEN], then write the details as usual after it, and say in the spoken part that the details are in WhatsApp. Only the block is read aloud; the details go to this chat.
Either way, keep the transcript with your notes: file the attached .txt the way skills/plaud.md files recordings (without that skill, wherever your notes go). The recording is not shown in this chat; when it was spoken to you, do not mention filing it in your answer.]`

// maxSpokenWords caps what the phone reads of a [SPOKEN] block that came
// back long anyway: it is cut at the last sentence that fits. An answer
// without a block that is longer than this is not read at all
// (spokenFallback).
const maxSpokenWords = 60

// maxOpeningWords is how long an opening sentence may be to be read before
// spokenFallback's pointer to the chat.
const maxOpeningWords = 25

// The fallbacks an answer too structured or too long to read gets.
// shortcut: English only, since takeVoice does not know the recording's
// language; say it in that language once the site sends one.
const (
	spokenDetailsInChat = "The details are in WhatsApp."
	spokenAnswerInChat  = "The answer is in WhatsApp."
)

var (
	spokenRE = regexp.MustCompile(`(?s)\[SPOKEN\](.*?)\[/SPOKEN\]`)

	mdFenceRE     = regexp.MustCompile("(?s)```.*?(```|$)")
	mdListRE      = regexp.MustCompile(`^\s*([-*•+]|\d+[.)])\s+`)
	mdHeadingRE   = regexp.MustCompile(`^\s*#{1,6}\s*`)
	mdQuoteRE     = regexp.MustCompile(`^\s*>+\s*`)
	mdTableSepRE  = regexp.MustCompile(`^[\s|:-]+$`)
	mdLinkRE      = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdURLRE       = regexp.MustCompile(`(?:https?://|www\.)\S+`)
	mdEmphasisRE  = regexp.MustCompile("\\*+|~~|`+")
	mdUnderlineRE = regexp.MustCompile(`(^|[\s(])_+|_+([\s).,!?:;]|$)`)
)

// spokenAnswer splits a brain's answer to a voice question into what the
// phone reads aloud and what the chat gets: the [SPOKEN] block and the
// rest, or without one the whole answer for both. A block is read cut to
// maxSpokenWords at a sentence's end. Without one, the answer is read
// without its markdown, URLs and emoji, unless it is a list, a table,
// code or long: then spokenFallback is read instead.
func spokenAnswer(text string) (spoken, chat string) {
	if m := spokenRE.FindStringSubmatchIndex(text); m != nil {
		spoken = text[m[2]:m[3]]
		chat = strings.TrimSpace(strings.TrimSpace(text[:m[0]]) + "\n\n" + strings.TrimSpace(text[m[1]:]))
		if chat == "" {
			chat = strings.TrimSpace(spoken)
		}
		return shortenSpoken(strings.TrimSpace(spoken), maxSpokenWords), chat
	}
	plain := stripMarkdown(text)
	if plain == "" || structuredAnswer(text) || len(strings.Fields(plain)) > maxSpokenWords {
		return spokenFallback(text), text
	}
	return plain, text
}

// structuredAnswer reports whether an answer is made for reading, not
// for the ear: a code block, a table or a list of three items or more.
func structuredAnswer(text string) bool {
	if strings.Contains(text, "```") {
		return true
	}
	items := 0
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "|") && strings.Count(line, "|") >= 2 {
			return true
		}
		if mdListRE.MatchString(line) {
			items++
		}
	}
	return items >= 3
}

// spokenFallback is what is read of an answer too structured or too long
// to read: its opening sentence, when that is short prose, and that the
// rest is in the chat.
func spokenFallback(text string) string {
	var para []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || mdListRE.MatchString(t) || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "```") {
			break
		}
		if !mdHeadingRE.MatchString(t) {
			para = append(para, t)
		}
	}
	if s := firstSentence(stripMarkdown(strings.Join(para, "\n"))); s != "" {
		if len(strings.Fields(s)) <= maxOpeningWords {
			return s + " " + spokenDetailsInChat
		}
	}
	return spokenAnswerInChat
}

// firstSentence is text up to its first sentence's end, or "" when it has
// none.
func firstSentence(text string) string {
	for i, r := range text {
		if end := i + utf8.RuneLen(r); sentenceEnd(text, end, r) {
			return text[:end]
		}
	}
	return ""
}

// stripMarkdown is text as it is said: without code blocks, markdown
// marks, list bullets, table pipes, URLs (a link keeps its text) and
// emoji, on one line.
func stripMarkdown(text string) string {
	text = mdFenceRE.ReplaceAllString(text, " ")
	text = mdLinkRE.ReplaceAllString(text, "$1")
	text = mdURLRE.ReplaceAllString(text, "")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "|") && mdTableSepRE.MatchString(line) {
			continue
		}
		line = mdHeadingRE.ReplaceAllString(line, "")
		line = mdQuoteRE.ReplaceAllString(line, "")
		line = mdListRE.ReplaceAllString(line, "")
		lines = append(lines, strings.ReplaceAll(line, "|", " "))
	}
	text = mdEmphasisRE.ReplaceAllString(strings.Join(lines, "\n"), "")
	text = mdUnderlineRE.ReplaceAllString(text, "$1$2")
	text = strings.Map(func(r rune) rune {
		if isEmoji(r) {
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

// isEmoji reports whether r is an emoji or a part of one. Symbols below
// the arrows, such as ° and ©, stay: they are said.
func isEmoji(r rune) bool {
	switch {
	case r == 0x200D, r == 0x20E3, r >= 0xFE00 && r <= 0xFE0F, r >= 0x1F3FB && r <= 0x1F3FF:
		return true
	}
	return r >= 0x2190 && unicode.Is(unicode.So, r)
}

// sentenceEnd reports whether r, which ends at end in text, ends a
// sentence: a full stop inside a number or a time is none.
func sentenceEnd(text string, end int, r rune) bool {
	return strings.ContainsRune(".!?…", r) && (end == len(text) || unicode.IsSpace(rune(text[end])))
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
		if !sentenceEnd(text, end, r) {
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

// temporaryError is a failure that a retry can get past: the bridge tells
// the site, which tells the phone to try again.
type temporaryError struct{ error }

func (temporaryError) Temporary() bool { return true }
func (e temporaryError) Unwrap() error { return e.error }

// add keeps q under key, unless there are too many.
func (b *voiceBook) add(key string, q *voiceQuestion) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if len(b.byID) >= maxVoiceWaiting {
		return temporaryError{errors.New("too many voice questions at once; try again in a few minutes")}
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
	if p.cfg().Plaud.PhoneUpload && !p.cfg().Plaud.Voice {
		return plaudClaims{}, errVoiceOff
	}
	c, err := p.pairedPhone(assertion, aud, now)
	if err != nil {
		return c, err
	}
	if !voiceIDRE.MatchString(c.Rid) {
		return c, errors.New("the question has no valid id")
	}
	return c, nil
}

// takeVoice hands a phone's voice question, or a recording from its pin,
// to the voice brain. The same question again (a retry whose answer got
// lost) asks nothing new.
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
	var started time.Time
	limit, what := VoiceQuestionMax, "question"
	switch c.Src {
	case "":
	case voiceSrcPin:
		limit, what = TranscriptMax, "recording"
		// The name goes into a file name: only the app's own form goes.
		if started, err = time.ParseInLocation(plaudPhoneLayout, c.Name, time.Local); err != nil {
			return nil, errors.New("the recording's name is not its start time")
		}
	default:
		return nil, errors.New("the question is of a kind this Fridolin does not know: update it")
	}
	question := strings.TrimSpace(string(body))
	switch {
	case len(body) > limit:
		return nil, fmt.Errorf("the %s is too long", what)
	case !utf8.ValidString(question):
		return nil, fmt.Errorf("the %s is not text", what)
	case question == "":
		return nil, fmt.Errorf("the %s is empty", what)
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
	text := voicePrompt + "\n\n" + question
	var media []string
	if c.Src == voiceSrcPin {
		rec := phoneRecording(voicePinName, started, question)
		path, err := p.inboxRecording(cfg, br, plaudPhonePrefix+c.Name, rec)
		if err != nil {
			return nil, temporaryError{err}
		}
		text, media = voicePinPrompt+"\n\n"+recordingMessage(cfg, rec), []string{path}
		if len(question) <= VoiceQuestionMax {
			text += "\n\n=== Transcript ===\n" + question
		}
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
	if err := br.Ask(jid, voiceSender, text, media, quiet, answer, done); err != nil {
		p.voice.remove(key)
		return nil, temporaryError{fmt.Errorf("brain %s did not take the %s: %w", br.ID(), what, err)}
	}
	if c.Src == voiceSrcPin {
		p.noteUpload(c.Iss, now)
		// Its name is the time it started, as an upload's is in the log.
		log.Printf("plaud %s%s: sent by phone %s in voice mode, to brain %s", plaudPhonePrefix, c.Name, c.Iss, br.ID())
	} else {
		log.Printf("plaud: phone %s asked brain %s by voice", c.Iss, br.ID())
	}
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
