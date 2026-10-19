package whatsapp

import (
	"bytes"
	"context"

	"io"

	"sync"

	"go.mau.fi/whatsmeow"

	"go.mau.fi/whatsmeow/util/cbcutil"
)

// fakeDownloader stands in for whatsmeow's media download. Like whatsmeow,
// it writes the body into the file with io.Copy; then it fails with err, or,
// with block set, waits for the context to end. With hold set, a download
// stalls before it writes anything, until the test closes hold or the
// context ends.
//
// With encrypted set, it does what whatsmeow does with a real attachment:
// the body arrives AES-CBC encrypted with a MAC after it, up to
// mediaCipherOverhead bytes larger, and is then decrypted in place.
type fakeDownloader struct {
	body      string
	encrypted bool
	err       error
	block     bool
	hold      chan struct{}

	mu      sync.Mutex
	calls   int
	ctx     context.Context // of the last call
	sent    int64           // bytes the download carried
	copied  int64           // bytes the file took
	stopped bool            // the context ended while copying
}

// fakeMediaKey and fakeMediaIV encrypt the fake's attachments; whatsmeow
// derives both from the media key in the message.
var (
	fakeMediaKey = bytes.Repeat([]byte{7}, 32)
	fakeMediaIV  = bytes.Repeat([]byte{9}, 16)
)

func (d *fakeDownloader) DownloadToFile(ctx context.Context, _ whatsmeow.DownloadableMessage, f whatsmeow.File) error {
	wire := []byte(d.body)
	if d.encrypted {
		enc, err := cbcutil.Encrypt(fakeMediaKey, fakeMediaIV, wire[:len(wire):len(wire)])
		if err != nil {
			return err
		}
		wire = append(enc, make([]byte, fakeMACLength)...) // whatsmeow checks the MAC; the fake does not
	}
	d.mu.Lock()
	d.calls++
	d.ctx = ctx
	d.sent = int64(len(wire))
	d.mu.Unlock()
	if d.hold != nil {
		select {
		case <-d.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n, err := io.Copy(f, io.TeeReader(bytes.NewReader(wire), io.Discard))
	d.mu.Lock()
	d.copied, d.stopped = n, ctx.Err() != nil
	d.mu.Unlock()
	if err != nil {
		return err
	}
	if d.encrypted {
		if err := decryptInPlace(f, n); err != nil {
			return err
		}
	}
	if d.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return d.err
}

func (d *fakeDownloader) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}
