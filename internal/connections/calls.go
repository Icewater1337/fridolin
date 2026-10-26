package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The limits of what a caller may send.
const (
	// MaxAudio is the largest recording a transcription takes.
	MaxAudio = 25 << 20
	// MaxResponsesBody is the largest request to OpenAI's Responses.
	MaxResponsesBody = 20 << 20
	// MaxSpeechText is the most characters one text-to-speech reads.
	MaxSpeechText = 10000
	// maxPrompt bounds a transcription's prompt.
	maxPrompt = 10000
	// maxVoiceSettings bounds text-to-speech's voice_settings.
	maxVoiceSettings = 16 << 10
	// maxAnswer bounds what a provider may answer.
	maxAnswer = 64 << 20
)

// How long each call may take.
const (
	timeoutTranscribe   = 120 * time.Second
	timeoutResponses    = 180 * time.Second
	timeoutSpeak        = 120 * time.Second
	timeoutELTranscribe = 180 * time.Second
	timeoutVoices       = 30 * time.Second
)

// The models used when a caller names none.
const (
	DefaultOpenAITranscribe     = "gpt-4o-mini-transcribe"
	DefaultElevenLabsSpeech     = "eleven_multilingual_v2"
	DefaultElevenLabsTranscribe = "scribe_v1"
	DefaultSpeechFormat         = "mp3_44100_128"
)

