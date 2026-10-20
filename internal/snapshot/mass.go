package snapshot

// The thresholds of MassDeletion, the same as the Drive backup's
// (internal/backup, maxDeletions), so that both refuse alike.
const (
	massDeletionShare    = 5   // more than 1/5 of the files gone…
	massDeletionMinFiles = 10  // …once at least this many are, looks like a wipe
	massDeletionMaxFiles = 250 // this many gone looks like a wipe however many there were
)

// MassDeletion reports whether going from prev to cur files looks like a
// wipe rather than tidying up: more than half of the files gone, however
// few there were; more than a fifth, once that is at least ten; or 250 or
// more. A snapshot taken after a wipe would be the one retention keeps,
// so the caller stops before it rather than after.
func MassDeletion(prev, cur int) bool {
	gone := prev - cur
	return gone > min(prev/2, max(massDeletionMinFiles-1, prev/massDeletionShare), massDeletionMaxFiles-1)
}
