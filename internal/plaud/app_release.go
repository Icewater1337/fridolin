package plaud

// Updates of the Plaud Sync phone app (docs/PLAUD.md, "Updates").
//
// Each build of the app, from the Plaud card or asked for by the core
// brain (plaud_app_update), numbers itself past every build before it and
// every version a phone reported (nextVersionCode), and becomes the
// current release: release.json beside the APK, with its versionCode,
// SHA-256 and the certificate it is signed with. A paired phone asks for
// the release at its check-ins (op "app_release", aud
// fridolin:app-release), and says in the same JWT which version it runs,
// which certificate signs it and how its last update went, which the card
// shows. When the release is newer, it fetches it (AppDownload, aud
// fridolin:app-apk). The APK carries Plaud's developer keys and the site,
// where module code runs, passes it on, so the bridge encrypts it for a
// key of the phone's keystore, as it does voice answers (sealApp). The
// phone checks the SHA-256 and that the build is signed with its own key,
// then installs it with Android's PackageInstaller (Updater.kt).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/fsx"
)

const (
	plaudAppReleaseAud = "fridolin:app-release"
	plaudAppAPKAud     = "fridolin:app-apk"
	// appSealInfo is HKDF's info for the key an APK is encrypted with.
	appSealInfo = "fridolin app update v1"
	// appSealChunk is the size of the pieces an APK is sealed in, so that
	// the phone opens it as it arrives instead of holding it all.
	appSealChunk = 64 << 10
	// maxVersionCode is the highest versionCode Google Play takes, which
	// Android's tools hold builds to as well.
	maxVersionCode = 2_100_000_000
	// maxUpdateError bounds what a phone says went wrong.
	maxUpdateError = 300
)

// Release is the current build of the phone app, the one paired phones
// update to: <data_dir>/plaud/app/release.json.
type Release struct {
	VersionCode int    `json:"version_code"`
	VersionName string `json:"version_name,omitempty"`
	// SHA256 is the APK's, CertSHA256 that of the certificate it is
	// signed with; both hex.
	SHA256     string    `json:"sha256"`
	CertSHA256 string    `json:"cert_sha256"`
	Size       int64     `json:"size"`
	Built      time.Time `json:"built"`
	// Node built it, "" for this machine.
	Node string `json:"node,omitempty"`
}

// PhoneApp is what a phone said of its app when it last asked for the
// release.
type PhoneApp struct {
	VersionCode int    `json:"version_code"`
	VersionName string `json:"version_name,omitempty"`
	// CertSHA256 is the certificate its app is signed with, hex.
	CertSHA256 string     `json:"cert_sha256,omitempty"`
	Update     *AppUpdate `json:"update,omitempty"`
	Asked      time.Time  `json:"asked"`
}

// AppUpdate is a phone's word on its last update.
type AppUpdate struct {
	// VersionCode is the build it was about.
	VersionCode int `json:"version_code"`
	// State is one of the updateStates.
	State string    `json:"state"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at,omitzero"`
}

// updateStates are what the app says of an update: downloaded and
// waiting until it is not syncing, handed to Android, waiting for the
// owner's tap on the phone, waiting until Plaud Sync may install apps,
// installed, or failed (Error says why).
var updateStates = []string{"waiting", "installing", "confirm", "not_allowed", "installed", "failed"}

// appUpdateClaim is the upd claim of a release request.
type appUpdateClaim struct {
	Code  int    `json:"code"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	At    int64  `json:"at,omitempty"`
}

func (p *Manager) releasePath() string { return filepath.Join(p.appDir(), "release.json") }

// loadRelease is the current release, nil before the first build that
// recorded one.
func (p *Manager) loadRelease() *Release {
	data, err := os.ReadFile(p.releasePath())
	if err != nil {
		return nil
	}
	var r Release
	if json.Unmarshal(data, &r) != nil || r.VersionCode <= 0 || r.SHA256 == "" {
		return nil
	}
	return &r
}

// appVersion is the version a build gives the app.
type appVersion struct {
	Code int
	Name string
}

var (
	gradleVersionCodeRE = regexp.MustCompile(`(?m)^\s*versionCode\s+(\d{1,10})\s*$`)
	gradleVersionNameRE = regexp.MustCompile(`(?m)^\s*versionName\s+'([0-9A-Za-z.+-]{1,32})'\s*$`)
	versionNameRE       = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,32}$`)
)

// sourceVersion is the version the app's source in src gives itself, in
// app/build.gradle.
func sourceVersion(src string) (appVersion, error) {
	data, err := os.ReadFile(filepath.Join(src, "app", "build.gradle"))
	if err != nil {
		return appVersion{}, err
	}
	code := gradleVersionCodeRE.FindSubmatch(data)
	name := gradleVersionNameRE.FindSubmatch(data)
	if code == nil || name == nil {
		return appVersion{}, errors.New("app/build.gradle of the app's source has no versionCode N and versionName '…' of its own")
	}
	n, err := strconv.Atoi(string(code[1]))
	if err != nil || n <= 0 || n > maxVersionCode {
		return appVersion{}, fmt.Errorf("app/build.gradle's versionCode %s is out of range", code[1])
	}
	return appVersion{Code: n, Name: string(name[1])}, nil
}