var (
	// modelRE is a model's name, as module.json's models are.
	modelRE    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	languageRE = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8})?$`)
	voiceRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	formatRE   = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	ctypeRE    = regexp.MustCompile(`^[a-z]+/[a-z0-9.+-]{1,64}$`)
	// codeRE is a provider's error code we pass on.
	codeRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	// keyLikeRE is what looks like a key in a provider's message.
	keyLikeRE = regexp.MustCompile(`(?i)\b(sk|xi)[-_][A-Za-z0-9_-]{8,}|[A-Za-z0-9_\-+/=]{32,}`)
)

// ready is connection conn's key, or why there is none.
func (s *Service) ready(conn string) (key, error) {
	if !Known(conn) {
		return key{}, fail(http.StatusNotFound, "no connection %q", conn)
	}
	k, ok := s.loadKey(conn)
	if !ok {
		return key{}, fail(http.StatusServiceUnavailable, "no %s key: set one in the dashboard under Integrations", Title(conn))
	}
	return k, nil
}

// allowed refuses a model c may not use.
func allowed(conn string, c Caller, model string) error {
	if !modelRE.MatchString(model) {
		return fail(http.StatusBadRequest, "%q is not a model name", model)
	}
	if len(c.Models) > 0 && !slices.Contains(c.Models, model) {
		return fail(http.StatusForbidden, "%s may not use model %s with %s: it may use %s", c.ID, model, Title(conn), strings.Join(c.Models, ", "))
	}
	return nil
}

// emit writes the audit's Event of a call that ended with *err.
func (s *Service) emit(c Caller, conn, op, model string, err *error) {
	if s.o.Audit == nil {
		return
	}
	out := "ok"
	if *err != nil {
		out = (*err).Error()
	}
	s.o.Audit(Event{Caller: c, Connection: conn, Op: op, Model: model, Outcome: out})
}

// Refused is the audit's Event of a call refused before it reached an
// operation, such as one a module did not declare.
func (s *Service) Refused(c Caller, conn, op string, err error) {
	s.emit(c, conn, op, "", &err)
}

// scrub takes what looks like a key out of a provider's message.
func scrub(msg string, k key) string {
	if k.APIKey != "" {
		msg = strings.ReplaceAll(msg, k.APIKey, "[redacted]")
	}
	msg = keyLikeRE.ReplaceAllString(msg, "[redacted]")
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 500 {
		n := 500
		for n > 0 && !utf8.RuneStart(msg[n]) {
			n--
		}
		msg = msg[:n] + "…"
	}
	return msg
}

// request is one call to a provider.
type request struct {
	conn, method, path string
	body               []byte
	ctype              string
	timeout            time.Duration
}

// do makes r with key k and answers the headers and body of a 2xx.
func (s *Service) do(ctx context.Context, k key, r request) (http.Header, []byte, error) {
	title := Title(r.conn)
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var body io.Reader
	if r.body != nil {
		body = bytes.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, s.base(r.conn)+r.path, body)
	if err != nil {
		return nil, nil, fail(http.StatusInternalServerError, "the call to %s: %s", title, scrub(err.Error(), k))
	}
	if r.ctype != "" {
		req.Header.Set("Content-Type", r.ctype)
	}
	if r.conn == ElevenLabs {
		req.Header.Set("xi-api-key", k.APIKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+k.APIKey)
		if k.Organization != "" {
			req.Header.Set("OpenAI-Organization", k.Organization)
		}
		if k.Project != "" {
			req.Header.Set("OpenAI-Project", k.Project)
		}
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, nil, transportError(title, r.timeout, err, k)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, nil, transportError(title, r.timeout, err, k)
	}
	if len(data) > maxAnswer {
		return nil, nil, fail(http.StatusBadGateway, "%s answered more than %d MiB", title, maxAnswer>>20)
	}
	if resp.StatusCode/100 != 2 {
		return nil, nil, providerError(r.conn, resp.StatusCode, data, k)
	}
	return resp.Header, data, nil
}

// transportError is a call that got no answer: 504 when it took too
// long, 502 otherwise.
func transportError(title string, timeout time.Duration, err error, k key) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return fail(http.StatusGatewayTimeout, "%s did not answer within %v", title, timeout)
	}
	if errors.Is(err, context.Canceled) {
		return fail(http.StatusBadGateway, "the call to %s was stopped", title)
	}
	return fail(http.StatusBadGateway, "could not reach %s: %s", title, scrub(err.Error(), k))
}

// providerError is a provider's error answer: its message and code.
// OpenAI answers {"error": {"message", "code", "type"}}; ElevenLabs
// {"detail": {"message", "status" or "code"}}, {"detail": "…"} or a
// list of {"msg"}.
func providerError(conn string, status int, data []byte, k key) *Error {
	var msg, code string
	var body struct {
		Error *struct {
			Message string          `json:"message"`
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
		} `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(data, &body) == nil {
		if e := body.Error; e != nil {
			msg, code = e.Message, rawString(e.Code)
			if code == "" {
				code = e.Type
			}
		}
		if len(body.Detail) > 0 {
			var obj struct {
				Message string `json:"message"`
				Status  string `json:"status"`
				Code    string `json:"code"`
			}
			var list []struct {
				Msg string `json:"msg"`
			}
			switch {
			case json.Unmarshal(body.Detail, &obj) == nil:
				msg, code = obj.Message, obj.Code
				if code == "" {
					code = obj.Status
				}
			case json.Unmarshal(body.Detail, &list) == nil && len(list) > 0:
				msg = list[0].Msg
			default:
				msg = rawString(body.Detail)
			}
		}
	}
	if strings.TrimSpace(msg) == "" {
		msg = fmt.Sprintf("it answered %d %s", status, http.StatusText(status))
	}
	if !codeRE.MatchString(code) {
		code = ""
	}
	return &Error{Status: http.StatusBadGateway, Message: Title(conn) + ": " + scrub(msg, k), UpstreamStatus: status, UpstreamCode: code}
}

// rawString is a JSON string's value, or "" for anything else.
func rawString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// ── transcription ────────────────────────────────────────────────────────

// Audio is a recording a caller sends.
type Audio struct {
	Data        []byte
	Filename    string
	ContentType string
}

// TranscribeRequest asks for a recording's text. Prompt is OpenAI's only.
type TranscribeRequest struct {
	Audio
	Model, Language, Prompt string
}

