package app

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

// runRecord is one run's process group, as saved while the run lasts.
type runRecord struct {
	Brain string `json:"brain"`
	PGID  int    `json:"pgid"`
	// Start is when the group's leader started (procStart), and Boot which
	// boot of the machine that was (bootID): a PID is reused, so a group
	// is taken for the run's only when its leader is still the same
	// process.
	Start string `json:"start"`
	Boot  string `json:"boot"`
}

// runsDir holds the records of the runs going on.
func runsDir(dataDir string) string { return filepath.Join(dataDir, "runs") }

// recordRun saves the run of brain whose process group pid leads. It
// returns the record's file, to remove when the run is over, or "" when
// none could be saved: the run goes ahead anyway, it only cannot be stopped
// after a crash then.
func recordRun(dataDir, brain string, pid int) string {
	if dataDir == "" {
		return "" // a brain made by hand, in a test
	}
	start, err := procStart(pid)
	if err == nil {
		var boot string
		if boot, err = bootID(); err == nil {
			var data []byte
			data, err = json.Marshal(runRecord{Brain: brain, PGID: pid, Start: start, Boot: boot})
			if err == nil {
				if err = os.MkdirAll(runsDir(dataDir), 0o700); err == nil {
					p := filepath.Join(runsDir(dataDir), strconv.Itoa(pid)+".json")
					if err = fsx.WriteFileAtomic(p, data, 0o600, false); err == nil {
						return p
					}
				}
			}
		}
	}
	log.Printf("brain %s: could not record run %d, so a crash of the bridge would leave it running: %v", brain, pid, err)
	return ""
}

// reapGrace is how long reapOrphanedRuns gives a group to end after SIGTERM
// before it kills it.
var reapGrace = 3 * time.Second

// reapOrphanedRuns stops the process groups of the runs a bridge that is
// gone left behind, and forgets every record. Call it while holding the
// instance lock and before any brain runs, so that each record is from a
// bridge that is not running any more.
func reapOrphanedRuns(dataDir string) {
	entries, err := os.ReadDir(runsDir(dataDir))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("reading %s: %v", runsDir(dataDir), err)
		}
		return
	}
	boot, _ := bootID()
	for _, e := range entries {
		p := filepath.Join(runsDir(dataDir), e.Name())
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var rec runRecord
		data, err := os.ReadFile(p)
		if err == nil {
			err = json.Unmarshal(data, &rec)
		}
		if err == nil && boot != "" && rec.Boot == boot && rec.PGID > 1 && runIsOurs(rec) {
			log.Printf("brain %s: stopping run %d, which the last bridge left running", rec.Brain, rec.PGID)
			stopGroup(rec.PGID)
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("removing %s: %v", p, err)
		}
	}
}
