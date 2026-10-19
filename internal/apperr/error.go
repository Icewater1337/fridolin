// Package apperr classifies application errors without changing their text.
package apperr

import "errors"

type Kind uint8

const (
	Unknown Kind = iota
	NotFound
	Invalid
	Conflict
	Busy
	Precondition
	Unavailable
	TooLarge
	BadJSON
)

type Error struct {
	Kind    Kind
	Message string
	cause   error
}

func (e *Error) Error() string            { return e.Message }
func (e *Error) Unwrap() error            { return e.cause }
func New(kind Kind, message string) error { return &Error{Kind: kind, Message: message} }
func Wrap(kind Kind, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Message: err.Error(), cause: err}
}
func KindOf(err error) Kind {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Kind
	}
	return Unknown
}
