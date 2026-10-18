package main

import (
	"errors"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
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
// has none left. The zero value is ready.
type chatQueues struct {
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
		c.drop(chat)
		return errChatQueueFull
	}
	c.jobs = append(c.jobs, job)
	return nil
}

// drop counts a job dropped from c, chat's full queue, and logs it: the
// first of a flood at once, then once every chatDropLogEvery with how many
// more went, so that a flood does not flood the log too.
func (c *chatQueue) drop(chat string) {
	c.dropped++
	switch {
	case c.loggedAt.IsZero():
		log.Printf("WhatsApp: %s sent more than %d messages the bridge has not handled yet; dropping what it sends until it catches up", maskChatJID(chat), chatQueueLimit)
	case time.Since(c.loggedAt) >= chatDropLogEvery:
		log.Printf("WhatsApp: dropped %d more messages from %s, whose queue is still full", c.dropped, maskChatJID(chat))
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
		runJob(chat, job)
	}
}

// runJob runs one of chat's jobs, and logs a panic in it.
func runJob(chat string, job func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("WhatsApp: handling a message from %s panicked: %v\n%s", maskChatJID(chat), r, debug.Stack())
		}
	}()
	job()
}

// close stops taking jobs and waits up to wait for the workers to run
// those they hold. It returns how many were left when the wait ended:
// those are dropped, and the workers end after the job they are on.
func (q *chatQueues) close(wait time.Duration) (left int) {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		return 0
	case <-timer.C:
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, c := range q.chats {
		left += len(c.jobs)
		c.jobs = nil
	}
	return left
}

// maskChatJID is chat, a queue's JID, for the log: a group's as the rest
// of the log names it, a person's masked as a sender is.
func maskChatJID(chat string) string {
	jid, err := types.ParseJID(chat)
	switch {
	case err != nil:
		return maskDigits(chat, 0)
	case jid.Server == types.GroupServer:
		return chat
	}
	return maskJID(jid.User, "", jid.Server)
}
