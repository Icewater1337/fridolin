// Package ghapptest is a fake of the parts of GitHub's API that ghapp
// uses, for tests: no test reaches GitHub.
package ghapptest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Code is the manifest code the fake converts.
const Code = "fakecode123"

// Server is a fake GitHub with one app installed on one account.
type Server struct {
	*httptest.Server
	Key *rsa.PrivateKey
	// AppID, Slug and Account describe the app and where it is installed.
	AppID          int64
	Slug, Account  string
	InstallationID int64
	Repos          []string // owner/name, what the installation reaches

	mu sync.Mutex
	// Manifest is the last manifest the new-app page took, and setupURL
	// where the install page sends the browser back.
	Manifest map[string]any
	setupURL string
	n        int
	live    map[string]Minted
	Minted  []Minted // every token made, in order
	Revoked []string // tokens revoked
}

// Minted is one token the fake made, and what it was asked for.
type Minted struct {
	Token       string
	Repos       []string // names asked for, without the owner
	Permissions map[string]string
}

// New starts a fake. Close it with t.Cleanup(s.Close).
func New() *Server {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	s := &Server{Key: key, AppID: 4242, Slug: "fridolin-test", Account: "you", InstallationID: 77,
		Repos: []string{"you/fridolin", "you/fride"}, live: map[string]Minted{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// PEM is the app's private key, as GitHub hands it over.
func (s *Server) PEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(s.Key)}))
}

// Live is the tokens made and not revoked.
func (s *Server) Live() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for t := range s.live {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// LastMinted is the newest token's record, zero without one.
func (s *Server) LastMinted() Minted {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.Minted) == 0 {
		return Minted{}
	}
	return s.Minted[len(s.Minted)-1]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// checkJWT verifies an app JWT: RS256 with the app's key, iss the app id,
// not expired.
func (s *Server) checkJWT(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&s.Key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var c struct {
		Iss string `json:"iss"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return false
	}
	return c.Iss == strconv.FormatInt(s.AppID, 10) && time.Unix(c.Exp, 0).After(time.Now())
}

// installationToken is the live token a request carries, if any.
func (s *Server) installationToken(r *http.Request) (Minted, bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.live[tok]
	return m, ok
}

func (m Minted) reaches(full string) bool {
	_, name, _ := strings.Cut(full, "/")
	return len(m.Repos) == 0 || slices.Contains(m.Repos, name)
}

// LastManifest is what the new-app page was posted, nil before.
func (s *Server) LastManifest() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Manifest
}

// web plays github.com's pages a browser goes through: the new-app page
// takes the manifest form and, as if the owner clicked Create, sends the
// browser to its redirect_url with the code; the install page sends it to
// the setup_url, as if the owner clicked Install.
func (s *Server) web(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	switch {
	case r.Method == "POST" && (p == "/settings/apps/new" || strings.HasPrefix(p, "/organizations/") && strings.HasSuffix(p, "/settings/apps/new")):
		var m map[string]any
		if err := json.Unmarshal([]byte(r.PostFormValue("manifest")), &m); err != nil {
			http.Error(w, "no manifest", http.StatusUnprocessableEntity)
			return true
		}
		redirect, _ := m["redirect_url"].(string)
		if _, ok := m["url"].(string); !ok || redirect == "" {
			http.Error(w, "the manifest needs url and redirect_url", http.StatusUnprocessableEntity)
			return true
		}
		s.mu.Lock()
		s.Manifest = m
		s.setupURL, _ = m["setup_url"].(string)
		s.mu.Unlock()
		http.Redirect(w, r, redirect+"?code="+Code+"&state="+r.URL.Query().Get("state"), http.StatusFound)
	case r.Method == "GET" && p == "/apps/"+s.Slug+"/installations/new":
		s.mu.Lock()
		setup := s.setupURL
		s.mu.Unlock()
		if setup == "" {
			http.Error(w, "no setup URL", http.StatusNotFound)
			return true
		}
		http.Redirect(w, r, setup+"?installation_id="+strconv.FormatInt(s.InstallationID, 10)+"&setup_action=install", http.StatusFound)
	default:
		return false
	}
	return true
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.web(w, r) {
		return
	}
	switch p := r.URL.Path; {
	case r.Method == "POST" && strings.HasPrefix(p, "/app-manifests/") && strings.HasSuffix(p, "/conversions"):
		if strings.TrimSuffix(strings.TrimPrefix(p, "/app-manifests/"), "/conversions") != Code {
			fail(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"id": s.AppID, "slug": s.Slug, "name": s.Slug, "html_url": s.URL + "/apps/" + s.Slug,
			"owner":     map[string]string{"login": s.Account, "type": "User"},
			"client_id": "Iv1.fakeclient", "client_secret": "fake-client-secret", "webhook_secret": nil, "pem": s.PEM(),
		})
	case r.Method == "GET" && p == "/app/installations":
		if !s.checkJWT(r) {
			fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{{
			"id": s.InstallationID, "account": map[string]string{"login": s.Account, "type": "User"},
			"html_url": s.URL + "/settings/installations/" + strconv.FormatInt(s.InstallationID, 10), "repository_selection": "selected",
			"permissions": map[string]string{"contents": "read", "issues": "write", "metadata": "read"},
		}})
	case r.Method == "POST" && p == "/app/installations/"+strconv.FormatInt(s.InstallationID, 10)+"/access_tokens":
		if !s.checkJWT(r) {
			fail(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
			return
		}
		var req struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				fail(w, http.StatusBadRequest, "Problems parsing JSON")
				return
			}
		}
		var repos []map[string]string
		for _, name := range req.Repositories {
			full := s.Account + "/" + name
			if !slices.Contains(s.Repos, full) {
				fail(w, http.StatusUnprocessableEntity, "There is at least one repository that does not exist or is not accessible to the parent installation.")
				return
			}
			repos = append(repos, map[string]string{"full_name": full})
		}
		s.mu.Lock()
		s.n++
		m := Minted{Token: fmt.Sprintf("ghs_fake%04d", s.n), Repos: req.Repositories, Permissions: req.Permissions}
		s.live[m.Token] = m
		s.Minted = append(s.Minted, m)
		s.mu.Unlock()
		perms := req.Permissions
		if perms == nil {
			perms = map[string]string{"contents": "read", "issues": "write", "metadata": "read"}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": m.Token, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"permissions": perms, "repositories": repos})
	case r.Method == "DELETE" && p == "/installation/token":
		m, ok := s.installationToken(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		s.mu.Lock()
		delete(s.live, m.Token)
		s.Revoked = append(s.Revoked, m.Token)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && p == "/installation/repositories":
		m, ok := s.installationToken(r)
		if !ok {
			fail(w, http.StatusUnauthorized, "Bad credentials")
			return
		}
		var repos []map[string]string
		for _, full := range s.Repos {
			if m.reaches(full) {
				repos = append(repos, map[string]string{"full_name": full})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": repos})
	case r.Method == "GET" && strings.HasPrefix(p, "/repos/") && strings.HasSuffix(p, "/issues"):
		m, ok := s.installationToken(r)
		full := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/"), "/issues")
		if !ok || !m.reaches(full) || m.Permissions != nil && m.Permissions["issues"] == "" {
			fail(w, http.StatusNotFound, "Not Found")
			return
		}
		writeJSON(w, http.StatusOK, []map[string]any{{"number": 7, "title": "A test issue", "html_url": s.URL + "/" + full + "/issues/7"}})
	default:
		fail(w, http.StatusNotFound, "Not Found")
	}
}
