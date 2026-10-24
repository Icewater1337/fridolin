package nodeagent

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// FrIDE on the node (docs/NODES.md, "FrIDE"): the agent is FrIDE's
// companion. It holds the token `fride pair-companion` traded for, calls
// FrIDE's loopback companion API with it for the core's fride.* calls,
// hears FrIDE's signed webhook on a loopback port of its own and relays
// the thread news to the core, and declares the fride capability only
// while FrIDE answers.

// FrideTokenFile is the companion token in the node's state dir.
const FrideTokenFile = "fride.token"

// frideTransfers is the folder in the state dir where bundles are staged
// on their way to or from the core.
const frideTransfers = "fride-transfers"

// FrideConfig is node.json's fride section, which `fridolin node fride
// pair` writes.
type FrideConfig struct {
	// URL is FrIDE's loopback address, http://127.0.0.1:<port>.
	URL string `json:"url"`
	// Webhook is the loopback host:port the agent hears FrIDE's webhook
	// on; FrIDE was given http://<Webhook>/fride when it was paired.
	Webhook string `json:"webhook"`
	// Base is the folder fride.repo_clone and fride.repo_new add
	// repositories to: one of FrIDE's base folders, as an absolute path.
	// Empty: FrIDE's only base folder, read from its settings at each
	// call (with several, the call is refused until Base names one).
	Base string `json:"base,omitempty"`
}

// Check reports what is wrong with the section.
func (f *FrideConfig) Check() error {
	if f == nil {
		return nil
	}
	if err := nodeproto.CheckLoopbackURL(f.URL); err != nil {
		return fmt.Errorf("fride.url: %w", err)
	}
	if err := nodeproto.CheckLoopbackURL(webhookURL(f.Webhook)); err != nil {
		return fmt.Errorf("fride.webhook: %w", err)
	}
	if f.Base != "" && !filepath.IsAbs(f.Base) {
		return fmt.Errorf("fride.base: %q is not an absolute path", f.Base)
	}
	return nil
}

// webhookURL is the URL FrIDE posts its news to for a webhook address.
func webhookURL(addr string) string { return "http://" + addr + "/fride" }

// Timing of the FrIDE link.
const (
	// DefaultFrideCheckEvery is how often the agent asks whether FrIDE
	// answers, and reads its fride settings again.
	DefaultFrideCheckEvery = 30 * time.Second
	frideProbeTimeout      = 5 * time.Second
	frideCallTimeout       = 60 * time.Second
	frideSlowTimeout       = 15 * time.Minute
	// frideEventQueue bounds the news waiting for the core; FrIDE retries
	// what is refused while it is full.
	frideEventQueue = 256
	// frideTransferTTL is how long a staged bundle is kept unread.
	frideTransferTTL = 24 * time.Hour
	maxWebhookBody   = 1 << 20
)

// fride is the agent's link to FrIDE.
type fride struct {
	mu    sync.Mutex
	cfg   *FrideConfig
	token string
	// cap is what the agent declares: non-nil while FrIDE answers.
	cap *nodeproto.FrideCap
	// srv hears the webhook on srvAddr.
	srv     *http.Server
	srvAddr string
	lastErr string

	// events is the news on its way to the core; changed says the
	// declaration changed.
	events  chan nodeproto.FrideEvent
	changed chan struct{}
	once    sync.Once
}

func (f *fride) init() {
	f.once.Do(func() {
		f.events = make(chan nodeproto.FrideEvent, frideEventQueue)
		f.changed = make(chan struct{}, 1)
	})
}

// frideClient calls FrIDE: loopback only, no proxy, no redirects.
var frideClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, MaxIdleConns: 4},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// FrideInfo is FrIDE's answer to GET /api/companion/info.
type FrideInfo struct {
	Mode         string   `json:"mode"`
	Configured   bool     `json:"configured"`
	Repos        []string `json:"repos"`
	Profiles     []string `json:"profiles"`
	WebhookURL   string   `json:"webhook_url"`
	Orchestrator bool     `json:"orchestrator"`
}

// FrideError is FrIDE's answer to a call that failed.
type FrideError struct {
	Status  int
	Message string
}

func (e *FrideError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("FrIDE answered %d", e.Status)
	}
	return fmt.Sprintf("FrIDE answered %d: %s", e.Status, e.Message)
}