// nextVersion is the version of the next build: the source's versionName,
// and a versionCode at least the source's and past the release's and
// every version a paired phone reported, so that each phone takes the
// build as an update.
func (p *Manager) nextVersion(src string) (appVersion, error) {
	v, err := sourceVersion(src)
	if err != nil {
		return v, err
	}
	if rel := p.loadRelease(); rel != nil {
		v.Code = max(v.Code, rel.VersionCode+1)
	}
	st, err := p.loadDevices()
	if err != nil {
		return v, err
	}
	for _, d := range st.Devices {
		if d.App != nil {
			v.Code = max(v.Code, d.App.VersionCode+1)
		}
	}
	if v.Code > maxVersionCode {
		return v, fmt.Errorf("the next versionCode, %d, is past the highest Android takes", v.Code)
	}
	return v, nil
}

// saveBuild keeps apk, a build of version v made on node ("" for this
// machine), as the app to download and the current release.
func (p *Manager) saveBuild(apk []byte, v appVersion, node string, now time.Time) (Release, error) {
	cert, err := apkSigner(apk)
	if err != nil {
		return Release{}, fmt.Errorf("reading the built app's signature: %w", err)
	}
	sum, certSum := sha256.Sum256(apk), sha256.Sum256(cert)
	rel := Release{
		VersionCode: v.Code, VersionName: v.Name, SHA256: hex.EncodeToString(sum[:]), CertSHA256: hex.EncodeToString(certSum[:]),
		Size: int64(len(apk)), Built: now, Node: node,
	}
	if err := os.MkdirAll(p.appDir(), 0o700); err != nil {
		return rel, err
	}
	if err := fsx.WriteFileAtomic(p.AppFile(), apk, 0o600, false); err != nil {
		return rel, err
	}
	data, _ := json.MarshalIndent(rel, "", "  ")
	return rel, fsx.WriteFileAtomic(p.releasePath(), data, 0o600, false)
}

// pairedPhone checks that a paired phone signed assertion for aud, while
// uploads from the phone app are on, and returns its claims.
func (p *Manager) pairedPhone(assertion, aud string, now time.Time) (plaudClaims, error) {
	if !p.cfg().Plaud.PhoneUpload {
		return plaudClaims{}, errPhoneUploadOff
	}
	c, err := parsePlaudJWT(assertion)
	if err != nil {
		return c, err
	}
	st, err := p.loadDevices()
	if err != nil {
		return c, err
	}
	i := slices.IndexFunc(st.Devices, func(d plaudDevice) bool { return d.ID == c.Iss })
	if c.Iss == "" || i < 0 {
		return c, errors.New("this phone is not paired, or was unpaired")
	}
	return verifyPlaudJWT(assertion, st.Devices[i].Key, aud, now)
}

var hexSHA256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// phoneAppOf is what a release request says of the phone's app; it
// refuses what no app of ours says.
func phoneAppOf(c plaudClaims, now time.Time) (PhoneApp, error) {
	app := PhoneApp{VersionCode: c.Ver, VersionName: c.VName, CertSHA256: c.Cert, Asked: now}
	switch {
	case c.Ver <= 0 || c.Ver > maxVersionCode:
		return app, errors.New("the request does not say which version the app is")
	case !versionNameRE.MatchString(c.VName):
		return app, errors.New("the app's version name is not one of ours")
	case c.Cert != "" && !hexSHA256RE.MatchString(c.Cert):
		return app, errors.New("the app's certificate is not a SHA-256")
	}
	if u := c.Upd; u != nil {
		if !slices.Contains(updateStates, u.State) || u.Code <= 0 || u.Code > maxVersionCode {
			return app, errors.New("the app's word on its last update is not one of ours")
		}
		app.Update = &AppUpdate{VersionCode: u.Code, State: u.State, Error: cleanUpdateError(u.Error)}
		if u.At > 0 {
			app.Update.At = time.Unix(u.At, 0).UTC()
		}
	}
	return app, nil
}

// cleanUpdateError is what the card shows of a phone's error: one line of
// printable text, at most maxUpdateError runes.
func cleanUpdateError(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }), " ")
	if utf8.RuneCountInString(s) > maxUpdateError {
		s = string([]rune(s)[:maxUpdateError]) + "…"
	}
	return s
}

