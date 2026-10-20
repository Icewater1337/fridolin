package app

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// auditDirName is the data dir's folder of audit logs: one JSON line per
// event, a file per month (YYYY-MM.jsonl).
const auditDirName = "audit"

// auditEntry is one line of the audit log. Fields are added, never
// renamed; a reader ignores the ones it does not know.
type auditEntry struct {
	TS   string `json:"ts"`
	Kind string `json:"kind"` // "delegate"
	From string `json:"from"` // the brain that acted
	To   string `json:"to,omitempty"`
	// Run is the run of From that acted, ID the event's own id (the
	// delegated task's).
	Run        string `json:"run,omitempty"`
	ID         string `json:"id,omitempty"`
	Task       string `json:"task,omitempty"` // shortened
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"duration_ms"`
}

// auditLog appends to <data_dir>/audit/YYYY-MM.jsonl.
type auditLog struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
}

func (a *auditLog) append(e auditEntry) {
	if a == nil || a.dir == "" {
		return
	}
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	t := now()
	e.TS = t.Format(time.RFC3339)
	line, err := json.Marshal(e)
	if err != nil {
		log.Printf("audit: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		log.Printf("audit: %v", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(a.dir, t.Format("2006-01")+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("audit: %v", err)
	}
}
