package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// fakeFileSender records what SendFile uploads and sends.
type fakeFileSender struct {
	uploadErr error
	uploaded  []byte
	as        whatsmeow.MediaType
	to        types.JID
	sent      *waE2E.Message
}

func (f *fakeFileSender) UploadReader(_ context.Context, r io.Reader, _ io.ReadWriteSeeker, as whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	if f.uploadErr != nil {
		return whatsmeow.UploadResponse{}, f.uploadErr
	}
	f.uploaded, _ = io.ReadAll(r)
	f.as = as
	return whatsmeow.UploadResponse{URL: "https://mmg.whatsapp.net/u", DirectPath: "/v/u", MediaKey: []byte("key"), FileSHA256: []byte("sha"), FileEncSHA256: []byte("enc"), FileLength: uint64(len(f.uploaded))}, nil
}

func (f *fakeFileSender) SendMessage(_ context.Context, to types.JID, m *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.to, f.sent = to, m
	return whatsmeow.SendResponse{}, nil
}

// pngOf is a w×h PNG image.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, 0, color.RGBA{R: 200, A: 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// newFileTestWhatsApp is newTestWhatsApp with a fake for what goes to
// WhatsApp, which refuses a Status as the bridge does.
func newFileTestWhatsApp(t *testing.T) (*testWhatsApp, *fakeFileSender) {
	t.Helper()
	tw := newTestWhatsApp(t)
	f := &fakeFileSender{}
	tw.files = f
	tw.outbound.Recipient = func(jid string) (types.JID, error) {
		to, err := types.ParseJID(jid)
		if err == nil && to == types.StatusBroadcastJID {
			err = errors.New("refused")
		}
		return to, err
	}
	return tw, f
}

// A file goes out as its kind says: an image with its size and a small
// JPEG preview, a document by its name, a voice note as a played voice
// message with its length; each uploaded as the media type it is sent as.
func TestSendFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	photo := pngOf(t, 400, 100)
	ctx := context.Background()

	tw, f := newFileTestWhatsApp(t)
	err := tw.SendFile(ctx, testDMChat, chat.File{Path: write("chart.png", photo), Name: "chart.png", Mime: "image/png", Kind: chat.FileImage, Caption: "Sales"})
	if err != nil {
		t.Fatal(err)
	}
	img := f.sent.GetImageMessage()
	if f.to.String() != testDMChat || f.as != whatsmeow.MediaImage || !bytes.Equal(f.uploaded, photo) || img == nil {
		t.Fatalf("sent %v as %q to %s", f.sent, f.as, f.to)
	}
	if img.GetCaption() != "Sales" || img.GetMimetype() != "image/png" || img.GetWidth() != 400 || img.GetHeight() != 100 ||
		img.GetURL() != "https://mmg.whatsapp.net/u" || img.GetDirectPath() != "/v/u" || img.GetFileLength() != uint64(len(photo)) {
		t.Errorf("image = %v", img)
	}
	thumb, err := jpeg.Decode(bytes.NewReader(img.GetJPEGThumbnail()))
	if err != nil || thumb.Bounds().Dx() != previewSide || thumb.Bounds().Dy() != previewSide/4 {
		t.Errorf("preview: %v, %v", thumb, err)
	}

	err = tw.SendFile(ctx, testGroup, chat.File{Path: write("report.pdf", []byte("%PDF-1.7")), Name: "report.pdf", Mime: "application/pdf", Kind: chat.FileDocument})
	doc := f.sent.GetDocumentMessage()
	if err != nil || f.as != whatsmeow.MediaDocument || doc == nil || doc.GetFileName() != "report.pdf" || doc.GetTitle() != "report.pdf" ||
		doc.GetMimetype() != "application/pdf" || doc.Caption != nil || f.to.String() != testGroup {
		t.Errorf("document = %v, %v", doc, err)
	}

	err = tw.SendFile(ctx, testDMChat, chat.File{Path: write("hello.ogg", []byte("OggS")), Name: "hello.ogg", Mime: "audio/ogg", Kind: chat.FileVoice, Seconds: 7})
	voice := f.sent.GetAudioMessage()
	if err != nil || f.as != whatsmeow.MediaAudio || voice == nil || !voice.GetPTT() || voice.GetSeconds() != 7 ||
		voice.GetMimetype() != "audio/ogg; codecs=opus" || len(voice.GetWaveform()) != 64 {
		t.Errorf("voice note = %v, %v", voice, err)
	}

	// An image it cannot read goes without a preview.
	err = tw.SendFile(ctx, testDMChat, chat.File{Path: write("odd.jpg", []byte("\xff\xd8\xffnot really")), Name: "odd.jpg", Mime: "image/jpeg", Kind: chat.FileImage})
	if img := f.sent.GetImageMessage(); err != nil || img == nil || img.JPEGThumbnail != nil || img.Width != nil {
		t.Errorf("an odd image = %v, %v", img, err)
	}
}

// What cannot go out says why: a refused chat, a missing file, an upload
// WhatsApp refused.
func TestSendFileFails(t *testing.T) {
	tw, f := newFileTestWhatsApp(t)
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(p, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := chat.File{Path: p, Name: "a.pdf", Mime: "application/pdf", Kind: chat.FileDocument}
	if err := tw.SendFile(ctx, "status@broadcast", doc); err == nil {
		t.Error("sent to a Status")
	}
	missing := doc
	missing.Path = p + ".gone"
	if err := tw.SendFile(ctx, testDMChat, missing); err == nil {
		t.Error("sent a missing file")
	}
	f.uploadErr = errors.New("media conn refused")
	if err := tw.SendFile(ctx, testDMChat, doc); err == nil || f.sent != nil {
		t.Errorf("an upload that failed: %v, sent %v", err, f.sent)
	}
}

// A preview keeps the image's shape, its longest side previewSide at
// most; a small image stays as it is.
func TestShrink(t *testing.T) {
	for _, c := range []struct{ w, h, tw, th int }{{400, 100, 72, 18}, {100, 400, 18, 72}, {50, 30, 50, 30}, {1000, 1, 72, 1}} {
		got := shrink(image.NewRGBA(image.Rect(0, 0, c.w, c.h)), previewSide).Bounds()
		if got.Dx() != c.tw || got.Dy() != c.th {
			t.Errorf("%dx%d shrank to %dx%d, want %dx%d", c.w, c.h, got.Dx(), got.Dy(), c.tw, c.th)
		}
	}
}
