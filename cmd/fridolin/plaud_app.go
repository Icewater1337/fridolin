package main

// The Plaud Sync phone app (plaud-android/) uploading transcripts through
// the site (plaud.phone_upload; docs/PLAUD.md, "Through your site").
//
// The dashboard builds the app with the site's address and a one-time
// pairing code. On its first start the app makes a key pair in the phone's
// keystore, which never lets the private key out, and pairs: it sends the
// public key and the code in a JWT signed with that key to the site, which
// passes it on through its spool (site_ingest.go). The bridge checks the
// code and keeps the key. Each transcript the app uploads then comes with
// a JWT (ES256) it signed, naming the phone, the recording and the
// SHA-256 of the transcript. The site checks it to turn junk away early,
// but the bridge checks it again: module code runs in the site's process,
// so the site's word is no proof. A transcript that passes goes into the
// folder the poller reads phone recordings from (plaudPhoneDir).
//
// What Fridolin keeps: the phones' public keys (<data_dir>/plaud/
// devices.json, and a copy for the site in <data_dir>/site/devices.json),
// the pairing codes' hashes, and Plaud's developer keys the app is built
// with (<data_dir>/plaud/developer.json). None of it lets anyone upload as
// a phone.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/fsx"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	// plaudPairingLifetime is how long a built app's pairing code works.
	plaudPairingLifetime = 24 * time.Hour
	// The audiences of the app's two JWTs, so neither passes for the other.
	plaudPairAud       = "fridolin:pair"
	plaudTranscriptAud = "fridolin:plaud-transcript"
	// A JWT may live this long, and clocks may be this far apart.
	plaudJWTMaxAge     = 10 * time.Minute
	plaudClockSkew     = time.Minute
	plaudTranscriptMax = 512 << 10
)

var errPhoneUploadOff = errors.New("uploads from the phone app are off (plaud.phone_upload)")

// plaudJWK is a phone's public key, an EC P-256 JSON Web Key.
type plaudJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k plaudJWK) publicKey() (*ecdsa.PublicKey, error) {
	if k.Kty != "EC" || k.Crv != "P-256" {
		return nil, errors.New("the key is not an EC P-256 key")
	}
	x, errX := base64.RawURLEncoding.DecodeString(k.X)
	y, errY := base64.RawURLEncoding.DecodeString(k.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, errors.New("the key's coordinates are not 32 bytes each")
	}
	// It refuses a point that is not on the curve.
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
}

// plaudClaims is what the app's JWTs say: iss is the phone, code and key
// pair it, name and sha256 say which transcript an upload carries.
type plaudClaims struct {
	Iss    string    `json:"iss,omitempty"`
	Aud    string    `json:"aud"`
	Iat    int64     `json:"iat"`
	Exp    int64     `json:"exp"`
	Code   string    `json:"code,omitempty"`
	Key    *plaudJWK `json:"key,omitempty"`
	Name   string    `json:"name,omitempty"`
	SHA256 string    `json:"sha256,omitempty"`
}

// parsePlaudJWT reads the claims of a compact ES256 JWS without checking
// its signature, which needs the key the claims name.
func parsePlaudJWT(tok string) (plaudClaims, error) {
	var c plaudClaims
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return c, errors.New("the assertion is not a JWT")
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(h, &hdr) != nil || hdr.Alg != "ES256" {
		return c, errors.New("the assertion is not signed with ES256")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return c, errors.New("the assertion's claims are not JSON")
	}
	return c, nil
}

// verifyPlaudJWT checks that key signed tok, for aud, and that it is
// current at now; it returns the claims.
func verifyPlaudJWT(tok string, key plaudJWK, aud string, now time.Time) (plaudClaims, error) {
	c, err := parsePlaudJWT(tok)
	if err != nil {
		return c, err
	}
	pub, err := key.publicKey()
	if err != nil {
		return c, err
	}
	i := strings.LastIndexByte(tok, '.')
	sig, err := base64.RawURLEncoding.DecodeString(tok[i+1:])
	sum := sha256.Sum256([]byte(tok[:i]))
	if err != nil || len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return c, errors.New("the signature does not match")
	}
	iat, exp := time.Unix(c.Iat, 0), time.Unix(c.Exp, 0)
	switch {
	case c.Aud != aud:
		return c, errors.New("the assertion is for something else")
	case exp.Before(now.Add(-plaudClockSkew)), iat.After(now.Add(plaudClockSkew)), exp.Sub(iat) > plaudJWTMaxAge:
		return c, errors.New("the assertion is not current: is the phone's clock right?")
	}
	return c, nil
}

// ── paired phones ────────────────────────────────────────────────────────

