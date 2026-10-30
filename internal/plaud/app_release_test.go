package plaud

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icewater1337/fridolin/internal/testutil"
)

func TestAPKSignerReadsTheSignersCertificate(t *testing.T) {
	cert := []byte("the builder's certificate, DER")
	got, err := apkSigner(testutil.SignedAPK(cert, map[string]string{"classes.dex": strings.Repeat("x", 5000)}))
	if err != nil || !bytes.Equal(got, cert) {
		t.Fatalf("apkSigner = %q, %v", got, err)
	}
	// A zip without a signing block, and what is no zip at all.
	plain := testutil.SignedAPK(cert, nil)
	cd := bytes.Index(plain, []byte("APK Sig Block 42"))
	plain[cd] = 'X'
	for name, apk := range map[string][]byte{"unsigned": plain, "no zip": []byte("PK fake apk"), "empty": nil} {
		if _, err := apkSigner(apk); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// What a node sends back is read without trust: no input makes the
// reader panic or read past it.
func FuzzAPKSigner(f *testing.F) {
	f.Add(testutil.SignedAPK([]byte("cert"), map[string]string{"a": "b"}))
	f.Add([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	f.Fuzz(func(t *testing.T, apk []byte) {
		_, _ = apkSigner(apk)
	})
}

func TestSourceVersionOfTheApp(t *testing.T) {
	v, err := sourceVersion(filepath.Join(repoDir, "plaud-android"))
	if err != nil || v.Code <= 0 || v.Name == "" {
		t.Fatalf("plaud-android's version: %+v, %v", v, err)
	}
	if _, err := sourceVersion(t.TempDir()); err == nil {
		t.Error("a source without app/build.gradle has a version")
	}
}

// openApp opens what sealApp wrote, as the app does (UpdateCrypto.kt).
func openApp(k *ecdh.PrivateKey, sealed []byte, apkSHA256 []byte, chunk int) ([]byte, error) {
	if len(sealed) < 65+8 {
		return nil, errors.New("too short")
	}
	epk, err := ecdh.P256().NewPublicKey(sealed[:65])
	if err != nil {
		return nil, err
	}
	secret, err := k.ECDH(epk)
	if err != nil {
		return nil, err
	}
	key, _ := hkdf.Key(sha256.New, secret, nil, appSealInfo, 32)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := append(bytes.Clone(sealed[65:73]), 0, 0, 0, 0)
	rest := sealed[73:]
	var out []byte
	for i := 0; ; i++ {
		n := min(len(rest), chunk+gcm.Overhead())
		last := n == len(rest)
		binary.BigEndian.PutUint32(nonce[8:], uint32(i))
		aad := binary.BigEndian.AppendUint32(bytes.Clone(apkSHA256), uint32(i))
		if last {
			aad = append(aad, 1)
		} else {
			aad = append(aad, 0)
		}
		piece, err := gcm.Open(nil, nonce, rest[:n], aad)
		if err != nil {
			return nil, err
		}
		out, rest = append(out, piece...), rest[n:]
		if last {
			return out, nil
		}
	}
}

func TestSealedAppOpensOnlyWholeAndInOrder(t *testing.T) {
	phone, _ := ecdh.P256().GenerateKey(rand.Reader)
	for _, size := range []int{1, appSealChunk - 1, appSealChunk, 2*appSealChunk + 17} {
		apk := make([]byte, size)
		_, _ = rand.Read(apk)
		sum := sha256.Sum256(apk)
		var sealed bytes.Buffer
		n, err := sealApp(voiceJWK(phone), apk, &sealed, appSealChunk, nil, nil)
		if err != nil || n != int64(sealed.Len()) {
			t.Fatalf("size %d: sealApp = %d, %v", size, n, err)
		}
		got, err := openApp(phone, sealed.Bytes(), sum[:], appSealChunk)
		if err != nil || !bytes.Equal(got, apk) {
			t.Fatalf("size %d: opened %d bytes, %v", size, len(got), err)
		}
		if size > appSealChunk {
			// Cut after a whole piece: the piece before the cut was not sealed as the last.
			cut := sealed.Bytes()[:73+appSealChunk+16]
			if _, err := openApp(phone, cut, sum[:], appSealChunk); err == nil {
				t.Errorf("size %d: a cut-off app opened", size)
			}
		}
		other := sha256.Sum256([]byte("another build"))
		if _, err := openApp(phone, sealed.Bytes(), other[:], appSealChunk); err == nil {
			t.Errorf("size %d: opened as another build", size)
		}
	}
	if _, err := sealApp(voiceJWK(phone), nil, &bytes.Buffer{}, appSealChunk, nil, nil); err == nil {
		t.Error("an empty app was sealed")
	}
}

// The vector UpdateCryptoTest.kt opens (plaud-android): pieces of 16
// bytes, so that it is short.
const (
	appVectorText   = "Plaud Sync 0.7 (8), an APK in pieces."
	appVectorPrefix = "0011223344556677"
	appVectorSealed = "04eac6ce536a07d9ecca6ae9349919180f408feb2e9662ce29d650ccd4cef0f5abed7737488f778ddaec1684d2080c42e2d7080601f4092e4589a3f400eadd5bd70011223344556677323fdb34acf0778a8cbb57d1dc717bad2ca68c066f46bdd8203866d38d3d2834ab1e8141b57f5408db4173fcfe09712057d05c2bd627deaa0bcb2e41fbb32f556d13e4e1a08eee7eb493d9d2af74f0cd59b239b7aa"
)

func TestSealAppVector(t *testing.T) {
	h := func(s string) []byte { b, _ := hex.DecodeString(s); return b }
	phone, _ := ecdh.P256().NewPrivateKey(h(voiceVectorPhoneKey))
	eph, _ := ecdh.P256().NewPrivateKey(h(voiceVectorEphKey))
	var sealed bytes.Buffer
	if _, err := sealApp(voiceJWK(phone), []byte(appVectorText), &sealed, 16, eph, h(appVectorPrefix)); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(sealed.Bytes()); got != appVectorSealed {
		t.Errorf("sealed = %s", got)
	}
	sum := sha256.Sum256([]byte(appVectorText))
	if got, err := openApp(phone, sealed.Bytes(), sum[:], 16); err != nil || string(got) != appVectorText {
		t.Errorf("opened %q, %v", got, err)
	}
}

// releaseRequest is a phone's question for the release.
func releaseRequest(t *testing.T, phone serviceTestPhone, iss string, ver int, cert string, upd *appUpdateClaim) string {
	t.Helper()
	now := time.Now().Unix()
	return phone.sign(t, plaudClaims{Iss: iss, Aud: plaudAppReleaseAud, Iat: now, Exp: now + 300, Ver: ver, VName: "0.7", Cert: cert, Upd: upd})
}

func certSum(cert []byte) string {
	s := sha256.Sum256(cert)
	return hex.EncodeToString(s[:])
}

// Each build is the release, numbered past the one before and past what
// a phone runs; a paired phone learns of it, says how its app is and how
// its last update went, and fetches the build sealed for its key, by the
// SHA-256 the release named.
func TestAPhoneUpdatesToTheRelease(t *testing.T) {
	node := &nodeBuild{}
	_, m := appBuildManager(t, []AppBuilder{{ID: "abc123", Name: "mac-mini"}}, node)
	phone := newServiceTestPhone(t)
	if err := m.updateDevices(func(st *plaudDevices) error {
		st.Devices = append(st.Devices, plaudDevice{ID: "dev_1", Key: phone.jwk, Paired: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mine := certSum(testAPKCert)

	// Before any build there is nothing to update to.
	ans, err := m.DeviceRequest("app_release", releaseRequest(t, phone, "dev_1", 7, mine, nil), nil)
	if err != nil || len(ans) != 0 {
		t.Fatalf("before a build: %v, %v", ans, err)
	}

	build := func() Release {
		t.Helper()
		if err := m.BuildApp(); err != nil {
			t.Fatal(err)
		}
		if job := waitAppBuild(t, m); job.State != "done" {
			t.Fatalf("job %+v", job)
		}
		return *m.loadRelease()
	}
	// The phone runs 7, the source's own: the build is 8.
	rel := build()
	if rel.VersionCode != 8 || rel.VersionName != "0.7" || rel.CertSHA256 != mine || rel.Node != "mac-mini" || rel.Size != int64(len(testAPK("8"))) {
		t.Fatalf("release %+v", rel)
	}
	node.mu.Lock()
	if node.props["version_code"] != "8" {
		t.Errorf("built with version_code %q", node.props["version_code"])
	}
	node.mu.Unlock()
	if st := m.appStatus(); st.Release == nil || st.Release.VersionCode != 8 || st.Build.VersionCode != 8 {
		t.Errorf("status: release %+v, build %+v", st.Release, st.Build)
	}

	upd := &appUpdateClaim{Code: 8, State: "confirm", At: time.Now().Unix()}
	ans, err = m.DeviceRequest("app_release", releaseRequest(t, phone, "dev_1", 7, mine, upd), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := ans["release"].(map[string]any)
	if r["version_code"] != 8 || r["sha256"] != rel.SHA256 || r["cert_sha256"] != mine {
		t.Fatalf("release answer %v", ans)
	}
	row := m.appStatus().Devices[0]
	if row.App == nil || row.App.VersionCode != 7 || row.App.Update == nil || row.App.Update.State != "confirm" || row.App.Update.VersionCode != 8 || row.OtherKey {
		t.Errorf("phone row %+v, app %+v", row, row.App)
	}

	// The phone fetches what the release named, for its agreement key.
	enc, _ := ecdh.P256().GenerateKey(rand.Reader)
	fetch := func(signer serviceTestPhone, apk string, key *plaudJWK, aud string) ([]byte, error) {
		now := time.Now().Unix()
		var out bytes.Buffer
		_, err := m.AppDownload(signer.sign(t, plaudClaims{Iss: "dev_1", Aud: aud, Iat: now, Exp: now + 300, APK: apk, Enc: key}), &out)
		return out.Bytes(), err
	}
	sealed, err := fetch(phone, rel.SHA256, voiceJWK(enc), plaudAppAPKAud)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := hex.DecodeString(rel.SHA256)
	if got, err := openApp(enc, sealed, sum, appSealChunk); err != nil || !bytes.Equal(got, testAPK("8")) {
		t.Fatalf("opened %d bytes, %v", len(got), err)
	}
	for what, f := range map[string]func() ([]byte, error){
		"another build": func() ([]byte, error) { return fetch(phone, certSum([]byte("old")), voiceJWK(enc), plaudAppAPKAud) },
		"no key":        func() ([]byte, error) { return fetch(phone, rel.SHA256, nil, plaudAppAPKAud) },
		"another phone": func() ([]byte, error) {
			return fetch(newServiceTestPhone(t), rel.SHA256, voiceJWK(enc), plaudAppAPKAud)
		},
		"another purpose": func() ([]byte, error) { return fetch(phone, rel.SHA256, voiceJWK(enc), plaudAppReleaseAud) },
	} {
		if out, err := f(); err == nil || len(out) != 0 {
			t.Errorf("%s: fetched %d bytes, %v", what, len(out), err)
		}
	}

	// Installed: the phone runs 8 now. A phone that says it runs 20 (a
	// build from elsewhere) puts the next build past it, and one signed
	// with another key shows on the card.
	if _, err := m.DeviceRequest("app_release", releaseRequest(t, phone, "dev_1", 20, certSum([]byte("another key")), &appUpdateClaim{Code: 8, State: "installed"}), nil); err != nil {
		t.Fatal(err)
	}
	if row := m.appStatus().Devices[0]; !row.OtherKey || row.App.VersionCode != 20 {
		t.Errorf("phone row %+v", row)
	}
	if rel := build(); rel.VersionCode != 21 {
		t.Errorf("the build after a phone with 20 is %d", rel.VersionCode)
	}
	if rel := build(); rel.VersionCode != 22 {
		t.Errorf("the build after 21 is %d", rel.VersionCode)
	}
	// A fetch of the build before is refused: the phone asks again.
	if _, err := fetch(phone, rel.SHA256, voiceJWK(enc), plaudAppAPKAud); err == nil || !strings.Contains(err.Error(), "not the current one") {
		t.Errorf("fetching a build that is no longer the release: %v", err)
	}
}

func TestReleaseRequestsSayOnlyWhatAnAppSays(t *testing.T) {
	_, m := appBuildManager(t, nil, &nodeBuild{})
	phone := newServiceTestPhone(t)
	if err := m.updateDevices(func(st *plaudDevices) error {
		st.Devices = append(st.Devices, plaudDevice{ID: "dev_1", Key: phone.jwk, Paired: time.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for what, c := range map[string]plaudClaims{
		"no version":      {Ver: 0, VName: "0.7"},
		"a name of HTML":  {Ver: 7, VName: "<b>0.7</b>"},
		"a cert of words": {Ver: 7, VName: "0.7", Cert: "my key"},
		"another state":   {Ver: 7, VName: "0.7", Upd: &appUpdateClaim{Code: 8, State: "hacked"}},
		"unpaired":        {Iss: "dev_2", Ver: 7, VName: "0.7"},
	} {
		if c.Iss == "" {
			c.Iss = "dev_1"
		}
		c.Aud, c.Iat, c.Exp = plaudAppReleaseAud, now, now+300
		if _, err := m.DeviceRequest("app_release", phone.sign(t, c), nil); err == nil {
			t.Errorf("%s: taken", what)
		}
	}
	long := strings.Repeat("\x1b[31mINSTALL_FAILED ", 40)
	if _, err := m.DeviceRequest("app_release", releaseRequest(t, phone, "dev_1", 7, "", &appUpdateClaim{Code: 8, State: "failed", Error: long}), nil); err != nil {
		t.Fatal(err)
	}
	e := m.appStatus().Devices[0].App.Update.Error
	if strings.ContainsRune(e, '\x1b') || len([]rune(e)) > maxUpdateError+1 || !strings.HasPrefix(e, "[31mINSTALL_FAILED") {
		t.Errorf("the error kept: %q", e)
	}
}
