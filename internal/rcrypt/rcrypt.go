// Package rcrypt writes and reads the file contents of rclone's crypt
// remote (https://rclone.org/crypt/#file-encryption), so that the core can
// encrypt what it stores in a folder on a node the way rclone would: the
// folder then opens with rclone's own crypt remote over it
// (filename_encryption off, suffix none) on any machine, without
// fridolin. It is the outer layer of a node target's restic repository
// (docs/VAULT.md, "Encryption"); restic's own encryption is the inner one.
//
// The format, as rclone's backend/crypt/cipher.go writes it: the header
// "RCLONE\x00\x00" and a random 24-byte nonce, then the plaintext in
// blocks of 64 KiB, each sealed with NaCl secretbox (XSalsa20-Poly1305)
// under the data key and the nonce, which counts up by one per block
// (little-endian over all 24 bytes). The data key is the first 32 bytes
// of scrypt(password, salt, N=16384, r=8, p=1, 80 bytes); without a salt
// (rclone's password2) rclone uses a fixed one. Names are not encrypted
// here: the node needs restic's names to check them.
//
// What rclone's format does not do: tell a file cut at a block boundary
// from a whole one. The inner layer does (restic checks every blob's MAC
// and every pack's size and hash).
package rcrypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

// Sizes of rclone's format.
const (
	magic      = "RCLONE\x00\x00"
	nonceSize  = 24
	HeaderSize = len(magic) + nonceSize
	tagSize    = secretbox.Overhead
	BlockSize  = 64 << 10 // plaintext per block
	sealedSize = BlockSize + tagSize
)

// defaultSalt is rclone's salt for a remote without password2.
var defaultSalt = []byte{0xA8, 0x0D, 0xF4, 0x3A, 0x8F, 0xBD, 0x03, 0x08, 0xA7, 0xCA, 0xB8, 0x3E, 0x58, 0x1F, 0x86, 0xB1}

// Errors of decryption.
var (
	ErrBadHeader = errors.New("rcrypt: not an rclone crypt file (no RCLONE header)")
	ErrBadBlock  = errors.New("rcrypt: a block failed to decrypt: wrong password, or the file is damaged")
	ErrBadSize   = errors.New("rcrypt: no rclone crypt file has this size")
)

// Cipher holds the data key of one password and salt.
type Cipher struct {
	key [32]byte
}

// New derives the data key from the password and salt (rclone's password
// and password2, in the clear; an empty salt is rclone's default one).
func New(password, salt string) (*Cipher, error) {
	if password == "" {
		return nil, errors.New("rcrypt: no password")
	}
	s := defaultSalt
	if salt != "" {
		s = []byte(salt)
	}
	k, err := scrypt.Key([]byte(password), s, 16384, 8, 1, 80)
	if err != nil {
		return nil, err
	}
	c := &Cipher{}
	copy(c.key[:], k)
	return c, nil
}

type nonce [nonceSize]byte

func (n *nonce) carry(i int) {
	for ; i < len(n); i++ {
		d := n[i]
		n[i] = d + 1
		if n[i] >= d {
			return
		}
	}
}

func (n *nonce) increment() { n.carry(0) }

// add adds x, as rclone's nonce.add does.
func (n *nonce) add(x uint64) {
	carry := uint16(0)
	for i := range 8 {
		carry += uint16(n[i]) + uint16(byte(x))
		x >>= 8
		n[i] = byte(carry)
		carry >>= 8
	}
	if carry != 0 {
		n.carry(8)
	}
}

// EncryptedSize is the size of the encrypted file of n plaintext bytes.
func EncryptedSize(n int64) int64 {
	blocks, rest := n/BlockSize, n%BlockSize
	size := int64(HeaderSize) + blocks*sealedSize
	if rest != 0 {
		size += tagSize + rest
	}
	return size
}

// DecryptedSize is the plaintext size of an encrypted file of n bytes.
func DecryptedSize(n int64) (int64, error) {
	n -= int64(HeaderSize)
	if n < 0 {
		return 0, ErrBadSize
	}
	blocks, rest := n/sealedSize, n%sealedSize
	size := blocks * BlockSize
	if rest != 0 {
		rest -= tagSize
		if rest <= 0 {
			return 0, ErrBadSize
		}
	}
	return size + rest, nil
}

