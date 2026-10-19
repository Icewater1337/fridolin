package main

// admit returns the queue after m arrives without mutating the supplied slice.
// It keeps the existing supersede and capacity rules; gone contains only work
// superseded by m. Chatter displaced solely for capacity has historically had
// no completion callback, so that behavior stays with this extraction.
// Fair turn ordering and current-run interruption are decided by the caller.
func admit(pending []Inbound, m Inbound, mode string, max int) (next, gone []Inbound, interrupting bool, err error) {
	interrupting, replaces := supersedes(m, mode)
	next = make([]Inbound, 0, len(pending)+1)
	for _, p := range pending {
		if replaces(p) {
			gone = append(gone, p)
		} else {
			next = append(next, p)
		}
	}
	if len(next) >= max {
		displaced := false
		for i, p := range next {
			if p.Silent {
				next = append(next[:i], next[i+1:]...)
				displaced = true
				break
			}
		}
		if !displaced {
			return next, gone, interrupting, errQueueFull
		}
	}
	return append(next, m), gone, interrupting, nil
}