// frideDo calls FrIDE at base+path with token, sending body (JSON, or an
// io.Reader as it is) and decoding the answer into out (nil to drop it, an
// io.Writer to copy it there).
func frideDo(ctx context.Context, base, token, method, path string, body, out any) error {
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rd, ctype = b, "application/gzip"
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(data), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, rd)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := frideClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(data, &e) != nil {
			e.Error = strings.TrimSpace(string(data))
		}
		return &FrideError{Status: resp.StatusCode, Message: clean(e.Error, 500)}
	}
	switch o := out.(type) {
	case nil:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	case io.Writer:
		_, err := io.Copy(o, resp.Body)
		return err
	default:
		return json.NewDecoder(io.LimitReader(resp.Body, nodeproto.MaxMessageBytes)).Decode(out)
	}
}

// ProbeFride asks the FrIDE at base whether it answers in companion mode
// with token.
func ProbeFride(ctx context.Context, base, token string) (FrideInfo, error) {
	var info FrideInfo
	if err := frideDo(ctx, base, token, http.MethodGet, "/api/companion/info", nil, &info); err != nil {
		return info, err
	}
	if info.Mode != "companion" {
		return info, fmt.Errorf("FrIDE at %s is not in companion mode (%q): start it with fride serve --under-fridolin", base, info.Mode)
	}
	return info, nil
}

// loadFride reads the fride section and the token from the state dir.
func loadFride(dir string) (*FrideConfig, string, error) {
	cfg, err := LoadConfig(dir)
	if err != nil {
		return nil, "", err
	}
	if cfg.Fride == nil {
		return nil, "", nil
	}
	tok, err := os.ReadFile(filepath.Join(dir, FrideTokenFile))
	if err != nil {
		return cfg.Fride, "", fmt.Errorf("the FrIDE token: %w (pair again: fridolin node fride pair)", err)
	}
	return cfg.Fride, strings.TrimSpace(string(tok)), nil
}

// frideCheck reads the fride settings again, keeps the webhook listening
// and asks FrIDE whether it answers. It reports whether what the node
// declares changed.
func (a *Agent) frideCheck(ctx context.Context) bool {
	f := &a.fride
	f.init()
	cfg, token := a.Config.Fride, a.FrideToken
	if a.Dir != "" {
		c, t, err := loadFride(a.Dir)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		f.mu.Lock()
		if msg != "" && msg != f.lastErr {
			a.logf("node: FrIDE: %v", err)
		}
		f.lastErr = msg
		if c == nil && err != nil {
			// node.json does not read (an edit in progress): keep what
			// was read before.
			c, t = f.cfg, f.token
		}
		f.mu.Unlock()
		cfg, token = c, t
	}
	f.mu.Lock()
	f.cfg, f.token = cfg, token
	f.mu.Unlock()
	a.frideListen(cfg)

	var next *nodeproto.FrideCap
	if cfg != nil && token != "" {
		pctx, cancel := context.WithTimeout(ctx, frideProbeTimeout)
		info, err := ProbeFride(pctx, cfg.URL, token)
		cancel()
		if err == nil {
			next = &nodeproto.FrideCap{URL: cfg.URL, Repos: info.Repos}
			if next.Check() != nil {
				next.Repos = nil
			}
		}
	}
	f.mu.Lock()
	old := f.cap
	f.cap = next
	f.mu.Unlock()
	changed := (old == nil) != (next == nil) || old != nil && (old.URL != next.URL || !slices.Equal(old.Repos, next.Repos))
	if changed {
		if next != nil {
			a.logf("node: FrIDE answers at %s; offering it to the core", next.URL)
		} else if old != nil {
			a.logf("node: FrIDE at %s does not answer; no longer offering it", old.URL)
		}
	}
	return changed
}

