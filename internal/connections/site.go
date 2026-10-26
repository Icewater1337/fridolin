package connections

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
)

// The site bridge's calls (docs/CONNECTIONS.md, "Site modules"): a
// module's server code calls the bridge's site listener, which checks the
// listener's token, and names itself in ModuleHeader. The bridge checks
// that the live commit's manifest of that module declares the operation,
// and the model where it lists models, and the module's budget, before it
// calls the provider.

// ModuleHeader names the module a call of the site bridge comes from.
const ModuleHeader = "X-Fridolin-Module"

// ModuleLookup is what the live commit's module id declares, by
// connection, or an error when no such module is live.
type ModuleLookup func(id string) (map[string]Declaration, error)

// moduleRE is what can be a module's id.
var moduleRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// The bodies the site bridge takes for each call; the bridge's listener
// holds requests to them (siteWrites).
const (
	SiteAudioBody     = MaxAudio + 1<<20
	SiteResponsesBody = MaxResponsesBody
	SiteSpeechBody    = 1 << 20
)

// SiteMux serves the calls under /site-api/connections/.
func (s *Service) SiteMux(lookup ModuleLookup) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /site-api/connections/openai/transcribe", s.site(lookup, OpenAI, OpTranscribe, s.siteTranscribe(OpenAI)))
	mux.HandleFunc("POST /site-api/connections/openai/responses", s.site(lookup, OpenAI, OpResponses, s.siteResponses))
	mux.HandleFunc("POST /site-api/connections/elevenlabs/tts", s.site(lookup, ElevenLabs, OpTTS, s.siteSpeak))
	mux.HandleFunc("POST /site-api/connections/elevenlabs/transcribe", s.site(lookup, ElevenLabs, OpTranscribe, s.siteTranscribe(ElevenLabs)))
	mux.HandleFunc("GET /site-api/connections/elevenlabs/voices", s.site(lookup, ElevenLabs, OpVoices, s.siteVoices))
	return mux
}

// siteOp answers a call of module caller c, or returns why not.
type siteOp func(w http.ResponseWriter, r *http.Request, c Caller) error

// site checks the module a call names, and what it declares, before op.
func (s *Service) site(lookup ModuleLookup, conn, op string, h siteOp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		module := r.Header.Get(ModuleHeader)
		if module == "" {
			WriteError(w, fail(http.StatusUnauthorized, "name the module that calls in %s", ModuleHeader))
			return
		}
		if !moduleRE.MatchString(module) {
			WriteError(w, fail(http.StatusNotFound, "no module %q is live", module))
			return
		}
		c := Caller{ID: "module:" + module}
		decls, err := lookup(module)
		if err != nil {
			err = fail(http.StatusNotFound, "%s", err.Error())
			s.Refused(c, conn, op, err)
			WriteError(w, err)
			return
		}
		d, ok := decls[conn]
		if !ok || !slices.Contains(d.Use, op) {
			err := fail(http.StatusForbidden, "module %s does not declare %s in capabilities.%s.use", module, op, conn)
			s.Refused(c, conn, op, err)
			WriteError(w, err)
			return
		}
		if err := h(w, r, ModuleCaller(module, d)); err != nil {
			WriteError(w, err)
		}
	}
}

func (s *Service) siteTranscribe(conn string) siteOp {
	return func(w http.ResponseWriter, r *http.Request, c Caller) error {
		req, err := readAudioForm(r)
		if err != nil {
			s.Refused(c, conn, OpTranscribe, err)
			return err
		}
		t, err := s.Transcribe(r.Context(), conn, c, req)
		if err != nil {
			return err
		}
		if conn == OpenAI {
			writeJSON(w, http.StatusOK, map[string]string{"text": t.Text})
			return nil
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": t.Text, "language": t.Language})
		return nil
	}
}

// readAudioForm reads a transcription's form: the recording as file,
// and model, language and prompt.
func readAudioForm(r *http.Request) (TranscribeRequest, error) {
	var req TranscribeRequest
	mr, err := r.MultipartReader()
	if err != nil {
		return req, fail(http.StatusBadRequest, "send multipart/form-data with the recording as file")
	}
	got := false
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return req, fail(http.StatusBadRequest, "reading the form: %v", err)
		}
		limit := int64(maxPrompt)
		if p.FormName() == "file" {
			limit = MaxAudio
		}
		data, err := io.ReadAll(io.LimitReader(p, limit+1))
		if err != nil {
			return req, fail(http.StatusBadRequest, "reading the form: %v", err)
		}
		if int64(len(data)) > limit {
			if p.FormName() == "file" {
				return req, fail(http.StatusBadRequest, "the audio is more than %d MiB", MaxAudio>>20)
			}
			return req, fail(http.StatusBadRequest, "%s is more than %d characters", p.FormName(), maxPrompt)
		}
		switch p.FormName() {
		case "file":
			ctype, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			req.Data, req.Filename, req.ContentType, got = data, p.FileName(), ctype, true
		case "model":
			req.Model = string(data)
		case "language":
			req.Language = string(data)
		case "prompt":
			req.Prompt = string(data)
		}
	}
	if !got {
		return req, fail(http.StatusBadRequest, "no audio: send the recording as file")
	}
	return req, nil
}

func (s *Service) siteResponses(w http.ResponseWriter, r *http.Request, c Caller) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxResponsesBody+1))
	if err != nil {
		err = fail(http.StatusBadRequest, "reading the request: %v", err)
		s.Refused(c, OpenAI, OpResponses, err)
		return err
	}
	out, err := s.Responses(r.Context(), c, body)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
	return nil
}

func (s *Service) siteSpeak(w http.ResponseWriter, r *http.Request, c Caller) error {
	var req struct {
		Text          string          `json:"text"`
		Voice         string          `json:"voice"`
		Model         string          `json:"model"`
		Format        string          `json:"format"`
		Language      string          `json:"language"`
		VoiceSettings json.RawMessage `json:"voice_settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		err := fail(http.StatusBadRequest, "send {\"text\": \"…\"} as JSON: %v", err)
		s.Refused(c, ElevenLabs, OpTTS, err)
		return err
	}
	sp, err := s.Speak(r.Context(), c, SpeakRequest{Text: req.Text, Voice: req.Voice, Model: req.Model, Format: req.Format, Language: req.Language, VoiceSettings: req.VoiceSettings})
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", sp.ContentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sp.Audio)
	return nil
}

func (s *Service) siteVoices(w http.ResponseWriter, r *http.Request, c Caller) error {
	vs, err := s.Voices(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"voices": vs})
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError answers err as JSON: {"error"}, with the provider's status
// and code when it was the provider's.
func WriteError(w http.ResponseWriter, err error) {
	body := map[string]any{"error": err.Error()}
	if e, ok := errors.AsType[*Error](err); ok && e.UpstreamStatus != 0 {
		body["upstream_status"] = e.UpstreamStatus
		if e.UpstreamCode != "" {
			body["upstream_code"] = e.UpstreamCode
		}
	}
	writeJSON(w, StatusOf(err), body)
}
