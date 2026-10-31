package plaud

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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

const (
	// plaudPairingLifetime is how long a built app's pairing code works.
	plaudPairingLifetime = 24 * time.Hour
	// The audiences of the app's two JWTs, so neither passes for the other.
	plaudPairAud       = "fridolin:pair"
	plaudTranscriptAud = "fridolin:plaud-transcript"
	// A JWT may live this long, and clocks may be this far apart.
	plaudJWTMaxAge = 10 * time.Minute
	plaudClockSkew = time.Minute
	TranscriptMax  = 512 << 10
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
	// Rid and Enc are a voice question's (voice.go): the id the app gave
	// it, and the key its answer is encrypted to. Src "pin" makes it a
	// recording from the pin instead, whose Name is then the time it
	// started, as an upload's is.
	Rid string    `json:"rid,omitempty"`
	Enc *plaudJWK `json:"enc,omitempty"`
	Src string    `json:"src,omitempty"`
	// Ver, VName, Cert and Upd are a release request's (app_release.go):
	// the app's versionCode and versionName, the SHA-256 of the
	// certificate it is signed with, and how its last update went. APK
	// names the build a phone fetches by its SHA-256 (hex); Enc is then
	// the key it is encrypted for.
	Ver   int             `json:"ver,omitempty"`
	VName string          `json:"vname,omitempty"`
	Cert  string          `json:"cert,omitempty"`
	Upd   *appUpdateClaim `json:"upd,omitempty"`
	APK   string          `json:"apk,omitempty"`
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
	// App is what the phone said of its app when it last asked for the
	// release (app_release.go).
	App *PhoneApp `json:"app,omitempty"`
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

func (p *Manager) devicesPath() string { return filepath.Join(p.dir(), "devices.json") }

func (p *Manager) loadDevices() (plaudDevices, error) {
	var st plaudDevices
	data, err := os.ReadFile(p.devicesPath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, DecodeError(json.Unmarshal(data, &st))
}

// updateDevices changes devices.json with fn, drops the pairing codes that
// expired and rewrites the site's copy, which has the ids and public keys
// only. An error from fn saves nothing.
func (p *Manager) updateDevices(fn func(st *plaudDevices) error) error {
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
	sitePath := p.options.SiteDevices
	if sitePath == "" {
		return errors.New("plaud: no path for the site's copy of the paired devices")
	}
	if err := os.MkdirAll(filepath.Dir(sitePath), 0o700); err != nil {
		return err
	}
	data, _ = json.Marshal(site)
	return fsx.WriteFileAtomic(sitePath, data, 0o600, false)
}

// newPairing makes a pairing code for the next build of the app.
func (p *Manager) newPairing() (string, time.Time, error) {
	code := plaudRandomToken(18)
	exp := time.Now().Add(plaudPairingLifetime)
	sum := sha256.Sum256([]byte(code))
	return code, exp, p.updateDevices(func(st *plaudDevices) error {
		st.Pairings = append(st.Pairings, plaudPairing{Hash: hex.EncodeToString(sum[:]), Expires: exp})
		return nil
	})
}

// RevokeDevice unpairs phone id: its uploads are refused from now on.
func (p *Manager) RevokeDevice(id string) error {
	err := p.updateDevices(func(st *plaudDevices) error {
		n := len(st.Devices)
		st.Devices = slices.DeleteFunc(st.Devices, func(d plaudDevice) bool { return d.ID == id })
		if len(st.Devices) == n {
			return ErrUnknownDevice
		}
		return nil
	})
	if err == nil {
		log.Printf("plaud: phone %s unpaired on the dashboard", id)
	}
	return err
}

var ErrUnknownDevice = errors.New("there is no such paired phone")

// DeviceRequest answers a phone's request that the site passed on:
// "pair", "transcript" with the transcript in body, "voice" with a voice
// question in body, "voice_reply", which waits up to VoiceReplyWait
// for the answer to one (site_ingest.go), or "app_release", which asks
// for the current build of the app (app_release.go; AppDownload fetches
// it).
// Its errors go back to the phone, so they name what is wrong and nothing
// the request carried.
func (p *Manager) DeviceRequest(op, assertion string, body []byte) (map[string]any, error) {
	switch op {
	case "pair":
		id, err := p.pairDevice(assertion, time.Now())
		if err != nil {
			return nil, err
		}
		return map[string]any{"device": id}, nil
	case "transcript":
		return nil, p.takeTranscript(assertion, body, time.Now())
	case "voice":
		return p.takeVoice(assertion, body, time.Now())
	case "voice_reply":
		return p.voiceReply(assertion, time.Now())
	case "app_release":
		return p.appRelease(assertion, time.Now())
	}
	return nil, fmt.Errorf("unknown request %q", op)
}

// pairDevice keeps the key that signed assertion, a pairing request with a
// code that works: it proves the phone holds the key's private half.
func (p *Manager) pairDevice(assertion string, now time.Time) (string, error) {
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
	id := "dev_" + plaudRandomToken(9)
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
func (p *Manager) takeTranscript(assertion string, body []byte, now time.Time) error {
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
	if len(body) > TranscriptMax {
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
	p.noteUpload(c.Iss, now)
	log.Printf("plaud %s%s: uploaded by phone %s", plaudPhonePrefix, c.Name, c.Iss)
	// Handed over now rather than at the next check.
	if cfg.Plaud.Enabled {
		if _, err := p.CheckNow(); err != nil {
			log.Printf("plaud: %v", err)
		}
	}
	return nil
}

// noteUpload notes that phone id sent a transcript at now, as an upload
// or, in voice mode, a recording from the pin (voice.go).
func (p *Manager) noteUpload(id string, now time.Time) {
	_ = p.updateDevices(func(st *plaudDevices) error {
		for i := range st.Devices {
			if st.Devices[i].ID == id {
				st.Devices[i].LastUpload = now
			}
		}
		return nil
	})
}

// ── building the app ─────────────────────────────────────────────────────

// Developer is the Plaud developer app the phone app is built with
// (portal.plaud.ai): <data_dir>/plaud/developer.json. The dashboard only
// learns whether it is there.
type Developer struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	APIKey       string `json:"api_key"`
}

func (d Developer) complete() bool {
	return d.ClientID != "" && d.ClientSecret != "" && d.APIKey != ""
}

func (p *Manager) developerPath() string { return filepath.Join(p.dir(), "developer.json") }

func (p *Manager) loadDeveloper() Developer {
	var d Developer
	if data, err := os.ReadFile(p.developerPath()); err == nil {
		_ = json.Unmarshal(data, &d)
	}
	return d
}

// SaveDeveloper keeps the keys of d that are set; the others stay.
func (p *Manager) SaveDeveloper(d Developer) error {
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
	// Node is the node that builds it, "" for this machine; Stage says
	// what the build is doing while it runs.
	Node  string `json:"node,omitempty"`
	Stage string `json:"stage,omitempty"`
	// VersionCode is the build's (nextVersion).
	VersionCode int `json:"version_code,omitempty"`
}

var ErrAppBuilding = errors.New("the phone app is being built already")

func (p *Manager) appDir() string  { return filepath.Join(p.dir(), "app") }
func (p *Manager) AppFile() string { return filepath.Join(p.appDir(), "plaud-sync.apk") }

// BuildApp starts building the phone app in the background, with the
// site's address, a new pairing code and the next versionCode: Gradle, in
// plaud-android/ of the checkout Fridolin was installed from, on this
// machine where it has a JDK and the Android SDK, else on a node that
// builds Android apps (android-build). The build becomes the release the
// paired phones update to (app_release.go).
func (p *Manager) BuildApp() error {
	select {
	case <-p.lifetime().Done():
		return ErrStopping
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
	if p.options.SiteURL != nil {
		site = p.options.SiteURL()
	}
	if site == "" {
		return errors.New("the site has no domain yet (Site → Domain): the app uploads through it")
	}
	src, err := p.options.AppSource()
	if err != nil {
		return err
	}
	javaHome, sdk, localErr := p.options.AndroidToolchain()
	var node *AppBuilder
	if localErr != nil {
		builders := p.appBuilders()
		if len(builders) == 0 {
			return noBuilderError(localErr)
		}
		node = &builders[0]
	}
	version, err := p.nextVersion(src)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return ErrStopping
	}
	if p.appJob != nil && p.appJob.State == "running" {
		p.mu.Unlock()
		return ErrAppBuilding
	}
	job := &plaudAppJob{State: "running", Started: time.Now(), Stage: "Building on this computer", VersionCode: version.Code}
	if node != nil {
		job.Node, job.Stage = node.Name, "Packing the app's source for "+node.Name
	}
	p.appJob = job
	p.bg.Add(1)
	p.mu.Unlock()
	stage := func(s string) {
		p.mu.Lock()
		job.Stage = s
		p.mu.Unlock()
	}
	go func() {
		defer p.bg.Done()
		var exp time.Time
		var out string
		var err error
		if node != nil {
			exp, out, err = p.buildAppOnNode(p.lifetime(), *node, src, site, dev, version, stage)
		} else {
			exp, out, err = p.buildApp(p.lifetime(), src, javaHome, sdk, site, dev, version)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		job.Finished, job.PairingExpires, job.State, job.Stage = time.Now(), exp, "done", ""
		where := ""
		if node != nil {
			where = " on node " + node.Name
		}
		if err != nil {
			job.State, job.Error, job.Log = "error", err.Error(), out
			log.Printf("plaud: building the phone app%s failed: %v", where, err)
		} else {
			log.Printf("plaud: built the phone app %s (%d)%s", version.Name, version.Code, where)
		}
	}()
	return nil
}

// noBuilderHint says how to get the app built when this machine cannot
// build it and no node can.
const noBuilderHint = "join a node that has a JDK 17 or newer and the Android SDK (fridolin node join), and approve what it offers, android-build among it (docs/NODES.md, android-build)"

func noBuilderError(local error) error {
	return fmt.Errorf("this computer cannot build the app (%v), and no node that builds Android apps is online: %s", local, noBuilderHint)
}

func (p *Manager) appBuilders() []AppBuilder {
	if p.options.AppBuilders == nil || p.options.BuildOnNode == nil {
		return nil
	}
	return p.options.AppBuilders()
}

// appProperties is what the app is built with: Plaud's developer keys,
// the site to upload to, the pairing code and the versionCode.
func appProperties(dev Developer, site, code string, v appVersion) map[string]string {
	return map[string]string{
		"client_id": dev.ClientID, "client_secret": dev.ClientSecret, "api_key": dev.APIKey, "site_url": site, "pairing_code": code,
		"version_code": strconv.Itoa(v.Code),
	}
}

// buildAppOnNode has node build the app and saves it as the release. On
// failure it returns the end of Gradle's output.
func (p *Manager) buildAppOnNode(ctx context.Context, node AppBuilder, src, site string, dev Developer, v appVersion, stage func(string)) (time.Time, string, error) {
	archive, err := AppSourceArchive(src)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("packing the app's source: %w", err)
	}
	if err := os.MkdirAll(p.appDir(), 0o700); err != nil {
		return time.Time{}, "", err
	}
	code, exp, err := p.newPairing()
	if err != nil {
		return time.Time{}, "", err
	}
	stage("Building on " + node.Name + " (a few minutes)")
	var apk bytes.Buffer
	out, err := p.options.BuildOnNode(ctx, node.ID, archive, appProperties(dev, site, code, v), &apk)
	if err != nil {
		return time.Time{}, out, fmt.Errorf("on node %s: %w", node.Name, err)
	}
	if apk.Len() == 0 {
		return time.Time{}, "", fmt.Errorf("node %s sent no app", node.Name)
	}
	stage("Saving the app")
	_, err = p.saveBuild(apk.Bytes(), v, node.Name, time.Now())
	return exp, "", err
}

// appSourceSkips are what AppSourceArchive leaves out of plaud-android/:
// what its .gitignore names, Gradle's and the build's own files.
var appSourceSkips = []string{".gradle", "build", "app/build", "app/libs", "local.properties", ".idea"}

// AppSourceArchive packs the app's source in dir as a gzip'd tar for a
// node to build, without what a build there left (appSourceSkips, APKs)
// and without symlinks.
func AppSourceArchive(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if slices.Contains(appSourceSkips, rel) || strings.HasSuffix(rel, ".apk") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return nil
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name, h.Uname, h.Gname, h.Uid, h.Gid = rel, "", "", 0, 0
		if d.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = zw.Close()
	}
	return buf.Bytes(), err
}

// buildApp runs the build and saves the app as the release. On failure it
// returns the end of Gradle's output.
func (p *Manager) buildApp(ctx context.Context, src, javaHome, sdk, site string, dev Developer, v appVersion) (time.Time, string, error) {
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
	propsFile := filepath.Join(dir, "build.properties")
	content := ""
	props := appProperties(dev, site, code, v)
	for _, k := range slices.Sorted(maps.Keys(props)) {
		content += k + "=" + props[k] + "\n"
	}
	if err := fsx.WriteFileAtomic(propsFile, []byte(content), 0o600, false); err != nil {
		return time.Time{}, "", err
	}
	defer os.Remove(propsFile)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(src, "gradlew"), "--no-daemon", "--console=plain", "-p", src,
		"--project-cache-dir", filepath.Join(dir, "gradle"),
		"-PfridolinConfig="+propsFile, "-PfridolinBuildDir="+filepath.Join(dir, "build"), "assembleDebug")
	cmd.Dir = src
	cmd.Env = p.options.AppEnviron(ctx, javaHome, sdk)
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
	_, err = p.saveBuild(apk, v, "", time.Now())
	return exp, "", err
}

// ── the dashboard ────────────────────────────────────────────────────────

// AppStatus is the phone app part of the Plaud card, there while
// plaud.phone_upload is on.
type AppStatus struct {
	// Credentials says whether Plaud's developer keys are saved.
	Credentials bool `json:"credentials"`
	// SiteURL is where the app uploads, "" while the site has no domain.
	SiteURL string       `json:"site_url,omitempty"`
	Built   *time.Time   `json:"built,omitempty"`
	Build   *plaudAppJob `json:"build,omitempty"`
	// Release is the current build, the one the paired phones update to.
	Release *Release   `json:"release,omitempty"`
	Devices []PhoneRow `json:"devices"`
	// BuildsOn is where the next build runs: "this computer", or the name
	// of the node that builds it; "" when nothing can, and NoBuilder then
	// says why and what to do.
	BuildsOn  string `json:"builds_on,omitempty"`
	NoBuilder string `json:"no_builder,omitempty"`
}

// PhoneRow is a paired phone on the card.
type PhoneRow struct {
	ID         string    `json:"id"`
	Paired     time.Time `json:"paired"`
	LastUpload time.Time `json:"last_upload,omitzero"`
	// App is what the phone said of its app, nil before it asked for a
	// release (an app without updates never does).
	App *PhoneApp `json:"app,omitempty"`
	// OtherKey says that its app is signed with another key than the
	// release, which Android therefore will not install over it.
	OtherKey bool `json:"other_key,omitempty"`
}

func (p *Manager) appStatus() *AppStatus {
	s := &AppStatus{Credentials: p.loadDeveloper().complete(), Devices: []PhoneRow{}}
	if p.options.SiteURL != nil {
		s.SiteURL = p.options.SiteURL()
	}
	if err := p.localToolchain(); err == nil {
		s.BuildsOn = "this computer"
	} else if b := p.appBuilders(); len(b) > 0 {
		s.BuildsOn = b[0].Name
	} else {
		s.NoBuilder = noBuilderError(err).Error()
	}
	if st, err := os.Stat(p.AppFile()); err == nil {
		t := st.ModTime()
		s.Built = &t
	}
	s.Release = p.loadRelease()
	p.mu.Lock()
	if p.appJob != nil {
		job := *p.appJob
		s.Build = &job
	}
	p.mu.Unlock()
	if st, err := p.loadDevices(); err == nil {
		for _, d := range st.Devices {
			row := PhoneRow{ID: d.ID, Paired: d.Paired, LastUpload: d.LastUpload, App: d.App}
			row.OtherKey = d.App != nil && d.App.CertSHA256 != "" && s.Release != nil && d.App.CertSHA256 != s.Release.CertSHA256
			s.Devices = append(s.Devices, row)
		}
	}
	return s
}

// localToolchainTTL is how long the card trusts what it last found of the
// toolchain on this machine: the card asks every few seconds while a
// build runs, and finding the JDK may run a program.
const localToolchainTTL = time.Minute

// localToolchain is whether this machine has a toolchain to build the app
// with, looked up at most once per localToolchainTTL.
func (p *Manager) localToolchain() error {
	if p.options.AndroidToolchain == nil {
		return errors.New("no JDK and Android SDK here")
	}
	p.mu.Lock()
	if !p.toolchainAt.IsZero() && time.Since(p.toolchainAt) < localToolchainTTL {
		err := p.toolchainErr
		p.mu.Unlock()
		return err
	}
	p.mu.Unlock()
	_, _, err := p.options.AndroidToolchain()
	p.mu.Lock()
	p.toolchainAt, p.toolchainErr = time.Now(), err
	p.mu.Unlock()
	return err
}

// plaudRandomToken creates a private pairing token with the same encoding as the site.
func plaudRandomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