// Transcript is a recording's text, and its language where the provider
// says it (ElevenLabs).
type Transcript struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
}

// transcriptUsage is the usage an OpenAI transcription answers with.
type transcriptUsage struct {
	Type              string  `json:"type"`
	Seconds           float64 `json:"seconds"`
	InputTokens       int64   `json:"input_tokens"`
	OutputTokens      int64   `json:"output_tokens"`
	InputTokenDetails *struct {
		TextTokens  int64 `json:"text_tokens"`
		AudioTokens int64 `json:"audio_tokens"`
	} `json:"input_token_details"`
}

// openAITranscript is OpenAI's transcription answer.
type openAITranscript struct {
	Text     string           `json:"text"`
	Duration float64          `json:"duration"`
	Usage    *transcriptUsage `json:"usage"`
}

// Transcribe turns a recording into text with connection conn.
func (s *Service) Transcribe(ctx context.Context, conn string, c Caller, req TranscribeRequest) (t Transcript, err error) {
	model := req.Model
	if model == "" {
		model = DefaultOpenAITranscribe
		if conn == ElevenLabs {
			model = DefaultElevenLabsTranscribe
		}
	}
	defer s.emit(c, conn, OpTranscribe, model, &err)
	k, err := s.ready(conn)
	if err != nil {
		return t, err
	}
	switch {
	case len(req.Data) == 0:
		return t, fail(http.StatusBadRequest, "no audio: send the recording as file")
	case len(req.Data) > MaxAudio:
		return t, fail(http.StatusBadRequest, "the audio is more than %d MiB", MaxAudio>>20)
	case req.Language != "" && !languageRE.MatchString(req.Language):
		return t, fail(http.StatusBadRequest, "%q is not a language code such as de or en", req.Language)
	case conn == ElevenLabs && req.Prompt != "":
		return t, fail(http.StatusBadRequest, "ElevenLabs takes no prompt")
	case len(req.Prompt) > maxPrompt:
		return t, fail(http.StatusBadRequest, "the prompt is more than %d characters", maxPrompt)
	}
	if err := allowed(conn, c, model); err != nil {
		return t, err
	}
	if err := s.overBudget(conn, c); err != nil {
		return t, err
	}
	fields := [][2]string{{"model", model}, {"language", req.Language}, {"prompt", req.Prompt}, {"response_format", "json"}}
	path, timeout := "/v1/audio/transcriptions", timeoutTranscribe
	if conn == ElevenLabs {
		fields = [][2]string{{"model_id", model}, {"language_code", req.Language}}
		path, timeout = "/v1/speech-to-text", timeoutELTranscribe
	}
	body, ctype := audioForm(req.Audio, fields)
	_, data, err := s.do(ctx, k, request{conn: conn, method: http.MethodPost, path: path, body: body, ctype: ctype, timeout: timeout})
	if err != nil {
		s.record(conn, c.ID, Usage{Requests: 1, Errors: 1})
		return t, err
	}
	if conn == ElevenLabs {
		var a struct {
			Text         string `json:"text"`
			LanguageCode string `json:"language_code"`
			Words        []struct {
				End float64 `json:"end"`
			} `json:"words"`
		}
		if err := json.Unmarshal(data, &a); err != nil {
			s.record(conn, c.ID, Usage{Requests: 1, Errors: 1})
			return t, fail(http.StatusBadGateway, "ElevenLabs answered something that is not a transcript")
		}
		seconds := 0.0
		for _, w := range a.Words {
			seconds = max(seconds, w.End)
		}
		if seconds == 0 {
			seconds = sizeSeconds(len(req.Data))
		}
		u := sttUsage(model, seconds)
		u.Requests = 1
		s.record(conn, c.ID, u)
		return Transcript{Text: a.Text, Language: a.LanguageCode}, nil
	}
	var a openAITranscript
	if err := json.Unmarshal(data, &a); err != nil {
		s.record(conn, c.ID, Usage{Requests: 1, Errors: 1})
		return t, fail(http.StatusBadGateway, "OpenAI answered something that is not a transcript")
	}
	u := transcriptionUsage(model, len(req.Data), a)
	u.Requests = 1
	s.record(conn, c.ID, u)
	return Transcript{Text: a.Text}, nil
}

