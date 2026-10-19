package app

// redactedError is an error whose text is redacted; it unwraps to the
// error it was made from.
type redactedError struct {
	text string
	err  error
}

func (e redactedError) Error() string { return e.text }

func (e redactedError) Unwrap() error { return e.err }