// plaudDevice is a paired phone.
type plaudDevice struct {
	ID         string    `json:"id"`
	Key        plaudJWK  `json:"key"`
	Paired     time.Time `json:"paired"`
	LastUpload time.Time `json:"last_upload,omitzero"`
}

// plaudPairing is a pairing code a built app carries: its SHA-256, hex.
type plaudPairing struct {
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
}

// plaudDevices is <data_dir>/plaud/devices.json.
type plaudDevices struct {
	Devices  []plaudDevice  `json:"devices"`
	Pairings []plaudPairing `json:"pairings,omitempty"`
}

func (p *PlaudManager) devicesPath() string { return filepath.Join(p.dir(), "devices.json") }

func (p *PlaudManager) loadDevices() (plaudDevices, error) {
	var st plaudDevices
	data, err := os.ReadFile(p.devicesPath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(data, &st)
}

// updateDevices changes devices.json with fn, drops the pairing codes that
// expired and rewrites the site's copy, which has the ids and public keys
// only. An error from fn saves nothing.
func (p *PlaudManager) updateDevices(fn func(st *plaudDevices) error) error {
	p.devMu.Lock()
	defer p.devMu.Unlock()
	st, err := p.loadDevices()
	if err != nil {
		return err
	}
	if err := fn(&st); err != nil {
		return err
	}
	now := time.Now()
	st.Pairings = slices.DeleteFunc(st.Pairings, func(pp plaudPairing) bool { return now.After(pp.Expires) })
	if err := os.MkdirAll(p.dir(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	if err := fsx.WriteFileAtomic(p.devicesPath(), data, 0o600, false); err != nil {
		return err
	}
	type siteDevice struct {
		ID  string   `json:"id"`
		Key plaudJWK `json:"key"`
	}
	site := struct {
		Devices []siteDevice `json:"devices"`
	}{Devices: []siteDevice{}}
	for _, d := range st.Devices {
		site.Devices = append(site.Devices, siteDevice{d.ID, d.Key})
	}
	sitePath := filepath.Join(p.cfg().DataDir, "site", "devices.json")
	if err := os.MkdirAll(filepath.Dir(sitePath), 0o700); err != nil {
		return err
	}
	data, _ = json.Marshal(site)
	return fsx.WriteFileAtomic(sitePath, data, 0o600, false)
}

// newPairing makes a pairing code for the next build of the app.
func (p *PlaudManager) newPairing() (string, time.Time, error) {
	code := randomToken(18)
	exp := time.Now().Add(plaudPairingLifetime)
	sum := sha256.Sum256([]byte(code))
	return code, exp, p.updateDevices(func(st *plaudDevices) error {
		st.Pairings = append(st.Pairings, plaudPairing{Hash: hex.EncodeToString(sum[:]), Expires: exp})
		return nil
	})
}

// RevokeDevice unpairs phone id: its uploads are refused from now on.
func (p *PlaudManager) RevokeDevice(id string) error {
	err := p.updateDevices(func(st *plaudDevices) error {
		n := len(st.Devices)
		st.Devices = slices.DeleteFunc(st.Devices, func(d plaudDevice) bool { return d.ID == id })
		if len(st.Devices) == n {
			return errPlaudUnknownDevice
		}
		return nil
	})
	if err == nil {
		log.Printf("plaud: phone %s unpaired on the dashboard", id)
	}
	return err
}

var errPlaudUnknownDevice = errors.New("there is no such paired phone")

// deviceRequest answers a phone's request that the site passed on:
// "pair", or "transcript" with the transcript in body (site_ingest.go).
// Its errors go back to the phone, so they name what is wrong and nothing
// the request carried.
func (p *PlaudManager) deviceRequest(op, assertion string, body []byte) (map[string]any, error) {
	switch op {
	case "pair":
		id, err := p.pairDevice(assertion, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"device": id}, nil
	case "transcript":
		return nil, p.takeTranscript(assertion, body, time.Now())
	}
	return nil, fmt.Errorf("unknown request %q", op)
}

// pairDevice keeps the key that signed assertion, a pairing request with a
// code that works: it proves the phone holds the key's private half.
func (p *PlaudManager) pairDevice(assertion string, now time.Time) (string, error) {
	if !p.cfg().Plaud.PhoneUpload {
		return "", errPhoneUploadOff
	}
	c, err := parsePlaudJWT(assertion)
	if err != nil {
		return "", err
	}
	if c.Key == nil {
		return "", errors.New("the pairing request has no key")
	}
	if c, err = verifyPlaudJWT(assertion, *c.Key, plaudPairAud, now); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(c.Code))
	hash := hex.EncodeToString(sum[:])
	id := "dev_" + randomToken(9)
	err = p.updateDevices(func(st *plaudDevices) error {
		i := slices.IndexFunc(st.Pairings, func(pp plaudPairing) bool {
			return subtle.ConstantTimeCompare([]byte(pp.Hash), []byte(hash)) == 1 && now.Before(pp.Expires)
		})
		if c.Code == "" || i < 0 {
			return errors.New("the pairing code does not work: it was used or expired. Build the app again on the dashboard")
		}
		st.Pairings = slices.Delete(st.Pairings, i, i+1)
		st.Devices = append(st.Devices, plaudDevice{ID: id, Key: *c.Key, Paired: now})
		return nil
	})
	if err != nil {
		return "", err
	}
	log.Printf("plaud: a phone paired as %s", id)
	return id, nil
}

// takeTranscript puts an uploaded transcript where the poller reads phone
// recordings, once its JWT shows a paired phone signed this very text.
func (p *PlaudManager) takeTranscript(assertion string, body []byte, now time.Time) error {
	cfg := p.cfg()
	if !cfg.Plaud.PhoneUpload {
		return errPhoneUploadOff
	}
	c, err := parsePlaudJWT(assertion)
	if err != nil {
		return err
	}
	st, err := p.loadDevices()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(st.Devices, func(d plaudDevice) bool { return d.ID == c.Iss })
	if c.Iss == "" || i < 0 {
		return errors.New("this phone is not paired, or was unpaired")
	}
	if c, err = verifyPlaudJWT(assertion, st.Devices[i].Key, plaudTranscriptAud, now); err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	if c.SHA256 != base64.RawURLEncoding.EncodeToString(sum[:]) {
		return errors.New("the signature is for another transcript")
	}
	// The name becomes a file name: only the app's own form goes.
	if _, err := time.ParseInLocation(plaudPhoneLayout, c.Name, time.Local); err != nil {
		return errors.New("the transcript's name is not a recording's start time")
	}
	if len(body) > plaudTranscriptMax {
		return errors.New("the transcript is too long")
	}
	dir := plaudPhoneDir(cfg)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The same upload again (a retry whose answer got lost) rewrites the
	// same file, which the poller has seen already.
	if err := fsx.WriteFileAtomic(filepath.Join(dir, c.Name+".txt"), body, 0o600, false); err != nil {
		return err
	}
	_ = p.updateDevices(func(st *plaudDevices) error {
		for i := range st.Devices {
			if st.Devices[i].ID == c.Iss {
				st.Devices[i].LastUpload = now
			}
		}
		return nil
	})
	log.Printf("plaud %s%s: uploaded by phone %s", plaudPhonePrefix, c.Name, c.Iss)
	// Handed over now rather than at the next check.
	if cfg.Plaud.Enabled {
		if _, err := p.CheckNow(); err != nil {
			log.Printf("plaud: %v", err)
		}
	}
	return nil
}

// ── building the app ─────────────────────────────────────────────────────

// plaudDeveloper is the Plaud developer app the phone app is built with
// (portal.plaud.ai): <data_dir>/plaud/developer.json. The dashboard only
// learns whether it is there.
type plaudDeveloper struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	APIKey       string `json:"api_key"`
}

func (d plaudDeveloper) complete() bool {
	return d.ClientID != "" && d.ClientSecret != "" && d.APIKey != ""
}

func (p *PlaudManager) developerPath() string { return filepath.Join(p.dir(), "developer.json") }

func (p *PlaudManager) loadDeveloper() plaudDeveloper {
	var d plaudDeveloper
	if data, err := os.ReadFile(p.developerPath()); err == nil {
		_ = json.Unmarshal(data, &d)
	}
	return d
}

// SaveDeveloper keeps the keys of d that are set; the others stay.
func (p *PlaudManager) SaveDeveloper(d plaudDeveloper) error {
	old := p.loadDeveloper()
	for _, f := range []struct{ in, out *string }{{&d.ClientID, &old.ClientID}, {&d.ClientSecret, &old.ClientSecret}, {&d.APIKey, &old.APIKey}} {
		v := strings.TrimSpace(*f.in)
		if v == "" {
			continue
		}
		// They go into a Java properties file and the app's code.
		if strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r > '~' || r == '\\' || r == '"' }) {
			return errors.New("a key has a character no Plaud key has")
		}
		*f.out = v
	}
	if err := os.MkdirAll(p.dir(), 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(old)
	return fsx.WriteFileAtomic(p.developerPath(), data, 0o600, false)
}

