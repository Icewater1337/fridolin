package testutil

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
)

// SignedAPK is an APK as Android's signing tools lay it out: a zip of
// files, with an APK Signing Block between its entries and its central
// directory, which holds a v2 signature block whose signer carries cert.
// Nothing in it is a real signature: it is for code that reads which
// certificate an APK names, never for code that checks one.
func SignedAPK(cert []byte, files map[string]string) []byte {
	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	for name, content := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(content))
	}
	_ = zw.Close()
	data := z.Bytes()
	eocd := bytes.LastIndex(data, []byte("PK\x05\x06"))
	cd := int(binary.LittleEndian.Uint32(data[eocd+16 : eocd+20]))

	lp := func(parts ...[]byte) []byte {
		var b []byte
		for _, p := range parts {
			b = binary.LittleEndian.AppendUint32(b, uint32(len(p)))
			b = append(b, p...)
		}
		return b
	}
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	signed := cat(lp(nil), lp(lp(cert)), lp(nil))        // digests, certificates, attributes
	signer := cat(lp(signed), lp(nil), lp([]byte{1, 2})) // signed data, signatures, public key
	v2 := lp(lp(signer))                                 // one signer
	pair := binary.LittleEndian.AppendUint64(nil, uint64(4+len(v2)))
	pair = binary.LittleEndian.AppendUint32(pair, 0x7109871a)
	pair = append(pair, v2...)
	size := uint64(len(pair) + 8 + 16)
	block := binary.LittleEndian.AppendUint64(nil, size)
	block = append(block, pair...)
	block = binary.LittleEndian.AppendUint64(block, size)
	block = append(block, "APK Sig Block 42"...)

	out := cat(data[:cd], block, data[cd:])
	// The end of central directory record points past the block now.
	at := len(out) - (len(data) - eocd)
	binary.LittleEndian.PutUint32(out[at+16:at+20], uint32(cd+len(block)))
	return out
}
