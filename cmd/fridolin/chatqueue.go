package main

import (
	"context"
	"errors"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// whatsmeow hands the bridge every event on one goroutine and waits for
// each before it starts the next. Work done there, a download or a query
// to WhatsApp, held up every later event in every chat; and a handler
// started on a goroutine of its own for each message let a chat's messages
// overtake each other, so a photo and "what is this?" could merge in the
// wrong order, and !stop could run before the message it was to stop. So
// the event goroutine only puts each message, edit, deletion and poll vote
// on its chat's queue, and the chat's worker handles them in the order
// they came, apart from every other chat's.

// chatQueueLimit is how many events one chat's queue holds besides the one
// its worker is on. What a chat sends beyond that, a flood from a group
// the bridge does not read, say, is dropped rather than kept in memory.
const chatQueueLimit = 256

// chatDropLogEvery is how often a chat whose queue stays full is logged.
const chatDropLogEvery = time.Minute

// errChatQueueFull and errIntakeClosed are why chatQueues.add dropped a
// job.
var (
	errChatQueueFull = errors.New("the chat's queue is full")
	errIntakeClosed  = errors.New("the chat queues are closed")
)

// chatQueues runs jobs one after another per chat, each chat on a worker
// of its own that starts with the chat's first job and ends once the chat
// has none left. Its mask function is supplied before accepting jobs.
type chatQueues struct {
	mask   func(string) string
	mu     sync.Mutex
	chats  map[string]*chatQueue // the chats whose worker runs
	closed bool
	wg     sync.WaitGroup // one per worker
}

// chatQueue is one chat's jobs waiting, oldest first.
type chatQueue struct {
	jobs []func()
	// dropped counts the jobs dropped since loggedAt, when the last line
	// about them was logged.
	dropped  int
	loggedAt time.Time
}

// add puts job on chat's queue, and starts the chat's worker unless it
// runs. It is errChatQueueFull for a chat whose queue is full, and
// errIntakeClosed once close was called: job is dropped then.
func (q *chatQueues) add(chat string, job func()) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return errIntakeClosed
	}
	c := q.chats[chat]
	if c == nil {
		if q.chats == nil {
			q.chats = map[string]*chatQueue{}
		}
		c = &chatQueue{}
		q.chats[chat] = c
		q.wg.Add(1)
		go q.work(chat, c)
	}
	if len(c.jobs) >= chatQueueLimit {
		c.drop(chat, q.mask)
		return errChatQueueFull
	}
	c.jobs = append(c.jobs, job)
	return nil
}

// drop counts a job dropped from c, chat's full queue, and logs it: the
// first of a flood at once, then once every chatDropLogEvery with how many
// more went, so that a flood does not flood the log too.
func (c *chatQueue) drop(chat string, mask func(string) string) {
	c.dropped++
	switch {
	case c.loggedAt.IsZero():
		log.Printf("WhatsApp: %s sent more than %d messages the bridge has not handled yet; dropping what it sends until it catches up", mask(chat), chatQueueLimit)
	case time.Since(c.loggedAt) >= chatDropLogEvery:
		log.Printf("WhatsApp: dropped %d more messages from %s, whose queue is still full", c.dropped, mask(chat))
	default:
		return
	}
	c.loggedAt, c.dropped = time.Now(), 0
}

// work runs c's jobs until none is left, and then ends; the chat's next
// job starts a new worker. A job that panics is logged and the next one
// runs, as whatsmeow does with an event handler that panics.
func (q *chatQueues) work(chat string, c *chatQueue) {
	defer q.wg.Done()
	for {
		q.mu.Lock()
		if len(c.jobs) == 0 {
			delete(q.chats, chat)
			q.mu.Unlock()
			return
		}
		job := c.jobs[0]
		c.jobs[0] = nil
		c.jobs = c.jobs[1:]
		q.mu.Unlock()
		runJob(chat, job, q.mask)
	}
}

// runJob runs one of chat's jobs, and logs a panic in it.
func runJob(chat string, job func(), mask func(string) string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WhatsApp: handling a message from %s panicked: %v\n%s", mask(chat), r, debug.Stack())
		}
	}()
	job()
}

// close stops taking jobs and joins every worker after it hands over the
// accepted jobs. Network work is cancelled before this call. A slow local
// handler warns after warnAfter, but cannot make us discard its queue or
// return while a worker may still use the store that shutdown closes next.
func (q *chatQueues) close(warnAfter time.Duration) {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(warnAfter)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		log.Printf("WhatsApp: still draining accepted messages after %v; waiting before closing the store", warnAfter)
	}
	<-done
}

// maxDownloads is how many attachments are downloaded at once, all chats
// together. A chat downloads one at a time anyway, as its worker handles
// one message at a time; this bounds what many chats sending files at
// once take of memory, disk and bandwidth.
const maxDownloads = 3

// downloadSlots limits the downloads running at once to maxDownloads. The
// zero value is ready.
type downloadSlots struct {
	once  sync.Once
	slots chan struct{}
}

// acquire waits for a free slot, and returns the function that frees it.
// ok is false when ctx ended first: there is no slot to free then.
func (s *downloadSlots) acquire(ctx context.Context) (release func(), ok bool) {
	s.once.Do(func() { s.slots = make(chan struct{}, maxDownloads) })
	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, true
	case <-ctx.Done():
		return nil, false
	}
}