// plaudAppJob is the dashboard's build of the phone app.
type plaudAppJob struct {
	State    string    `json:"state"` // "running", "done" or "error"
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	Error    string    `json:"error,omitempty"`
	// Log is the end of the build's output, when it failed.
	Log string `json:"log,omitempty"`
	// PairingExpires is when the built app's pairing code stops working.
	PairingExpires time.Time `json:"pairing_expires,omitzero"`
}

var errPlaudAppBuilding = errors.New("the phone app is being built already")

func (p *PlaudManager) appDir() string  { return filepath.Join(p.dir(), "app") }
func (p *PlaudManager) appFile() string { return filepath.Join(p.appDir(), "plaud-sync.apk") }

// BuildApp starts building the phone app in the background, with the
// site's address and a new pairing code: Gradle, in plaud-android/ of the
// checkout Fridolin was installed from.
func (p *PlaudManager) BuildApp() error {
	select {
	case <-p.lifetime().Done():
		return errPlaudStopping
	default:
	}
	if !p.cfg().Plaud.PhoneUpload {
		return errPhoneUploadOff
	}
	dev := p.loadDeveloper()
	if !dev.complete() {
		return errors.New("enter the Plaud developer app's client ID, client secret and API key first")
	}
	site := ""
	if p.siteURL != nil {
		site = p.siteURL()
	}
	if site == "" {
		return errors.New("the site has no domain yet (Site → Domain): the app uploads through it")
	}
	src := filepath.Join(ownLayout().Source, "plaud-android")
	if ownLayout().Source == "" || !fsx.Exists(filepath.Join(src, "gradlew")) {
		return errors.New("the app's source is not here: Fridolin builds it from plaud-android/ in the checkout it was installed from")
	}
	javaHome, sdk, err := androidToolchain()
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return errPlaudStopping
	}
	if p.appJob != nil && p.appJob.State == "running" {
		p.mu.Unlock()
		return errPlaudAppBuilding
	}
	job := &plaudAppJob{State: "running", Started: time.Now()}
	p.appJob = job
	p.bg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.bg.Done()
		exp, out, err := p.buildApp(p.lifetime(), src, javaHome, sdk, site, dev)
		p.mu.Lock()
		defer p.mu.Unlock()
		job.Finished, job.PairingExpires, job.State = time.Now(), exp, "done"
		if err != nil {
			job.State, job.Error, job.Log = "error", err.Error(), out
			log.Printf("plaud: building the phone app failed: %v", err)
		} else {
			log.Printf("plaud: built the phone app")
		}
	}()
	return nil
}