// frideLoop checks FrIDE every FrideCheckEvery until ctx ends, and tells
// the session when the declaration changed.
func (a *Agent) frideLoop(ctx context.Context) {
	every := a.FrideCheckEvery
	if every <= 0 {
		every = DefaultFrideCheckEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	pruned := time.Time{}
	for {
		select {
		case <-ctx.Done():
			a.frideListen(nil)
			return
		case <-t.C:
		}
		fc := a.frideCheck(ctx)
		if ac := a.androidCheck(false); fc || ac {
			select {
			case a.fride.changed <- struct{}{}:
			default:
			}
		}
		if a.Dir != "" {
			pruneAndroid(filepath.Join(a.Dir, androidTransfers), time.Now(), androidTransferTTL)
		}
		if time.Since(pruned) > time.Hour && a.Dir != "" {
			pruneTransfers(filepath.Join(a.Dir, frideTransfers), time.Now())
			pruned = time.Now()
		}
	}
}

// declared is what the node offers now: node.json's capabilities, FrIDE
// while it answers, and android-build while the toolchain is there.
func (a *Agent) declared() nodeproto.Capabilities {
	c := a.Config.Capabilities
	c.AndroidBuild = a.androidDeclared()
	c.Update = nil
	if a.updateOffered() {
		c.Update = &nodeproto.UpdateCap{Box: a.Config.Box != nil}
	}
	a.fride.mu.Lock()
	if a.fride.cap != nil {
		f := *a.fride.cap
		f.Repos = slices.Clone(f.Repos)
		c.Fride = &f
	} else {
		c.Fride = nil
	}
	a.fride.mu.Unlock()
	a.declareWork(&c)
	return c
}

// frideListen keeps the webhook listener on cfg's address, or stops it
// for none.
func (a *Agent) frideListen(cfg *FrideConfig) {
	f := &a.fride
	addr := ""
	if cfg != nil {
		addr = cfg.Webhook
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.srvAddr == addr && (f.srv != nil || addr == "") {
		return
	}
	if f.srv != nil {
		_ = f.srv.Close()
		f.srv, f.srvAddr = nil, ""
	}
	if addr == "" {
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.logf("node: FrIDE's webhook cannot listen on %s, so its news does not reach the core: %v", addr, err)
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(a.frideWebhook), ReadHeaderTimeout: 10 * time.Second}
	f.srv, f.srvAddr = srv, addr
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logf("node: FrIDE's webhook on %s stopped: %v", addr, err)
		}
	}()
}

// SignFride is the X-Fride-Signature of body for token: sha256= and the hex
// HMAC-SHA256 keyed with the token's SHA-256 (its 32 raw bytes).
func SignFride(token string, body []byte) string {
	key := sha256.Sum256([]byte(token))
	m := hmac.New(sha256.New, key[:])
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// frideWebhook hears FrIDE's thread news: a POST from this machine, signed
// with the token, which goes on to the core while fride is approved.
func (a *Agent) frideWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "no browsers", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		http.Error(w, "unreadable or too large", http.StatusBadRequest)
		return
	}
	a.fride.mu.Lock()
	token := a.fride.token
	a.fride.mu.Unlock()
	if token == "" || !hmac.Equal([]byte(r.Header.Get("X-Fride-Signature")), []byte(SignFride(token, body))) {
		a.logf("node: refused a FrIDE webhook whose signature does not match the token")
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var ev nodeproto.FrideEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "not an event", http.StatusBadRequest)
		return
	}
	ev.Text = clean(ev.Text, 16<<10)
	// News is FrIDE's to report only while the core lets this node serve
	// fride: what happens before the approval, or after it is withdrawn,
	// is dropped here rather than queued, so that it cannot reach the core
	// once an approval arrives behind it. FrIDE gets its 204 all the same:
	// a refusal would only make it retry into the approval.
	if st := a.State(); st.Device != nodeproto.DeviceApproved || !st.Approved.Allows(nodeproto.MethodFrideStatus) {
		a.logf("node: dropped FrIDE's %s of thread %d: fride is not approved on this node", ev.Event, ev.ThreadID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.fride.init()
	select {
	case a.fride.events <- ev:
		w.WriteHeader(http.StatusNoContent)
	default:
		// FrIDE retries a 503 for about two minutes.
		http.Error(w, "the queue to the core is full", http.StatusServiceUnavailable)
	}
}

// taskIDRE is a task id FrIDE may have: a slug.
var taskIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// transferIDRE is a staged bundle's id.
var transferIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

func frideErr(err error) *nodeproto.Error {
	if fe, ok := errors.AsType[*FrideError](err); ok {
		switch fe.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return nodeproto.Errorf(nodeproto.CodeDenied, "FrIDE refused the node's token (%s): pair again with fridolin node fride pair", fe.Message)
		case http.StatusNotFound:
			return nodeproto.Errorf(nodeproto.CodeNotFound, "%s", fe.Message)
		case http.StatusBadRequest:
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s", fe.Message)
		}
		return nodeproto.Errorf(nodeproto.CodeFailed, "%v", fe)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nodeproto.Errorf(nodeproto.CodeTimeout, "FrIDE did not answer in time")
	}
	return nodeproto.Errorf(nodeproto.CodeUnavailable, "FrIDE does not answer: %v", err)
}

