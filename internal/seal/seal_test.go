package seal

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func testKey(t testing.TB) []byte {
	t.Helper()
	s, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sealAll(t testing.TB, data, key []byte, writes int) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := Encrypt(&b, key)
	if err != nil {
		t.Fatal(err)
	}
	step := max(1, len(data)/max(1, writes))
	for p := data; len(p) > 0; {
		n := min(step, len(p))
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRoundTripSizes(t *testing.T) {
	key := testKey(t)
	for _, n := range []int{0, 1, 100, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 3*ChunkSize + 17} {
		data := make([]byte, n)
		_, _ = rand.Read(data)
		for _, writes := range []int{1, 7} {
			sealed := sealAll(t, data, key, writes)
			if got := int64(len(sealed)); got != SealedSize(int64(n)) {
				t.Errorf("%d bytes: sealed %d, SealedSize says %d", n, got, SealedSize(int64(n)))
			}
			if p, err := PlainSize(int64(len(sealed))); err != nil || p != int64(n) {
				t.Errorf("%d bytes: PlainSize(%d) = %d, %v", n, len(sealed), p, err)
			}
			got, err := Open(sealed, key)
			if err != nil {
				t.Fatalf("%d bytes: %v", n, err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("%d bytes: the plaintext came back changed", n)
			}
		}
	}
}

func TestSaltMakesEachSealDifferent(t *testing.T) {
	key := testKey(t)
	a := sealAll(t, []byte("same"), key, 1)
	b := sealAll(t, []byte("same"), key, 1)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext are equal")
	}
}

func TestWrongKeyFailsClosed(t *testing.T) {
	sealed := sealAll(t, []byte("secret"), testKey(t), 1)
	if _, err := Open(sealed, testKey(t)); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("another key: %v, want ErrWrongKey", err)
	}
	if _, err := Open([]byte("plain text, not sealed at all, long enough for a header"), testKey(t)); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("plaintext: %v, want ErrNotSealed", err)
	}
	if _, err := Decrypt(bytes.NewReader(sealed), []byte("short")); err == nil {
		t.Fatal("a short key was taken")
	}
}

func TestTamperingFailsClosed(t *testing.T) {
	key := testKey(t)
	data := make([]byte, 2*ChunkSize+5)
	_, _ = rand.Read(data)
	sealed := sealAll(t, data, key, 1)
	cases := map[string][]byte{
		"cut after a full chunk":  sealed[:HeaderSize+ChunkSize+tagSize],
		"cut inside a chunk":      sealed[:len(sealed)-3],
		"cut to the header":       sealed[:HeaderSize],
		"a flipped byte":          flip(sealed, HeaderSize+10),
		"a flipped salt":          flip(sealed, HeaderSize-1),
		"chunks swapped":          swapChunks(sealed),
		"a byte appended":         append(append([]byte{}, sealed...), 0),
		"the last chunk repeated": append(append([]byte{}, sealed...), sealed[HeaderSize+2*(ChunkSize+tagSize):]...),
	}
	for name, c := range cases {
		r, err := Decrypt(bytes.NewReader(c), key)
		if err == nil {
			_, err = io.ReadAll(r)
		}
		if err == nil {
			t.Errorf("%s: read without an error", name)
		}
	}
	// Two files under one key cannot be spliced.
	other := sealAll(t, data, key, 1)
	spliced := append(append([]byte{}, sealed[:HeaderSize+ChunkSize+tagSize]...), other[HeaderSize+ChunkSize+tagSize:]...)
	if _, err := Open(spliced, key); !errors.Is(err, ErrDamaged) {
		t.Errorf("spliced: %v, want ErrDamaged", err)
	}
}

func flip(b []byte, i int) []byte {
	c := append([]byte{}, b...)
	c[i] ^= 1
	return c
}

func swapChunks(b []byte) []byte {
	c := append([]byte{}, b...)
	a := c[HeaderSize : HeaderSize+ChunkSize+tagSize]
	d := append([]byte{}, c[HeaderSize+ChunkSize+tagSize:HeaderSize+2*(ChunkSize+tagSize)]...)
	copy(c[HeaderSize+ChunkSize+tagSize:], a)
	copy(c[HeaderSize:], d)
	return c
}

func TestPlainSizeRefusesImpossibleSizes(t *testing.T) {
	for _, n := range []int64{0, 10, int64(HeaderSize), int64(HeaderSize) + tagSize - 1, int64(HeaderSize) + ChunkSize + tagSize + 5} {
		if _, err := PlainSize(n); err == nil {
			t.Errorf("PlainSize(%d) took a size no sealed stream has", n)
		}
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey("not a key"); err == nil {
		t.Error("took text that is no key")
	}
	if _, err := ParseKey("c2hvcnQ"); err == nil {
		t.Error("took a short key")
	}
	k := testKey(t)
	if len(KeyID(k)) != keyIDSize {
		t.Error("key id size")
	}
}

func FuzzDecrypt(f *testing.F) {
	key := make([]byte, KeySize)
	f.Add(sealAll(f, []byte("hello"), key, 1))
	f.Add(sealAll(f, make([]byte, ChunkSize+1), key, 1))
	f.Add([]byte("FRDSEAL1"))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Decrypt(bytes.NewReader(data), key)
		if err != nil {
			return
		}
		got, err := io.ReadAll(r)
		if err == nil {
			// Whatever opens must be what was sealed: its size matches.
			if p, perr := PlainSize(int64(len(data))); perr != nil || p != int64(len(got)) {
				t.Fatalf("opened %d bytes from %d sealed: %v", len(got), len(data), perr)
			}
		}
	})
}
