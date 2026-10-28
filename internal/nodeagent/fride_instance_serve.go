package nodeagent

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Icewater1337/fridolin/internal/nodeproto"
)

// The agent keeps each instance's FrIDE running while its state says
// run: it holds the serve script's exec open (sbx stops a VM 30 s after
// its last exec ends), starts it again when it ends, waiting longer after
// each quick end, and lets go of it when run is cleared: whoever cleared
// it ends FrIDE (a stop, a remake), and with it the exec, which the
// agent ends itself only when it is still there frideServeStopWait
// later. It hears each running instance's webhook on the instance's
// loopback port. When the agent stops, FrIDE runs on until the VM stops,
// unless the agent's next start holds it again first.
const (
	// DefaultFrideInstancePoll is how often the agent reads the
	// instances' state.
	DefaultFrideInstancePoll = 5 * time.Second
	frideServeMinBackoff     = 2 * time.Second
	frideServeMaxBackoff     = 5 * time.Minute
	// frideServeSteady is how long a run must last for the wait before
	// the next start to go back to the shortest.
	frideServeSteady = 2 * time.Minute
	// frideServeStopWait bounds ending FrIDE: how long a held exec whose
	// run is cleared may take to end.
	frideServeStopWait = 30 * time.Second
	// frideServeLogLines bounds the lines one serve logs.
	frideServeLogLines = 20
)

// frideInstances is the agent's part of the instances: what is under
// way, the supervisor's channels and the fride_host declaration.
type frideInstances struct {
	once sync.Once
	mu   sync.Mutex
	// starting are the instances a start works on; locks hold the calls
	// on one instance to one at a time; problems say why each one's FrIDE
	// last ended.
	starting map[string]bool
	locks    map[string]*sync.Mutex
	problems map[string]string
	// allocMu holds new instances' ports to one at a time.
	allocMu sync.Mutex
	// kick has the supervisor read the state now; syncs asks it for a
	// pass, which it acknowledges by closing the channel; supervising says
	// it runs.
	kick        chan struct{}
	syncs       chan chan struct{}
	supervising atomic.Bool
	// refresh says the declaration is due (frideLoop).
	refresh chan struct{}
	// The declaration, refreshed at most every frideHostEvery.
	refreshMu sync.Mutex
	capMu     sync.Mutex
	cap       *nodeproto.FrideHostCap
	capAt     time.Time

	// url, wait and portBase are instanceOps'; memory reads the
	// machine's memory (hostMemory when nil). Tests set them.
	url      func(name string, port int) string
	wait     time.Duration
	portBase int
	memory   func() (total, available uint64, err error)
}

func (f *frideInstances) init() {
	f.once.Do(func() {
		f.starting, f.locks, f.problems = map[string]bool{}, map[string]*sync.Mutex{}, map[string]string{}
		f.kick, f.syncs, f.refresh = make(chan struct{}, 1), make(chan chan struct{}), make(chan struct{}, 1)
	})
}

// lock is the instance name's lock.
func (f *frideInstances) lock(name string) *sync.Mutex {
	f.init()
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		f.locks[name] = l
	}
	return l
}

func (f *frideInstances) setStarting(name string, v bool) {
	f.init()
	f.mu.Lock()
	defer f.mu.Unlock()
	if v {
		f.starting[name] = true
	} else {
		delete(f.starting, name)
	}
}

func (f *frideInstances) isStarting(name string) bool {
	f.init()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starting[name]
}

func (f *frideInstances) setProblem(name, p string) {
	f.init()
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == "" {
		delete(f.problems, name)
	} else {
		f.problems[name] = p
	}
}

func (f *frideInstances) serveProblem(name string) string {
	f.init()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.problems[name]
}