// frideCall serves a fride.* method.
func (a *Agent) frideCall(ctx context.Context, method string, raw json.RawMessage) (any, *nodeproto.Error) {
	a.fride.mu.Lock()
	cfg, token := a.fride.cfg, a.fride.token
	a.fride.mu.Unlock()
	if cfg == nil || token == "" {
		return nil, nodeproto.Errorf(nodeproto.CodeUnavailable, "FrIDE is not paired with this node")
	}
	decode := func(v any) *nodeproto.Error {
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, v); err != nil {
			return nodeproto.Errorf(nodeproto.CodeBadRequest, "%s params: %v", method, err)
		}
		return nil
	}
	if method == nodeproto.MethodFrideRepoClone || method == nodeproto.MethodFrideRepoNew {
		var p nodeproto.FrideRepoParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.frideRepo(ctx, method, cfg, token, p)
	}
	timeout := frideCallTimeout
	if method == nodeproto.MethodFrideExport || method == nodeproto.MethodFrideImport || method == nodeproto.MethodFrideClose || method == nodeproto.MethodFrideDelete {
		timeout = frideSlowTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	do := func(m, path string, body, out any) *nodeproto.Error {
		if err := frideDo(ctx, cfg.URL, token, m, path, body, out); err != nil {
			return frideErr(err)
		}
		return nil
	}
	task := func(id string) (string, *nodeproto.Error) {
		if !taskIDRE.MatchString(id) {
			return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "task id %q", id)
		}
		return url.PathEscape(id), nil
	}
	switch method {
	case nodeproto.MethodFrideTask:
		var p nodeproto.FrideTaskParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if strings.TrimSpace(p.Message) == "" {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the message is empty")
		}
		var out nodeproto.FrideThread
		return out, do(http.MethodPost, "/api/companion/tasks", p, &out)
	case nodeproto.MethodFrideStatus:
		var p nodeproto.FrideStatusParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		var out nodeproto.FrideStatus
		if e := do(http.MethodGet, "/api/companion/threads/"+strconv.Itoa(p.ThreadID), nil, &out.Thread); e != nil {
			return nil, e
		}
		if out.Thread.TaskID != "" {
			if id, e := task(out.Thread.TaskID); e == nil {
				var t nodeproto.FrideTask
				if do(http.MethodGet, "/api/companion/tasks/"+id, nil, &t) == nil {
					out.Task = &t
				}
			}
		}
		return out, nil
	case nodeproto.MethodFrideReply:
		var p nodeproto.FrideReplyParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		if strings.TrimSpace(p.Text) == "" {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "the reply is empty")
		}
		var out nodeproto.FrideThread
		return out, do(http.MethodPost, "/api/companion/threads/"+strconv.Itoa(p.ThreadID)+"/reply", map[string]string{"text": p.Text}, &out)
	case nodeproto.MethodFrideTaskInfo, nodeproto.MethodFrideClose:
		var p nodeproto.FrideTaskRef
		if e := decode(&p); e != nil {
			return nil, e
		}
		id, e := task(p.TaskID)
		if e != nil {
			return nil, e
		}
		var out nodeproto.FrideTask
		if method == nodeproto.MethodFrideTaskInfo {
			return out, do(http.MethodGet, "/api/companion/tasks/"+id, nil, &out)
		}
		return out, do(http.MethodPost, "/api/companion/tasks/"+id+"/close", nil, &out)
	case nodeproto.MethodFrideDelete:
		var p nodeproto.FrideDeleteParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		id, e := task(p.TaskID)
		if e != nil {
			return nil, e
		}
		// The rest of FrIDE's API, which the UI uses: removing a workspace
		// has no companion route. force=1 drops uncommitted work, such as
		// the rationale a close writes; the core asks for it only once an
		// archive holds the task.
		path := "/api/tasks/" + id
		if p.Force {
			path += "?force=1"
		}
		return struct{}{}, do(http.MethodDelete, path, nil, nil)
	case nodeproto.MethodFrideMem:
		var m struct {
			nodeproto.FrideMem
			Mem struct {
				HostFree uint64 `json:"hostFree"`
			} `json:"mem"`
		}
		if e := do(http.MethodGet, "/api/companion/mem", nil, &m); e != nil {
			return nil, e
		}
		out := m.FrideMem
		out.HostFree = m.Mem.HostFree
		return out, nil
	case nodeproto.MethodFrideExport:
		var p nodeproto.FrideExportParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		id, e := task(p.TaskID)
		if e != nil {
			return nil, e
		}
		path := "/api/companion/tasks/" + id + "/export"
		if p.Stop {
			path += "?stop=1"
		}
		return a.frideExport(func(w io.Writer) *nodeproto.Error { return do(http.MethodGet, path, nil, w) })
	case nodeproto.MethodFrideRead, nodeproto.MethodFrideWrite, nodeproto.MethodFrideDrop:
		var p nodeproto.FrideChunkParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		return a.frideChunk(method, p)
	case nodeproto.MethodFrideImport:
		var p nodeproto.FrideImportParams
		if e := decode(&p); e != nil {
			return nil, e
		}
		path, e := a.transferPath(p.ID)
		if e != nil {
			return nil, e
		}
		q := url.Values{}
		if p.As != "" {
			if !taskIDRE.MatchString(p.As) {
				return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "task id %q", p.As)
			}
			q.Set("as", p.As)
		}
		if p.StartAgent {
			q.Set("start_agent", "1")
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no transfer %s", p.ID)
		}
		defer f.Close()
		u := "/api/companion/tasks/import"
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		var out nodeproto.FrideTask
		return out, do(http.MethodPost, u, f, &out)
	}
	return nil, nodeproto.Errorf(nodeproto.CodeUnsupported, "no method %q", method)
}