// encrypter is an io.Reader of the encrypted form of in.
type encrypter struct {
	c     *Cipher
	in    io.Reader
	nonce nonce
	plain []byte
	out   []byte
	pos   int
	eof   bool
	err   error
}

// EncryptReader returns a reader of in encrypted, with a new random nonce.
func (c *Cipher) EncryptReader(in io.Reader) (io.Reader, error) {
	e := &encrypter{c: c, in: in, plain: make([]byte, BlockSize)}
	if _, err := rand.Read(e.nonce[:]); err != nil {
		return nil, err
	}
	e.out = append(append(make([]byte, 0, HeaderSize+sealedSize), magic...), e.nonce[:]...)
	return e, nil
}

func (e *encrypter) Read(p []byte) (int, error) {
	for e.pos == len(e.out) {
		if e.err != nil {
			return 0, e.err
		}
		if e.eof {
			return 0, io.EOF
		}
		n, err := io.ReadFull(e.in, e.plain)
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			e.eof = true
		case err != nil:
			e.err = err
			continue
		}
		e.out, e.pos = e.out[:0], 0
		if n > 0 {
			nn := [nonceSize]byte(e.nonce)
			e.out = secretbox.Seal(e.out, e.plain[:n], &nn, &e.c.key)
			e.nonce.increment()
		}
	}
	n := copy(p, e.out[e.pos:])
	e.pos += n
	return n, nil
}

// Encrypt returns plaintext encrypted.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	r, err := c.EncryptReader(bytes.NewReader(plaintext))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// ReadAt reads n bytes of the encrypted file at offset off; fewer only at
// its end.
type ReadAt func(off, n int64) ([]byte, error)

// DecryptRange returns the plaintext bytes [off, off+n) (n < 0: to the
// end) of the encrypted file of size encSize that readAt reads, reading
// only the header and the blocks that hold them. It refuses a header that
// is not rclone's and any block that does not open.
func (c *Cipher) DecryptRange(readAt ReadAt, encSize, off, n int64) ([]byte, error) {
	plainSize, err := DecryptedSize(encSize)
	if err != nil {
		return nil, err
	}
	if off < 0 || off > plainSize {
		return nil, fmt.Errorf("rcrypt: offset %d is past the end (%d)", off, plainSize)
	}
	end := plainSize
	if n >= 0 {
		end = min(plainSize, off+n)
	}
	hdr, err := readAt(0, int64(HeaderSize))
	if err != nil {
		return nil, err
	}
	if len(hdr) != HeaderSize || string(hdr[:len(magic)]) != magic {
		return nil, ErrBadHeader
	}
	if end <= off {
		return []byte{}, nil
	}
	var base nonce
	copy(base[:], hdr[len(magic):])
	first, last := off/BlockSize, (end-1)/BlockSize
	encOff := int64(HeaderSize) + first*sealedSize
	encEnd := min(encSize, int64(HeaderSize)+(last+1)*sealedSize)
	enc, err := readAt(encOff, encEnd-encOff)
	if err != nil {
		return nil, err
	}
	if int64(len(enc)) != encEnd-encOff {
		return nil, fmt.Errorf("%w: the file is shorter than its size says", ErrBadBlock)
	}
	out := make([]byte, 0, end-off)
	for b := first; b <= last; b++ {
		chunk := enc[(b-first)*sealedSize : min(int64(len(enc)), (b-first+1)*sealedSize)]
		nn := base
		nn.add(uint64(b))
		arr := [nonceSize]byte(nn)
		plain, ok := secretbox.Open(nil, chunk, &arr, &c.key)
		if !ok {
			return nil, fmt.Errorf("%w (block %d)", ErrBadBlock, b)
		}
		lo, hi := int64(0), int64(len(plain))
		if b == first {
			lo = off - first*BlockSize
		}
		if b == last {
			hi = end - last*BlockSize
		}
		if lo > hi || hi > int64(len(plain)) {
			return nil, fmt.Errorf("%w (block %d is short)", ErrBadBlock, b)
		}
		out = append(out, plain[lo:hi]...)
	}
	return out, nil
}

// Decrypt returns the plaintext of a whole encrypted file.
func (c *Cipher) Decrypt(enc []byte) ([]byte, error) {
	return c.DecryptRange(func(off, n int64) ([]byte, error) {
		if off >= int64(len(enc)) {
			return nil, nil
		}
		return enc[off:min(int64(len(enc)), off+n)], nil
	}, int64(len(enc)), 0, -1)
}