// appRelease answers a phone's question for the current release, and
// keeps what it says of its app for the card.
func (p *Manager) appRelease(assertion string, now time.Time) (map[string]any, error) {
	c, err := p.pairedPhone(assertion, plaudAppReleaseAud, now)
	if err != nil {
		return nil, err
	}
	app, err := phoneAppOf(c, now)
	if err != nil {
		return nil, err
	}
	var before *PhoneApp
	err = p.updateDevices(func(st *plaudDevices) error {
		for i := range st.Devices {
			if st.Devices[i].ID == c.Iss {
				before, st.Devices[i].App = st.Devices[i].App, &app
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	logAppChange(c.Iss, before, app)
	rel := p.loadRelease()
	if rel == nil {
		return map[string]any{}, nil
	}
	return map[string]any{"release": map[string]any{
		"version_code": rel.VersionCode, "version_name": rel.VersionName, "sha256": rel.SHA256,
		"cert_sha256": rel.CertSHA256, "size": rel.Size,
	}}, nil
}

// logAppChange logs a phone's new version and how its updates go, once
// each.
func logAppChange(device string, before *PhoneApp, now PhoneApp) {
	if before == nil || before.VersionCode != now.VersionCode {
		log.Printf("plaud: phone %s runs the app %s (%d)", device, now.VersionName, now.VersionCode)
	}
	u := now.Update
	if u == nil || before != nil && before.Update != nil && *before.Update == *u {
		return
	}
	msg := fmt.Sprintf("plaud: phone %s, update to %d: %s", device, u.VersionCode, u.State)
	if u.Error != "" {
		msg += ": " + u.Error
	}
	log.Print(msg)
}

// AppDownload writes the current release, sealed for the phone that
// signed assertion (aud fridolin:app-apk), to w. The phone names the
// build it wants by its SHA-256, as the release it asked for gave it, so
// a build made in between is not what it gets. It returns the size of
// what it wrote.
func (p *Manager) AppDownload(assertion string, w io.Writer) (map[string]any, error) {
	c, err := p.pairedPhone(assertion, plaudAppAPKAud, time.Now())
	if err != nil {
		return nil, err
	}
	if c.Enc == nil {
		return nil, errors.New("the request has no key to encrypt the app for")
	}
	rel := p.loadRelease()
	if rel == nil || c.APK != rel.SHA256 {
		return nil, errors.New("that build is not the current one any more: ask for the release again")
	}
	apk, err := os.ReadFile(p.AppFile())
	if err != nil {
		return nil, errors.New("the current build is missing: build the app again on the dashboard")
	}
	if sum := sha256.Sum256(apk); hex.EncodeToString(sum[:]) != rel.SHA256 {
		return nil, errors.New("the app changed while it was asked for: ask for the release again")
	}
	n, err := sealApp(c.Enc, apk, w, appSealChunk, nil, nil)
	if err != nil {
		return nil, err
	}
	log.Printf("plaud: phone %s fetched the app %s (%d)", c.Iss, rel.VersionName, rel.VersionCode)
	return map[string]any{"size": n}, nil
}

// sealApp encrypts apk for the phone that holds the private half of to:
// ECDH (P-256) with a fresh key, HKDF-SHA256 of the shared secret (no
// salt, appSealInfo) to an AES-256-GCM key. It writes the fresh public key
// (65 bytes, uncompressed), an 8-byte nonce prefix, then apk in pieces of
// chunk bytes, each sealed with the nonce prefix ‖ its index
// (uint32, big-endian), and as associated data the APK's SHA-256 ‖ the
// index ‖ 1 for the last piece, else 0: pieces can be neither reordered,
// cut off nor taken from another build. chunk is appSealChunk but in
// tests; eph and prefix are for tests too, nil makes fresh ones. It
// returns how much it wrote.
func sealApp(to *plaudJWK, apk []byte, w io.Writer, chunk int, eph *ecdh.PrivateKey, prefix []byte) (int64, error) {
	if len(apk) == 0 {
		return 0, errors.New("the app is empty")
	}
	pub, err := to.publicKey()
	if err != nil {
		return 0, err
	}
	peer, err := pub.ECDH()
	if err != nil {
		return 0, err
	}
	if eph == nil {
		if eph, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return 0, err
		}
	}
	secret, err := eph.ECDH(peer)
	if err != nil {
		return 0, err
	}
	key, err := hkdf.Key(sha256.New, secret, nil, appSealInfo, 32)
	if err != nil {
		return 0, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, err
	}
	if prefix == nil {
		prefix = make([]byte, 8)
		if _, err := rand.Read(prefix); err != nil {
			return 0, err
		}
	}
	sum := sha256.Sum256(apk)
	var written int64
	write := func(b []byte) error {
		n, err := w.Write(b)
		written += int64(n)
		return err
	}
	if err := write(append(eph.PublicKey().Bytes(), prefix...)); err != nil {
		return written, err
	}
	nonce := append(slices.Clone(prefix), 0, 0, 0, 0)
	aad := append(sum[:], 0, 0, 0, 0, 0)
	var out []byte
	for i := 0; i*chunk < len(apk); i++ {
		piece := apk[i*chunk : min(len(apk), (i+1)*chunk)]
		binary.BigEndian.PutUint32(nonce[8:], uint32(i))
		binary.BigEndian.PutUint32(aad[32:36], uint32(i))
		aad[36] = 0
		if (i+1)*chunk >= len(apk) {
			aad[36] = 1
		}
		out = gcm.Seal(out[:0], nonce, piece, aad)
		if err := write(out); err != nil {
			return written, err
		}
	}
	return written, nil
}