// audioForm is the multipart body of a transcription: the recording as
// file, and the fields that are set.
func audioForm(a Audio, fields [][2]string) ([]byte, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	ctype := a.ContentType
	if !ctypeRE.MatchString(ctype) {
		ctype = "application/octet-stream"
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, audioName(a.Filename, ctype)))
	h.Set("Content-Type", ctype)
	part, _ := w.CreatePart(h)
	_, _ = part.Write(a.Data)
	for _, f := range fields {
		if f[1] != "" {
			_ = w.WriteField(f[0], f[1])
		}
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

// audioExt are the file name endings of the recordings' types: the
// providers tell a recording's format by its name.
var audioExt = map[string]string{
	"audio/mpeg": ".mp3", "audio/mp3": ".mp3", "audio/mp4": ".m4a", "audio/x-m4a": ".m4a", "audio/m4a": ".m4a",
	"audio/wav": ".wav", "audio/x-wav": ".wav", "audio/wave": ".wav", "audio/webm": ".webm", "video/webm": ".webm",
	"audio/ogg": ".ogg", "audio/opus": ".ogg", "audio/flac": ".flac", "audio/x-flac": ".flac", "video/mp4": ".mp4",
	"audio/aac": ".aac", "audio/amr": ".amr",
}

// AudioType is a recording's type by its file name's ending, "" for one
// not known.
func AudioType(name string) string {
	ext := strings.ToLower(name)
	if i := strings.LastIndex(ext, "."); i >= 0 {
		ext = ext[i:]
	}
	return map[string]string{
		".mp3": "audio/mpeg", ".mpga": "audio/mpeg", ".mpeg": "audio/mpeg", ".m4a": "audio/mp4", ".mp4": "audio/mp4",
		".wav": "audio/wav", ".webm": "audio/webm", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
		".flac": "audio/flac", ".aac": "audio/aac", ".amr": "audio/amr",
	}[ext]
}

// SpeechExt is the file name ending of speech in format, else of its type.
func SpeechExt(format, ctype string) string {
	kind, _, _ := strings.Cut(format, "_")
	switch kind {
	case "mp3", "pcm", "ulaw", "alaw", "opus", "wav":
		return "." + kind
	}
	if ext := audioExt[ctype]; ext != "" {
		return ext
	}
	return ".bin"
}

// audioName is a safe file name for a recording: its own name's letters,
// digits, dots, dashes and underscores, or "audio" with the ending of
// its type. Ogg's other endings become .ogg, which the providers know.
func audioName(name, ctype string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < utf8.RuneSelf && (r == '.' || r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, name)
	name = strings.TrimLeft(name, ".")
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	if !strings.Contains(name, ".") {
		name = "audio" + audioExt[ctype]
	}
	for _, ext := range []string{".opus", ".oga"} {
		if base, ok := strings.CutSuffix(name, ext); ok {
			name = base + ".ogg"
		}
	}
	return name
}

// ── OpenAI's Responses ───────────────────────────────────────────────────

// Responses passes body, a request to OpenAI's Responses, on and answers
// OpenAI's JSON as it is. It must name its model; a streamed or
// background response is refused, since the bridge answers once with the
// whole answer and counts what it cost.
func (s *Service) Responses(ctx context.Context, c Caller, body []byte) (out []byte, err error) {
	model := ""
	defer func() { s.emit(c, OpenAI, OpResponses, model, &err) }()
	k, err := s.ready(OpenAI)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxResponsesBody {
		return nil, fail(http.StatusBadRequest, "the request is more than %d MiB", MaxResponsesBody>>20)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, fail(http.StatusBadRequest, "send a request of OpenAI's Responses API: a JSON object")
	}
	model = rawString(fields["model"])
	if model == "" {
		return nil, fail(http.StatusBadRequest, "name the model, such as gpt-4o-mini")
	}
	for _, flag := range []string{"stream", "background"} {
		var on bool
		if raw, ok := fields[flag]; ok && json.Unmarshal(raw, &on) == nil && on {
			return nil, fail(http.StatusBadRequest, "%s: true is not supported: the bridge answers with the whole response", flag)
		}
	}
	if err := allowed(OpenAI, c, model); err != nil {
		return nil, err
	}
	if err := s.overBudget(OpenAI, c); err != nil {
		return nil, err
	}
	_, data, err := s.do(ctx, k, request{conn: OpenAI, method: http.MethodPost, path: "/v1/responses", body: body, ctype: "application/json", timeout: timeoutResponses})
	if err != nil {
		s.record(OpenAI, c.ID, Usage{Requests: 1, Errors: 1})
		return nil, err
	}
	var a struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		s.record(OpenAI, c.ID, Usage{Requests: 1, Errors: 1})
		return nil, fail(http.StatusBadGateway, "OpenAI answered something that is not JSON")
	}
	priced := model
	if a.Model != "" {
		priced = a.Model
	}
	u := responsesUsage(priced, a.Usage.InputTokens, a.Usage.OutputTokens)
	u.Requests = 1
	s.record(OpenAI, c.ID, u)
	return data, nil
}

