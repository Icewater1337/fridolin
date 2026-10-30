package plaud

// The certificate an APK is signed with, read from its APK Signing Block
// (https://source.android.com/docs/security/features/apksigning/v2). The
// bridge records it with each build of the phone app, so the card can say
// when a build is signed with another key than the app on a phone, which
// Android would not update. It only reads: the phone checks the signature
// itself before it installs a build (Updater.kt).

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const (
	apkSigMagic = "APK Sig Block 42"
	// The signature schemes' blocks, the one Android prefers first: v3.1,
	// v3, v2. All three keep the signer's certificates in the same place.
	apkSigV31 = 0x1b93ad61
	apkSigV3  = 0xf05368c0
	apkSigV2  = 0x7109871a
	// zipEOCDMin is the end of central directory record without its
	// comment, which is at most 65535 bytes.
	zipEOCDMin = 22
)

var errNoAPKSignature = errors.New("the APK carries no signature of scheme v2 or v3")

// apkSigner returns the DER certificate of apk's first signer, from its
// v3.1, v3 or v2 signature block.
func apkSigner(apk []byte) ([]byte, error) {
	cd, err := zipCentralDirectory(apk)
	if err != nil {
		return nil, err
	}
	// The signing block ends right before the central directory, with its
	// size (again) and its magic.
	if cd < 24+8 || string(apk[cd-16:cd]) != apkSigMagic {
		return nil, errNoAPKSignature
	}
	size := binary.LittleEndian.Uint64(apk[cd-24 : cd-16])
	if size < 24 || size > uint64(cd-8) {
		return nil, errors.New("the APK's signing block has a bad size")
	}
	start := cd - int(size) - 8
	if binary.LittleEndian.Uint64(apk[start:start+8]) != size {
		return nil, errors.New("the APK's signing block has two different sizes")
	}
	blocks := map[uint32][]byte{}
	for pairs := apk[start+8 : cd-24]; len(pairs) > 0; {
		if len(pairs) < 12 {
			return nil, errors.New("the APK's signing block is cut off")
		}
		n := binary.LittleEndian.Uint64(pairs[:8])
		if n < 4 || n > uint64(len(pairs)-8) {
			return nil, errors.New("the APK's signing block has a bad entry")
		}
		blocks[binary.LittleEndian.Uint32(pairs[8:12])] = pairs[12 : 8+n]
		pairs = pairs[8+n:]
	}
	for _, id := range []uint32{apkSigV31, apkSigV3, apkSigV2} {
		if b, ok := blocks[id]; ok {
			return firstSignerCert(b)
		}
	}
	return nil, errNoAPKSignature
}

// zipCentralDirectory is where apk's central directory starts, as its end
// of central directory record says.
func zipCentralDirectory(apk []byte) (int, error) {
	from := max(0, len(apk)-zipEOCDMin-0xffff)
	i := bytes.LastIndex(apk[from:], []byte("PK\x05\x06"))
	if i < 0 || from+i+zipEOCDMin > len(apk) {
		return 0, errors.New("the APK is not a zip archive")
	}
	eocd := apk[from+i:]
	cd := int(binary.LittleEndian.Uint32(eocd[16:20]))
	if cd > from+i {
		return 0, errors.New("the APK's central directory lies past its end")
	}
	return cd, nil
}

// firstSignerCert reads a v2/v3 block: length-prefixed signers, each a
// length-prefixed signed data first, whose second field is the
// length-prefixed certificates, each length-prefixed in turn.
func firstSignerCert(block []byte) ([]byte, error) {
	signers, _, ok := lpField(block)
	if !ok {
		return nil, errors.New("the APK's signature block has no signers")
	}
	signer, _, ok := lpField(signers)
	if !ok {
		return nil, errors.New("the APK's signature block has no signer")
	}
	signed, _, ok := lpField(signer)
	if !ok {
		return nil, errors.New("the APK's signer has no signed data")
	}
	_, rest, ok := lpField(signed) // the digests
	if !ok {
		return nil, errors.New("the APK's signed data has no digests")
	}
	certs, _, ok := lpField(rest)
	if !ok {
		return nil, errors.New("the APK's signed data has no certificates")
	}
	cert, _, ok := lpField(certs)
	if !ok || len(cert) == 0 {
		return nil, errors.New("the APK's signer has no certificate")
	}
	return cert, nil
}

// lpField splits b into its first uint32-length-prefixed field and the
// rest.
func lpField(b []byte) (field, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.LittleEndian.Uint32(b[:4])
	if uint64(n) > uint64(len(b)-4) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}
