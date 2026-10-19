package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Icewater1337/fridolin/internal/textx"
)

// File is the bounded random-access file supplied to a downloader. It
// deliberately exposes neither the underlying *os.File nor ReadFrom.
type File interface {
	io.Reader
	io.Writer
	io.Seeker
	io.ReaderAt
	io.WriterAt
	Truncate(int64) error
	Stat() (os.FileInfo, error)
}

// ErrTooLarge marks an attachment over max_media_mb.
var ErrTooLarge = errors.New("attachment is larger than max_media_mb")

// Save downloads a named attachment into Dir and returns where it was saved. It
// streams the download into a hidden temp file, so the attachment is never
// held in memory and a brain never sees a half-written file, and names the
// file only once it is complete. A failed download leaves nothing behind.
//
// A file over maxBytes is refused, before the download when the message
// declares its size, and as soon as it passes the limit otherwise: the
// declared size is only the sender's word.
func (in Inbox) Save(ctx context.Context, kind, name string, size uint64, maxBytes int64, download func(context.Context, File) error) (string, error) {
	dir := in.Dir
	if size > uint64(maxBytes) {
		return "", fmt.Errorf("%w: the %s is %d bytes, over the limit of %d", ErrTooLarge, kind, size, maxBytes)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("saving %s: %w", kind, err)
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", fmt.Errorf("saving %s: %w", kind, err)
	}
	defer os.Remove(tmp.Name()) // a failed download's; a saved one was renamed

	// The cap cancels ctx once the file passes it, so nothing more is
	// fetched for this attachment.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	f := &cappedFile{f: tmp, limit: maxBytes + CipherOverhead, stop: cancel}
	// whatsmeow does not rewind the file before it tries its next server.
	// When one fails partway, the next one's bytes follow the partial ones
	// and the download fails; for a file near the limit, it can pass the
	// cap instead, and the sender is wrongly told the file is too large.
	err = download(ctx, f)
	if err == nil && !f.over {
		// The cap leaves room for the encryption's padding and MAC, so check
		// the decrypted file too.
		if st, serr := tmp.Stat(); serr != nil {
			err = serr
		} else if st.Size() > maxBytes {
			f.over = true
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if f.over {
		return "", fmt.Errorf("%w: the %s passed the limit of %d bytes while downloading", ErrTooLarge, kind, maxBytes)
	}
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", kind, err)
	}
	return placeMedia(tmp.Name(), dir, name)
}

// CipherOverhead is how much larger than a file its encrypted download
// can be: up to 16 bytes of AES-CBC padding and a 10-byte MAC.
const CipherOverhead = 16 + 10

// cappedFile is the file whatsmeow downloads into. It refuses to grow past
// limit, and then calls stop. whatsmeow must not get the *os.File itself:
// it would set aside as much disk as the server announces before the cap
// could act. Nor may cappedFile embed it: io.Copy would use the file's
// ReadFrom and write around the cap.
type cappedFile struct {
	f     *os.File
	limit int64
	stop  func()
	over  bool
}

// refuse marks the file as over the cap and ends the download. Its error
// counts as a cancellation too: after any other error, whatsmeow would try
// its next server.
func (c *cappedFile) refuse() error {
	c.over = true
	c.stop()
	return fmt.Errorf("%w: %w", ErrTooLarge, context.Canceled)
}

func (c *cappedFile) Write(p []byte) (int, error) {
	pos, err := c.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if pos+int64(len(p)) > c.limit {
		return 0, c.refuse()
	}
	return c.f.Write(p)
}

func (c *cappedFile) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > c.limit {
		return 0, c.refuse()
	}
	return c.f.WriteAt(p, off)
}

func (c *cappedFile) Truncate(size int64) error {
	if size > c.limit {
		return c.refuse()
	}
	return c.f.Truncate(size)
}

func (c *cappedFile) Read(p []byte) (int, error)                { return c.f.Read(p) }
func (c *cappedFile) ReadAt(p []byte, off int64) (int, error)   { return c.f.ReadAt(p, off) }
func (c *cappedFile) Seek(off int64, whence int) (int64, error) { return c.f.Seek(off, whence) }
func (c *cappedFile) Stat() (os.FileInfo, error)                { return c.f.Stat() }

// placeMedia renames the finished download at tmp into dir. It first claims
// a free name with O_EXCL, which fails when anything is there, even a
// dangling symlink, so a file already there is neither overwritten nor
// followed. A taken name moves on to "-2", "-3" and so on, so two
// documents called "scan.pdf" both survive. The rename then replaces only
// the empty file it just created.
func placeMedia(tmp, dir, name string) (string, error) {
	name = sanitizeMediaName(name)
	for i := 1; ; i++ {
		p := filepath.Join(dir, mediaNameCandidate(name, i))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) && i < lastMediaCandidate {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("naming the download: %w", err)
		}
		f.Close()
		if err := os.Rename(tmp, p); err != nil {
			os.Remove(p)
			return "", fmt.Errorf("naming the download: %w", err)
		}
		return p, nil
	}
}

// lastMediaCandidate is the last name mediaNameCandidate offers.
const lastMediaCandidate = 1000

// mediaNameCandidate is the i-th name to try for a file called name: the name
// itself, then "scan-2.pdf" to "scan-999.pdf", then one the clock makes
// unique.
func mediaNameCandidate(name string, i int) string {
	if i <= 1 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if i < lastMediaCandidate {
		return fmt.Sprintf("%s-%d%s", stem, i, ext)
	}
	return fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext)
}

// maxMediaNameBytes caps the name of a saved file. File systems refuse names
// over 255 bytes, and a "-2" suffix must still fit.
const maxMediaNameBytes = 120

// sanitizeMediaName turns a sender-chosen file name into a safe base name in
// the media directory. Directories are dropped, so the name cannot escape it,
// and leading dots, so it is never hidden or "..". Control characters and
// line separators go too: the path is written into the prompt and into the
// line-based history ("[attachment: <path>]"), where a newline would start a
// line of its own, and a NUL makes the write fail. A name that is too long is
// shortened, keeping its extension, because saving it would fail and the
// attachment would be lost.
func sanitizeMediaName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, name)
	name = filepath.Base(filepath.Clean(name))
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "/" {
		return "file"
	}
	if len(name) <= maxMediaNameBytes {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = "" // not an extension, just a dot in a long name
	}
	// textx.Truncate appends "…" (3 bytes) and never splits a character.
	return textx.Truncate(strings.TrimSuffix(name, ext), maxMediaNameBytes-len(ext)-len("…")) + ext
}