// buildApp runs the build and copies the app to appFile. On failure it
// returns the end of Gradle's output.
func (p *PlaudManager) buildApp(ctx context.Context, src, javaHome, sdk, site string, dev plaudDeveloper) (time.Time, string, error) {
	dir := p.appDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return time.Time{}, "", err
	}
	code, exp, err := p.newPairing()
	if err != nil {
		return time.Time{}, "", err
	}
	// What the app is built with; the build reads it, then it goes. The
	// build itself stays in the data folder, which brains cannot read.
	props := filepath.Join(dir, "build.properties")
	content := fmt.Sprintf("client_id=%s\nclient_secret=%s\napi_key=%s\nsite_url=%s\npairing_code=%s\n", dev.ClientID, dev.ClientSecret, dev.APIKey, site, code)
	if err := fsx.WriteFileAtomic(props, []byte(content), 0o600, false); err != nil {
		return time.Time{}, "", err
	}
	defer os.Remove(props)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(src, "gradlew"), "--no-daemon", "--console=plain", "-p", src,
		"--project-cache-dir", filepath.Join(dir, "gradle"),
		"-PfridolinConfig="+props, "-PfridolinBuildDir="+filepath.Join(dir, "build"), "assembleDebug")
	cmd.Dir = src
	cmd.Env = childEnv(helperScope(ctx), nil, "JAVA_HOME="+javaHome, "ANDROID_HOME="+sdk)
	out := &cappedBuffer{limit: 256 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		return time.Time{}, strings.Join(lines[max(0, len(lines)-40):], "\n"), fmt.Errorf("the build failed: %w", err)
	}
	apk, err := os.ReadFile(filepath.Join(dir, "build", "app", "outputs", "apk", "debug", "app-debug.apk"))
	if err != nil {
		return time.Time{}, "", err
	}
	return exp, "", fsx.WriteFileAtomic(p.appFile(), apk, 0o600, false)
}

