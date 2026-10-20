package httpapi

import (
	"github.com/Icewater1337/fridolin/internal/apperr"
	"net/http"
)

// statusFor is the HTTP interpretation of service error kinds. Endpoints with
// an older, intentionally different contract translate that kind at the edge.
func statusFor(kind apperr.Kind) int {
	switch kind {
	case apperr.NotFound:
		return http.StatusNotFound
	case apperr.Invalid:
		return http.StatusUnprocessableEntity
	case apperr.Conflict, apperr.Busy:
		return http.StatusConflict
	case apperr.Precondition:
		return http.StatusPreconditionFailed
	case apperr.Unavailable:
		return http.StatusServiceUnavailable
	case apperr.TooLarge:
		return http.StatusRequestEntityTooLarge
	case apperr.Forbidden:
		return http.StatusForbidden
	case apperr.BadJSON:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