func (a *Agent) transfersDir() (string, *nodeproto.Error) {
	if a.Dir == "" {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "the agent has no state dir")
	}
	dir := filepath.Join(a.Dir, frideTransfers)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	return dir, nil
}

func (a *Agent) transferPath(id string) (string, *nodeproto.Error) {
	if !transferIDRE.MatchString(id) {
		return "", nodeproto.Errorf(nodeproto.CodeBadRequest, "transfer id %q", id)
	}
	dir, e := a.transfersDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(dir, id+".bundle"), nil
}

func newTransferID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// frideExport stages what get writes as a new transfer: whole, or not at
// all.
func (a *Agent) frideExport(get func(io.Writer) *nodeproto.Error) (any, *nodeproto.Error) {
	id := newTransferID()
	path, e := a.transferPath(id)
	if e != nil {
		return nil, e
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	h := sha256.New()
	ge := get(io.MultiWriter(f, h))
	cerr := f.Close()
	if ge == nil && cerr != nil {
		ge = nodeproto.Errorf(nodeproto.CodeFailed, "%v", cerr)
	}
	if ge != nil {
		_ = os.Remove(path)
		return nil, ge
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(path)
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "FrIDE's export is empty")
	}
	return nodeproto.FrideTransfer{ID: id, Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// frideChunk reads, appends to or drops a transfer.
func (a *Agent) frideChunk(method string, p nodeproto.FrideChunkParams) (any, *nodeproto.Error) {
	if method == nodeproto.MethodFrideWrite && p.ID == "" {
		if p.Offset != 0 {
			return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "a new transfer starts at offset 0")
		}
		p.ID = newTransferID()
	}
	path, e := a.transferPath(p.ID)
	if e != nil {
		return nil, e
	}
	switch method {
	case nodeproto.MethodFrideDrop:
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
		}
		return struct{}{}, nil
	case nodeproto.MethodFrideRead:
		return readChunk(path, p)
	}
	// fride.write
	tr, e := appendChunk(path, p)
	if e != nil {
		return nil, e
	}
	return tr, nil
}

// appendChunk appends p's chunk to the staged file at path, making it at
// offset 0.
func appendChunk(path string, p nodeproto.FrideChunkParams) (nodeproto.FrideTransfer, *nodeproto.Error) {
	if len(p.Data) > nodeproto.FrideChunkBytes {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeBadRequest, "a chunk holds at most %d bytes", nodeproto.FrideChunkBytes)
	}
	flags := os.O_WRONLY | os.O_APPEND
	if p.Offset == 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	if st.Size() != p.Offset {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeBadRequest, "transfer %s holds %d bytes, not %d", p.ID, st.Size(), p.Offset)
	}
	if _, err := f.Write(p.Data); err != nil {
		return nodeproto.FrideTransfer{}, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	return nodeproto.FrideTransfer{ID: p.ID, Size: p.Offset + int64(len(p.Data))}, nil
}

