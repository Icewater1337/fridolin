package main

// observeRuns reports whether m, untagged group chatter, gets a run of its
// own in a brain that observes with mode. Junk (a bare emoji, "ok") never
// does, but stays in the history like the rest: a "yes" may answer what
// the brain asked.
func observeRuns(mode string, m Inbound) bool {
	return mode == observeRun && (len(m.Media) > 0 || !isJunkMessage(m.Text))
}
