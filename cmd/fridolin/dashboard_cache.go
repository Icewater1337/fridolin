package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/Icewater1337/fridolin/internal/httpapi"
	"net/http"
	"strings"
)

// writeJSONIfChanged writes v as writeJSON does, with an ETag of the body,
// and answers a request whose If-None-Match names that ETag with 304 and
// no body. The dashboard follows the activity feed and the brains every
// few seconds and sends back the ETag of what it has, so a poll that finds
// nothing new is not downloaded and compared again (app/resources.js).
func writeJSONIfChanged(w http.ResponseWriter, r *http.Request, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		httpapi.WriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	body = append(body, '\n') // as writeJSON's encoder ends it
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	if etagListed(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagListed reports whether an If-None-Match header names etag. That
// header compares weakly (RFC 9110, 13.1.2), so a W/ in front is ignored.
func etagListed(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		if t = strings.TrimSpace(t); t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}
