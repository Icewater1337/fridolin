// Package connections holds Fridolin's own keys for OpenAI and ElevenLabs
// and makes the calls site modules and brains ask for
// (docs/CONNECTIONS.md).
//
// A caller names what it wants. The bridge checks what the caller
// declared or was granted and its monthly budget, calls the provider with
// the key, and logs the usage. No caller ever sees a key: it stays in
// <data_dir>/connections and in the requests this package sends. Errors
// the providers answer are scrubbed of anything key-like before they go
// back to a caller, into a log or into the audit.
//
// The package uses the standard library only. The bridge hands it its
// settings, an HTTP client, a clock and the folder it keeps its files in.
package connections

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
)

// The connections.
const (
	OpenAI     = "openai"
	ElevenLabs = "elevenlabs"
)

// The operations. ElevenLabs and OpenAI both transcribe.
const (
	OpTranscribe = "transcribe"
	OpResponses  = "responses"
	OpTTS        = "tts"
	OpVoices     = "voices"
)

// connection is one provider the bridge connects to.
type connection struct {
	id, title string
	ops       []string
}

var known = []connection{
	{id: OpenAI, title: "OpenAI", ops: []string{OpTranscribe, OpResponses}},
	{id: ElevenLabs, title: "ElevenLabs", ops: []string{OpTTS, OpTranscribe, OpVoices}},
}

func lookup(id string) (connection, bool) {
	i := slices.IndexFunc(known, func(c connection) bool { return c.id == id })
	if i < 0 {
		return connection{}, false
	}
	return known[i], true
}

// IDs are the connections, in the order the dashboard shows them.
func IDs() []string {
	out := make([]string, len(known))
	for i, c := range known {
		out[i] = c.id
	}
	return out
}

// Known reports whether id is a connection.
func Known(id string) bool { _, ok := lookup(id); return ok }

// Title is a connection's name for people, such as "OpenAI".
func Title(id string) string {
	if c, ok := lookup(id); ok {
		return c.title
	}
	return id
}

// Ops are the operations a connection offers.
func Ops(id string) []string {
	c, _ := lookup(id)
	return slices.Clone(c.ops)
}

// Policy is what config.json says of one connection: the brains whose
// runs may use it, what each may spend a month, and for ElevenLabs the
// voice text-to-speech uses when a caller names none.
type Policy struct {
	Brains          []string
	BrainMonthlyUSD float64
	Voice           string
}

// Settings are the policies of both connections.
type Settings struct {
	OpenAI, ElevenLabs Policy
}

// Of is the policy of connection id.
func (s Settings) Of(id string) Policy {
	if id == ElevenLabs {
		return s.ElevenLabs
	}
	if id == OpenAI {
		return s.OpenAI
	}
	return Policy{}
}

// Endpoints are the providers' base URLs. Tests point them at fakes.
type Endpoints struct {
	OpenAI, ElevenLabs string
}

// The providers' own addresses.
const (
	DefaultOpenAI     = "https://api.openai.com"
	DefaultElevenLabs = "https://api.elevenlabs.io"
)

// Event is one call a caller asked for, for the audit: who, which
// connection and operation, with which model, and how it ended. It never
// holds what was sent or answered.
type Event struct {
	Caller     Caller
	Connection string
	Op         string
	Model      string
	// Outcome is "ok", or what went wrong.
	Outcome string
}

// Options are what the bridge hands the service.
type Options struct {
	// Dir is <data_dir>/connections, where the keys and the usage log
	// are kept.
	Dir string
	// Settings are config.json's connections, read at each call.
	Settings func() Settings
	// HTTP is the client the calls go out with; nil makes one.
	HTTP *http.Client
	// Endpoints are the providers' addresses; an empty one is the real.
	Endpoints Endpoints
	// Now is the clock months are counted by; nil is time.Now.
	Now func() time.Time
	// Audit takes each call's Event; nil drops them.
	Audit func(Event)
}

// Service holds the keys and makes the calls.
type Service struct {
	o    Options
	http *http.Client
	// mu guards the usage log; usage is loaded on first use.
	mu    sync.Mutex
	usage *usageLog
}

// New is the service of o.
func New(o Options) *Service {
	if o.Settings == nil {
		o.Settings = func() Settings { return Settings{} }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Endpoints.OpenAI == "" {
		o.Endpoints.OpenAI = DefaultOpenAI
	}
	if o.Endpoints.ElevenLabs == "" {
		o.Endpoints.ElevenLabs = DefaultElevenLabs
	}
	client := o.HTTP
	if client == nil {
		// Each call has a deadline of its own (its context).
		client = &http.Client{}
	}
	return &Service{o: o, http: client}
}

func (s *Service) base(conn string) string {
	if conn == ElevenLabs {
		return s.o.Endpoints.ElevenLabs
	}
	return s.o.Endpoints.OpenAI
}

// month is the month usage is counted in now, UTC "YYYY-MM".
func (s *Service) month() string { return s.o.Now().UTC().Format("2006-01") }

// Caller is who asks for a call.
type Caller struct {
	// ID is "module:<id>" or "brain:<id>".
	ID string
	// Run is a brain's run, for the audit; "" for a module.
	Run string
	// Models are the models it may use; nil allows any.
	Models []string
	// LimitUSD is what it may spend a month, estimated; 0 is no limit.
	LimitUSD float64
}

// ModuleCaller is a site module that declared d.
func ModuleCaller(module string, d Declaration) Caller {
	return Caller{ID: "module:" + module, Models: d.Models, LimitUSD: d.MonthlyUSD}
}

// BrainCaller is a run of brain id with its monthly limit.
func BrainCaller(id, run string, limitUSD float64) Caller {
	return Caller{ID: "brain:" + id, Run: run, LimitUSD: limitUSD}
}

// Declaration is what a site module's module.json declares of one
// connection (capabilities.openai, capabilities.elevenlabs).
type Declaration struct {
	Use        []string
	Models     []string
	MonthlyUSD float64
}

// Error is a call that did not go through, with the HTTP status the
// bridge answers it with. A provider's own error carries its status and
// code.
type Error struct {
	Status         int
	Message        string
	UpstreamStatus int
	UpstreamCode   string
}

func (e *Error) Error() string { return e.Message }

// HTTPStatus is the status the bridge answers the error with.
func (e *Error) HTTPStatus() int { return e.Status }

func fail(status int, format string, a ...any) *Error {
	return &Error{Status: status, Message: fmt.Sprintf(format, a...)}
}

// StatusOf is the HTTP status of err: an Error's, else 500.
func StatusOf(err error) int {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Status
	}
	return http.StatusInternalServerError
}

// usd writes an amount of dollars as people do: 20, 7.5, 0.25.
func usd(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
