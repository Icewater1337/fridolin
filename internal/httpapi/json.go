package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// errNoBody answers a request that left out the body its endpoint needs.
var errNoBody = errors.New("a JSON body is required")

// DecodeJSON decodes the request's JSON body into v, strictly: a key the
// endpoint does not know and anything after the first value are errors that
// name what is wrong, rather than a typo silently dropped or a request that
// runs with Go's zero values. An empty body is errNoBody, sent chunked or
// not.
func DecodeJSON(r *http.Request, v any) error { return decodeBody(r, v, false) }

// DecodeOptionalJSON is DecodeJSON for an endpoint whose body may be left
// out, such as stopping a run: an empty body leaves v as it is.
func DecodeOptionalJSON(r *http.Request, v any) error { return decodeBody(r, v, true) }

// DecodeStrict decodes data, one JSON value, as strictly as DecodeJSON.
func DecodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func decodeBody(r *http.Request, v any, optional bool) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			if optional {
				return nil
			}
			return errNoBody
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON: the body must be a single JSON value")
	}
	return nil
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteErr(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, ErrorResponse{Error: msg})
}
