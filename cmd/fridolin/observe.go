package main

// What a brain does with untagged group messages, the ones nobody
// addressed to it (Inbound.Silent): BrainManifest.Observe. Either way the
// message goes into the brain's history first, and nothing is ever sent
// back for it.
const (
	// observeHistory, the default, keeps them in the history only. The
	// next run in that chat reads them there, which is all most brains
	// need, and chatter costs no run and holds up nobody's request.
	observeHistory = "history"
	// observeRun also gives each one an observe-only run, for a brain that
	// keeps notes on what it reads. Each costs a full Claude run, with
	// tools, on text anyone in the group may have written.
	observeRun = "run"
)

// validObserve are the values brain.json may give observe.
var validObserve = map[string]bool{"": true, observeHistory: true, observeRun: true}

// observeRuns reports whether m, untagged group chatter, gets a run of its
// own in a brain that observes with mode. Junk (a bare emoji, "ok") never
// does, but stays in the history like the rest: a "yes" may answer what
// the brain asked.
func observeRuns(mode string, m Inbound) bool {
	return mode == observeRun && (len(m.Media) > 0 || !isJunkMessage(m.Text))
}