// ── ElevenLabs' text-to-speech and voices ────────────────────────────────

// SpeakRequest asks ElevenLabs to read a text. Voice is the default voice
// when empty; VoiceSettings is ElevenLabs' voice_settings object.
type SpeakRequest struct {
	Text, Voice, Model, Format, Language string
	VoiceSettings                        json.RawMessage
}

// Speech is the audio ElevenLabs read, with its type and format, and the
// characters it billed.
type Speech struct {
	Audio       []byte
	ContentType string
	Format      string
	Characters  int64
}

// Speak reads req's text with ElevenLabs.
func (s *Service) Speak(ctx context.Context, c Caller, req SpeakRequest) (sp Speech, err error) {
	model := req.Model
	if model == "" {
		model = DefaultElevenLabsSpeech
	}
	defer s.emit(c, ElevenLabs, OpTTS, model, &err)
	k, err := s.ready(ElevenLabs)
	if err != nil {
		return sp, err
	}
	voice := req.Voice
	if voice == "" {
		voice = s.o.Settings().ElevenLabs.Voice
	}
	format := req.Format
	if format == "" {
		format = DefaultSpeechFormat
	}
	switch {
	case strings.TrimSpace(req.Text) == "":
		return sp, fail(http.StatusBadRequest, "no text to read")
	case utf8.RuneCountInString(req.Text) > MaxSpeechText:
		return sp, fail(http.StatusBadRequest, "the text is more than %d characters", MaxSpeechText)
	case voice == "":
		return sp, fail(http.StatusBadRequest, "name a voice: there is no default voice (set one in the dashboard under Integrations)")
	case !voiceRE.MatchString(voice):
		return sp, fail(http.StatusBadRequest, "%q is not a voice id", voice)
	case !formatRE.MatchString(format):
		return sp, fail(http.StatusBadRequest, "%q is not an output format such as mp3_44100_128", format)
	case req.Language != "" && !languageRE.MatchString(req.Language):
		return sp, fail(http.StatusBadRequest, "%q is not a language code such as de or en", req.Language)
	case len(req.VoiceSettings) > maxVoiceSettings:
		return sp, fail(http.StatusBadRequest, "voice_settings is too large")
	}
	payload := map[string]any{"text": req.Text, "model_id": model}
	if req.Language != "" {
		payload["language_code"] = req.Language
	}
	if vs := bytes.TrimSpace(req.VoiceSettings); len(vs) > 0 && string(vs) != "null" {
		var obj map[string]any
		if json.Unmarshal(vs, &obj) != nil || obj == nil {
			return sp, fail(http.StatusBadRequest, "voice_settings must be an object")
		}
		payload["voice_settings"] = obj
	}
	if err := allowed(ElevenLabs, c, model); err != nil {
		return sp, err
	}
	if err := s.overBudget(ElevenLabs, c); err != nil {
		return sp, err
	}
	body, _ := json.Marshal(payload)
	path := "/v1/text-to-speech/" + url.PathEscape(voice) + "?output_format=" + url.QueryEscape(format)
	h, data, err := s.do(ctx, k, request{conn: ElevenLabs, method: http.MethodPost, path: path, body: body, ctype: "application/json", timeout: timeoutSpeak})
	if err != nil {
		s.record(ElevenLabs, c.ID, Usage{Requests: 1, Errors: 1})
		return sp, err
	}
	chars := int64(utf8.RuneCountInString(req.Text))
	if n, err := strconv.ParseInt(strings.TrimSpace(h.Get("character-cost")), 10, 64); err == nil && n >= 0 {
		chars = n
	}
	u := speechUsage(chars)
	u.Requests = 1
	s.record(ElevenLabs, c.ID, u)
	ctype := h.Get("Content-Type")
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return Speech{Audio: data, ContentType: ctype, Format: format, Characters: chars}, nil
}