// kickServe has the supervisor read the state now.
func (f *frideInstances) kickServe() {
	f.init()
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// syncServe waits until the supervisor made a pass over the state as it
// is now: from then on it starts no FrIDE whose instance's run is
// cleared, and lets go of the one it holds.
func (f *frideInstances) syncServe(ctx context.Context) {
	f.init()
	if !f.supervising.Load() {
		return
	}
	ack := make(chan struct{})
	ctx, cancel := context.WithTimeout(ctx, frideServeStopWait)
	defer cancel()
	select {
	case f.syncs <- ack:
	case <-ctx.Done():
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	}
}

// instanceOps are the agent's instanceOps.
func (a *Agent) instanceOps() instanceOps {
	a.inst.init()
	return instanceOps{dir: a.Dir, d: a.sbx(), out: &lineLogger{logf: a.logf, prefix: "node: ", left: 200}, secrets: a.CoreSecrets,
		url: a.inst.url, wait: a.inst.wait, portBase: a.inst.portBase}
}

// frideServe is the held exec of an instance's serve script.
type frideServe struct {
	name    string
	cmd     *exec.Cmd
	done    chan struct{}
	started time.Time
	err     error
	log     *lineLogger
}

// served is the supervisor's part of one instance.
type served struct {
	cur      *frideServe
	backoff  time.Duration
	next     time.Time
	startErr string
	// letGo is when the agent began to let go of cur: its run was
	// cleared. Zero while it holds it.
	letGo time.Time
}

// frideInstancesSupervise starts the loop that keeps the instances'
// FrIDEs running, on a native node with sbx and a state dir.
func (a *Agent) frideInstancesSupervise(ctx context.Context, wg *sync.WaitGroup) {
	if a.Dir == "" || a.sbx() == nil || !sbxOS() || a.Config.Box != nil {
		return
	}
	a.inst.init()
	a.inst.supervising.Store(true)
	wg.Go(func() {
		defer a.inst.supervising.Store(false)
		a.frideInstancesLoop(ctx)
	})
}

func (a *Agent) frideInstancesLoop(ctx context.Context) {
	poll := a.FrideInstancePoll
	if poll <= 0 {
		poll = DefaultFrideInstancePoll
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	runs := map[string]*served{}
	hooks := map[string]*instanceHook{}
	ended := make(chan *frideServe, 8)
	defer func() {
		for _, r := range runs {
			if r.cur != nil {
				_ = r.cur.cmd.Process.Kill()
				<-r.cur.done
			}
		}
		for _, h := range hooks {
			_ = h.srv.Close()
		}
	}()
	var acks []chan struct{}
	for {
		a.frideInstancesPass(runs, hooks, ended, ctx.Done())
		for _, ack := range acks {
			close(ack)
		}
		acks = nil
		select {
		case <-ctx.Done():
			return
		case s := <-ended:
			r := runs[s.name]
			if r == nil || r.cur != s {
				continue
			}
			if !r.letGo.IsZero() {
				a.logf("node: let go of the FrIDE instance %s", s.name)
				r.cur, r.letGo = nil, time.Time{}
				a.inst.setProblem(s.name, "")
				a.frideHostKick()
				continue
			}
			if time.Since(s.started) >= frideServeSteady {
				r.backoff = frideServeMinBackoff
			}
			why := s.log.lastLine()
			if why == "" {
				why = errText(s.err)
			}
			a.inst.setProblem(s.name, why)
			a.logf("node: the FrIDE instance %s's exec ended (%v); starting it again in %s if it should run", s.name, s.err, r.backoff)
			r.cur, r.next, r.backoff = nil, time.Now().Add(r.backoff), min(2*r.backoff, frideServeMaxBackoff)
			a.frideHostKick()
		case <-t.C:
		case <-a.inst.kick:
		case ack := <-a.inst.syncs:
			acks = append(acks, ack)
		}
	}
}

// frideInstancesPass lets go of the FrIDEs whose instance's run is
// cleared, starts those that should run and do not (unless their wait
// is not over), and keeps a webhook listening for each instance that
// runs.
func (a *Agent) frideInstancesPass(runs map[string]*served, hooks map[string]*instanceHook, ended chan<- *frideServe, quit <-chan struct{}) {
	want := map[string]*FrideInstanceState{}
	for _, st := range listFrideInstances(a.Dir) {
		if st.Run {
			want[st.Name] = st
		}
	}
	for name, r := range runs {
		switch {
		case r.cur == nil && want[name] == nil:
			delete(runs, name)
		case r.cur != nil && want[name] != nil:
			// Run again before the exec ended: it is FrIDE's still.
			r.letGo = time.Time{}
		case r.cur != nil && r.letGo.IsZero():
			r.letGo = time.Now()
		case r.cur != nil && time.Since(r.letGo) > frideServeStopWait:
			// The exec outlived FrIDE's end: the agent ends it, not FrIDE.
			_ = r.cur.cmd.Process.Kill()
		}
	}
	for name, h := range hooks {
		if st := want[name]; st == nil || st.WebhookPort != h.port {
			_ = h.srv.Close()
			delete(hooks, name)
		}
	}
	for name, st := range want {
		if hooks[name] == nil {
			if h, err := a.listenInstanceHook(name, st.WebhookPort); err != nil {
				a.logf("node: the FrIDE instance %s's webhook cannot listen on %d, so its news does not reach the core: %v", name, st.WebhookPort, err)
			} else {
				hooks[name] = h
			}
		}
		r := runs[name]
		if r == nil {
			r = &served{backoff: frideServeMinBackoff}
			runs[name] = r
		}
		if r.cur != nil || time.Now().Before(r.next) {
			continue
		}
		cur, err := a.frideServeStart(st)
		if msg := errText(err); msg != r.startErr && msg != "" {
			a.logf("node: starting the FrIDE instance %s: %s", name, msg)
		}
		r.startErr = errText(err)
		if err != nil {
			a.inst.setProblem(name, err.Error())
			r.next, r.backoff = time.Now().Add(r.backoff), min(2*r.backoff, frideServeMaxBackoff)
			continue
		}
		r.cur = cur
		go func() {
			<-cur.done
			select {
			case ended <- cur:
			case <-quit:
			}
		}()
	}
}

// frideServeStart starts the instance's serve script's exec and returns
// it held.
func (a *Agent) frideServeStart(st *FrideInstanceState) (*frideServe, error) {
	w := &lineLogger{logf: a.logf, prefix: "node: the FrIDE instance " + st.Name + ": ", left: frideServeLogLines}
	args := append([]string{"exec", st.Sandbox}, frideInstanceScript(frideInstanceServe,
		strconv.Itoa(st.Port), strconv.Itoa(st.WebhookPort))...)
	cmd, err := a.sbx().start(nil, w, w, nil, args...)
	if err != nil {
		return nil, err
	}
	s := &frideServe{name: st.Name, cmd: cmd, done: make(chan struct{}), started: time.Now(), log: w}
	go func() {
		s.err = cmd.Wait()
		close(s.done)
	}()
	return s, nil
}

// instanceHook hears an instance's webhook.
type instanceHook struct {
	srv  *http.Server
	port int
}

// listenInstanceHook hears the instance name's FrIDE's news on the
// loopback's port.
func (a *Agent) listenInstanceHook(name string, port int) (*instanceHook, error) {
	ln, err := net.Listen("tcp", webhookAddr(port))
	if err != nil {
		return nil, err
	}
	signed := func(sig string, body []byte) bool {
		tok := readInstanceToken(a.Dir, name)
		return tok != "" && signedBy(tok, sig, body)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.frideNews(w, r, name, signed) }),
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logf("node: the FrIDE instance %s's webhook stopped: %v", name, err)
		}
	}()
	return &instanceHook{srv: srv, port: port}, nil
}

// errText is err's text, "" for none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// lineLogger logs what it is written line by line, up to left lines,
// and keeps the last.
type lineLogger struct {
	mu     sync.Mutex
	logf   func(string, ...any)
	prefix string
	buf    []byte
	left   int
	last   string
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > 4096 {
				l.buf = l.buf[:0]
			}
			return len(p), nil
		}
		if line := bytes.TrimSpace(l.buf[:i]); len(line) > 0 {
			l.last = firstLine(line)
			if l.left > 0 {
				l.left--
				l.logf("%s%s", l.prefix, l.last)
			}
		}
		l.buf = l.buf[i+1:]
	}
}

// lastLine is the last line written.
func (l *lineLogger) lastLine() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}
