// Package seal is fridolin's own symmetric layer: a stream encrypted with
// XChaCha20-Poly1305 under a 256-bit key, in chunks, so that a file of any
// size is encrypted and decrypted without holding it in memory, and a
// stream cut short, reordered or spliced from two files fails to decrypt
// (docs/VAULT.md, "Encryption"). It is the inner layer of the vault's
// cold tier, of the Litestream replicas and of the core exports; the outer
// layer of each is another cipher under another key (rclone crypt, age).
//
// The format, version 1:
//
//	magic   "FRDSEAL1"                                  8 bytes
//	key id  HMAC-SHA256(key, "fridolin seal key id")[:8]  8 bytes
//	salt    random                                      32 bytes
//	chunks  XChaCha20-Poly1305(subkey, nonce(i, last), chunk i, header)
//
// subkey is HKDF-SHA256(key, salt, "fridolin seal v1"), a key of its own
// per file, so nonces never repeat under one key however many files the
// key seals. nonce(i, last) is 15 zero bytes, i as a big-endian uint64 and
// one byte that is 1 for the last chunk, 0 before it (the STREAM
// construction): every chunk but the last holds ChunkSize bytes, the last
// 0 to ChunkSize, and an empty stream is one empty last chunk. The header
// is each chunk's associated data. The key id tells a wrong key from a
// damaged file before any chunk is tried; it reveals nothing of the key.
package seal

import (
	"bufio"
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the size of a key.
const KeySize = 32

// ChunkSize is the plaintext size of every chunk but the last.
const ChunkSize = 64 << 10

const (
	magic      = "FRDSEAL1"
	keyIDSize  = 8
	saltSize   = 32
	HeaderSize = len(magic) + keyIDSize + saltSize
	tagSize    = chacha20poly1305.Overhead
	infoSubkey = "fridolin seal v1"
	infoKeyID  = "fridolin seal key id"
)

// Errors of Decrypt. A sealed stream that fails to decrypt is never
// passed on in part as if it were whole: the reader returns the error.
var (
	ErrNotSealed = errors.New("seal: not a sealed stream (no FRDSEAL1 header)")
	ErrWrongKey  = errors.New("seal: sealed with another key")
	ErrDamaged   = errors.New("seal: the sealed stream is damaged or cut short")
)

// NewKey returns a new random key, as text: base64url without padding,
// the form host secrets keep (32 random bytes, as the vault's passwords).
func NewKey() (string, error) {
	var b [KeySize]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// ParseKey reads a key written by NewKey (base64url, with or without
// padding; standard base64 as well, as a password manager may hold it).
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, fmt.Errorf("seal: a key is %d bytes in base64url (43 characters), as fridolin made it", KeySize)
}

// KeyID is the key's 8-byte id as the header carries it.
func KeyID(key []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(infoKeyID))
	return m.Sum(nil)[:keyIDSize]
}

func checkKey(key []byte) error {
	if len(key) != KeySize {
		return fmt.Errorf("seal: the key must be %d bytes, not %d", KeySize, len(key))
	}
	return nil
}

func subkey(key, salt []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, key, salt, infoSubkey, chacha20poly1305.KeySize)
}

func nonce(i uint64, last bool) []byte {
	n := make([]byte, chacha20poly1305.NonceSizeX)
	binary.BigEndian.PutUint64(n[15:23], i)
	if last {
		n[23] = 1
	}
	return n
}

// SealedSize is the size of the sealed stream of a plaintext of n bytes.
func SealedSize(n int64) int64 {
	chunks := max(1, (n+ChunkSize-1)/ChunkSize)
	return int64(HeaderSize) + n + chunks*tagSize
}

// PlainSize is the plaintext size of a sealed stream of n bytes, or an
// error for a size no sealed stream has.
func PlainSize(n int64) (int64, error) {
	x := n - int64(HeaderSize)
	if x < tagSize {
		return 0, ErrDamaged
	}
	full := int64(ChunkSize + tagSize)
	chunks := (x + full - 1) / full
	p := x - chunks*tagSize
	if p < 0 || SealedSize(p) != n {
		return 0, ErrDamaged
	}
	return p, nil
}

// writer seals what is written to it.
type writer struct {
	dst  io.Writer
	aead interface {
		Seal(dst, nonce, plaintext, ad []byte) []byte
	}
	header []byte
	buf    []byte // up to ChunkSize bytes not yet sealed
	out    []byte
	i      uint64
	err    error
	closed bool
}