// Voice is one of the account's ElevenLabs voices.
type Voice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Voices lists the account's ElevenLabs voices.
func (s *Service) Voices(ctx context.Context, c Caller) (vs []Voice, err error) {
	defer s.emit(c, ElevenLabs, OpVoices, "", &err)
	k, err := s.ready(ElevenLabs)
	if err != nil {
		return nil, err
	}
	if err := s.overBudget(ElevenLabs, c); err != nil {
		return nil, err
	}
	_, data, err := s.do(ctx, k, request{conn: ElevenLabs, method: http.MethodGet, path: "/v1/voices", timeout: timeoutVoices})
	if err != nil {
		s.record(ElevenLabs, c.ID, Usage{Requests: 1, Errors: 1})
		return nil, err
	}
	var a struct {
		Voices []struct {
			VoiceID string `json:"voice_id"`
			Name    string `json:"name"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		s.record(ElevenLabs, c.ID, Usage{Requests: 1, Errors: 1})
		return nil, fail(http.StatusBadGateway, "ElevenLabs answered something that is not a list of voices")
	}
	vs = []Voice{}
	for _, v := range a.Voices {
		vs = append(vs, Voice{ID: v.VoiceID, Name: v.Name})
	}
	s.record(ElevenLabs, c.ID, Usage{Requests: 1})
	return vs, nil
}

// ── the key check ────────────────────────────────────────────────────────

// Check asks the provider whether connection id's key works, by listing
// its models, within timeout. It answers what to tell people, or an
// error that says what is wrong.
func (s *Service) Check(ctx context.Context, id string, timeout time.Duration) (string, error) {
	k, err := s.ready(id)
	if err != nil {
		return "", err
	}
	_, _, err = s.do(ctx, k, request{conn: id, method: http.MethodGet, path: "/v1/models", timeout: timeout})
	e, ok := errors.AsType[*Error](err)
	switch {
	case err == nil:
		return "the key works", nil
	case ok && e.UpstreamStatus == http.StatusUnauthorized:
		return "", fail(http.StatusBadGateway, "%s says the key is wrong (401)", Title(id))
	case ok && e.UpstreamStatus == http.StatusForbidden && id == ElevenLabs:
		return "the key works; it may not list models (403), which Fridolin does not need", nil
	}
	return "", err
}