// readChunk reads the chunk of the staged file at path that p names.
func readChunk(path string, p nodeproto.FrideChunkParams) (any, *nodeproto.Error) {
	if p.Offset < 0 {
		return nil, nodeproto.Errorf(nodeproto.CodeBadRequest, "offset %d", p.Offset)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeNotFound, "no transfer %s", p.ID)
	}
	defer f.Close()
	n := p.Max
	if n <= 0 || n > nodeproto.FrideChunkBytes {
		n = nodeproto.FrideChunkBytes
	}
	// A buffer of the fixed chunk size, of which a smaller Max reads a part.
	buf := make([]byte, nodeproto.FrideChunkBytes)[:n]
	k, err := f.ReadAt(buf, p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", err)
	}
	st, serr := f.Stat()
	if serr != nil {
		return nil, nodeproto.Errorf(nodeproto.CodeFailed, "%v", serr)
	}
	return nodeproto.FrideChunk{Data: buf[:k], EOF: p.Offset+int64(k) >= st.Size()}, nil
}

// pruneTransfers removes staged bundles older than frideTransferTTL: what
// a core that went away mid-transfer left.
func pruneTransfers(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > frideTransferTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// FridePairOptions are what `fridolin node fride pair` was given.
type FridePairOptions struct {
	// Dir is the node's state dir; the node must have joined.
	Dir string
	// URL is FrIDE's loopback address.
	URL string
	// Code is the code of `fride pair-companion`.
	Code string
	// Webhook is the loopback host:port to hear FrIDE's webhook on; ""
	// keeps the one paired before, or picks a free port.
	Webhook string
	// Base is the folder new repositories go to (FrideConfig.Base); ""
	// keeps the one set before.
	Base string
}

// PairFride trades the code for FrIDE's companion token, gives FrIDE the
// webhook, and keeps both in the state dir: the token in fride.token
// (0600), the addresses in node.json. A running agent picks them up at its
// next check of FrIDE.
func PairFride(ctx context.Context, o FridePairOptions) (FrideInfo, error) {
	if err := nodeproto.CheckLoopbackURL(o.URL); err != nil {
		return FrideInfo{}, fmt.Errorf("--url: %w", err)
	}
	if strings.TrimSpace(o.Code) == "" {
		return FrideInfo{}, errors.New("the pairing code is missing: run fride pair-companion")
	}
	cfg, err := LoadConfig(o.Dir)
	if err != nil {
		return FrideInfo{}, err
	}
	webhook := o.Webhook
	if webhook == "" && cfg.Fride != nil {
		webhook = cfg.Fride.Webhook
	}
	if webhook == "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return FrideInfo{}, fmt.Errorf("finding a port for FrIDE's webhook: %w", err)
		}
		webhook = ln.Addr().String()
		_ = ln.Close()
	}
	base := o.Base
	if base == "" && cfg.Fride != nil {
		base = cfg.Fride.Base
	} else if base != "" {
		if base, err = checkBaseDir(base); err != nil {
			return FrideInfo{}, fmt.Errorf("--base: %w", err)
		}
	}
	fc := &FrideConfig{URL: strings.TrimRight(o.URL, "/"), Webhook: webhook, Base: base}
	if err := fc.Check(); err != nil {
		return FrideInfo{}, err
	}
	var paired struct {
		Token string `json:"token"`
	}
	if err := frideDo(ctx, fc.URL, "", http.MethodPost, "/api/companion/pair", map[string]string{"code": strings.TrimSpace(o.Code), "webhook_url": webhookURL(webhook)}, &paired); err != nil {
		return FrideInfo{}, fmt.Errorf("pairing with FrIDE at %s: %w", fc.URL, err)
	}
	if !strings.HasPrefix(paired.Token, "frc_") {
		return FrideInfo{}, fmt.Errorf("FrIDE at %s answered the pairing without a token", fc.URL)
	}
	if err := writeFile(filepath.Join(o.Dir, FrideTokenFile), []byte(paired.Token+"\n"), 0o600); err != nil {
		return FrideInfo{}, err
	}
	cfg.Fride = fc
	if err := SaveConfig(o.Dir, cfg); err != nil {
		return FrideInfo{}, err
	}
	return ProbeFride(ctx, fc.URL, paired.Token)
}