// androidToolchain finds a JDK and the Android SDK to build the app with:
// JAVA_HOME and ANDROID_HOME (or ANDROID_SDK_ROOT) of the bridge's
// environment, else where macOS, Homebrew and Android Studio put them.
func androidToolchain() (javaHome, sdk string, err error) {
	home := homeDir()
	javas := []string{os.Getenv("JAVA_HOME")}
	sdks := []string{os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT"), filepath.Join(home, "Library", "Android", "sdk"), filepath.Join(home, "Android", "Sdk")}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/libexec/java_home").Output(); err == nil {
			javas = append(javas, strings.TrimSpace(string(out)))
		}
	}
	for _, prefix := range []string{"/opt/homebrew", "/usr/local", filepath.Join(home, "homebrew")} {
		for _, jdk := range []string{"openjdk", "openjdk@21", "openjdk@17"} {
			javas = append(javas, filepath.Join(prefix, "opt", jdk, "libexec", "openjdk.jdk", "Contents", "Home"))
		}
		sdks = append(sdks, filepath.Join(prefix, "share", "android-commandlinetools"))
	}
	javas = append(javas, "/Applications/Android Studio.app/Contents/jbr/Contents/Home")
	if jvms, _ := filepath.Glob("/usr/lib/jvm/*"); len(jvms) > 0 {
		javas = append(javas, jvms...)
	}
	for _, j := range javas {
		if j != "" && fsx.Exists(filepath.Join(j, "bin", "java")) {
			javaHome = j
			break
		}
	}
	for _, s := range sdks {
		if s != "" && isDir(filepath.Join(s, "platforms")) {
			sdk = s
			break
		}
	}
	switch {
	case javaHome == "":
		return "", "", errors.New("no JDK found to build the app with: install one (brew install openjdk@17), or set JAVA_HOME in the service's environment")
	case sdk == "":
		return "", "", errors.New(`no Android SDK found to build the app with: brew install --cask android-commandlinetools, then sdkmanager "platforms;android-35" "build-tools;35.0.0"; or set ANDROID_HOME in the service's environment`)
	}
	return javaHome, sdk, nil
}

// ── the dashboard ────────────────────────────────────────────────────────

// PlaudAppStatus is the phone app part of the Plaud card, there while
// plaud.phone_upload is on.
type PlaudAppStatus struct {
	// Credentials says whether Plaud's developer keys are saved.
	Credentials bool `json:"credentials"`
	// SiteURL is where the app uploads, "" while the site has no domain.
	SiteURL string          `json:"site_url,omitempty"`
	Built   *time.Time      `json:"built,omitempty"`
	Build   *plaudAppJob    `json:"build,omitempty"`
	Devices []PlaudPhoneRow `json:"devices"`
}

// PlaudPhoneRow is a paired phone on the card.
type PlaudPhoneRow struct {
	ID         string    `json:"id"`
	Paired     time.Time `json:"paired"`
	LastUpload time.Time `json:"last_upload,omitzero"`
}

func (p *PlaudManager) appStatus() *PlaudAppStatus {
	s := &PlaudAppStatus{Credentials: p.loadDeveloper().complete(), Devices: []PlaudPhoneRow{}}
	if p.siteURL != nil {
		s.SiteURL = p.siteURL()
	}
	if st, err := os.Stat(p.appFile()); err == nil {
		t := st.ModTime()
		s.Built = &t
	}
	p.mu.Lock()
	if p.appJob != nil {
		job := *p.appJob
		s.Build = &job
	}
	p.mu.Unlock()
	if st, err := p.loadDevices(); err == nil {
		for _, d := range st.Devices {
			s.Devices = append(s.Devices, PlaudPhoneRow{d.ID, d.Paired, d.LastUpload})
		}
	}
	return s
}

func (b *Bridge) apiPlaudAppCredentials(w http.ResponseWriter, r *http.Request) {
	var d plaudDeveloper
	if err := decodeJSON(r, &d); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := b.plaud.SaveDeveloper(d); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (b *Bridge) apiPlaudAppBuild(w http.ResponseWriter, r *http.Request) {
	switch err := b.plaud.BuildApp(); {
	case errors.Is(err, errPlaudAppBuilding):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusPreconditionFailed, err.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "building"})
	}
}

func (b *Bridge) apiPlaudAppDownload(w http.ResponseWriter, r *http.Request) {
	if !fsx.Exists(b.plaud.appFile()) {
		writeErr(w, http.StatusNotFound, "no phone app built yet")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="plaud-sync.apk"`)
	http.ServeFile(w, r, b.plaud.appFile())
}

func (b *Bridge) apiPlaudRevokeDevice(w http.ResponseWriter, r *http.Request) {
	switch err := b.plaud.RevokeDevice(r.PathValue("id")); {
	case errors.Is(err, errPlaudUnknownDevice):
		writeErr(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
	}
}