// Encrypt returns a writer that seals what is written to it into dst under
// key. Close seals the last chunk; it does not close dst. Nothing written
// is complete until Close returns nil.
func Encrypt(dst io.Writer, key []byte) (io.WriteCloser, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	header := make([]byte, 0, HeaderSize)
	header = append(header, magic...)
	header = append(header, KeyID(key)...)
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	header = append(header, salt...)
	sk, err := subkey(key, salt)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(sk)
	if err != nil {
		return nil, err
	}
	if _, err := dst.Write(header); err != nil {
		return nil, err
	}
	return &writer{dst: dst, aead: aead, header: header, buf: make([]byte, 0, ChunkSize)}, nil
}

func (w *writer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("seal: write after close")
	}
	n := 0
	for len(p) > 0 {
		// A full buffer is sealed only once more data shows it is not
		// the last chunk.
		if len(w.buf) == ChunkSize {
			if w.err = w.flush(false); w.err != nil {
				return n, w.err
			}
		}
		k := copy(w.buf[len(w.buf):ChunkSize], p)
		w.buf = w.buf[:len(w.buf)+k]
		p = p[k:]
		n += k
	}
	return n, nil
}

func (w *writer) flush(last bool) error {
	w.out = w.aead.Seal(w.out[:0], nonce(w.i, last), w.buf, w.header)
	w.i++
	w.buf = w.buf[:0]
	_, err := w.dst.Write(w.out)
	return err
}

func (w *writer) Close() error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return nil
	}
	w.closed = true
	w.err = w.flush(true)
	return w.err
}

// reader opens a sealed stream.
type reader struct {
	src  *bufio.Reader
	aead interface {
		Open(dst, nonce, ciphertext, ad []byte) ([]byte, error)
	}
	header []byte
	in     []byte
	buf    []byte
	plain  []byte
	pos    int
	i      uint64
	done   bool
	err    error
}

// Decrypt returns a reader of the plaintext of the sealed stream src. It
// reads the header at once: a stream that is not sealed, or sealed under
// another key, is an error here (ErrNotSealed, ErrWrongKey). A chunk that
// fails to open, and a stream that ends before its last chunk, are an
// error matching ErrDamaged from Read; Read returns io.EOF only after the
// last chunk opened.
func Decrypt(src io.Reader, key []byte) (io.Reader, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(src, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrNotSealed
		}
		return nil, err
	}
	if string(header[:len(magic)]) != magic {
		return nil, ErrNotSealed
	}
	if !hmac.Equal(header[len(magic):len(magic)+keyIDSize], KeyID(key)) {
		return nil, ErrWrongKey
	}
	sk, err := subkey(key, header[len(magic)+keyIDSize:])
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(sk)
	if err != nil {
		return nil, err
	}
	return &reader{src: bufio.NewReaderSize(src, ChunkSize+tagSize+1), aead: aead, header: header, in: make([]byte, ChunkSize+tagSize), buf: make([]byte, 0, ChunkSize)}, nil
}

func (r *reader) Read(p []byte) (int, error) {
	for r.pos == len(r.plain) {
		if r.err != nil {
			return 0, r.err
		}
		if r.done {
			r.err = io.EOF
			return 0, io.EOF
		}
		r.err = r.next()
	}
	n := copy(p, r.plain[r.pos:])
	r.pos += n
	return n, nil
}

// next opens the next chunk.
func (r *reader) next() error {
	n, err := io.ReadFull(r.src, r.in)
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// Shorter than a full chunk: it must be the last.
		r.done = true
	case err != nil:
		return err
	default:
		// A full chunk is the last one only when nothing follows it.
		if _, perr := r.src.Peek(1); errors.Is(perr, io.EOF) {
			r.done = true
		} else if perr != nil {
			return perr
		}
	}
	if n < tagSize {
		return fmt.Errorf("%w: it ends inside a chunk", ErrDamaged)
	}
	plain, oerr := r.aead.Open(r.buf[:0], nonce(r.i, r.done), r.in[:n], r.header)
	if oerr != nil {
		return fmt.Errorf("%w (chunk %d)", ErrDamaged, r.i)
	}
	r.i++
	r.plain, r.pos = plain, 0
	return nil
}

// Seal returns plaintext sealed under key: for small values (a key check,
// a password file).
func Seal(plaintext, key []byte) ([]byte, error) {
	var b bytes.Buffer
	w, err := Encrypt(&b, key)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Open returns the plaintext of sealed, read whole.
func Open(sealed, key []byte) ([]byte, error) {
	r, err := Decrypt(bytes.NewReader(sealed), key)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
