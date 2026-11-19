package whatsapp

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // thumbnails of PNG images
	"io"
	"os"
	"time"

	"github.com/Icewater1337/fridolin/internal/chat"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Files the bridge sends into a chat (a brain's reply_file): uploaded to
// WhatsApp, encrypted, then sent as an image, a document or a voice note.

// FileSender is what SendFile needs from whatsmeow.Client; tests set a
// fake.
type FileSender interface {
	UploadReader(ctx context.Context, plaintext io.Reader, tempFile io.ReadWriteSeeker, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
}

// fileSender is what files go out through.
func (wa *Client) fileSender() FileSender {
	if wa.files != nil {
		return wa.files
	}
	return wa.cl()
}

// SendFile uploads the file f names and sends it into the chat jid, as
// f.Kind says: an image (with a small preview), a document by its name,
// or a voice note.
func (wa *Client) SendFile(ctx context.Context, jid string, f chat.File) error {
	recipient, err := wa.outbound.Recipient(jid)
	if err != nil {
		return err
	}
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	var preview imagePreview
	if f.Kind == chat.FileImage {
		preview = previewOf(file)
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	up, err := wa.fileSender().UploadReader(ctx, file, nil, mediaTypeOf(f.Kind))
	if err != nil {
		return wa.classify(fmt.Errorf("uploading %s: %w", f.Name, err))
	}
	_, err = wa.fileSender().SendMessage(ctx, recipient, fileMessage(f, up, preview))
	return wa.classify(err)
}

// mediaTypeOf is the media type a file of kind is encrypted as.
func mediaTypeOf(kind string) whatsmeow.MediaType {
	switch kind {
	case chat.FileImage:
		return whatsmeow.MediaImage
	case chat.FileVoice:
		return whatsmeow.MediaAudio
	}
	return whatsmeow.MediaDocument
}

// voiceWaveform is a voice note's waveform: flat, which WhatsApp draws as
// a calm line, since the bridge does not measure the audio.
var voiceWaveform = bytes.Repeat([]byte{40}, 64)

// fileMessage is the message that sends f, uploaded as up.
func fileMessage(f chat.File, up whatsmeow.UploadResponse, preview imagePreview) *waE2E.Message {
	now := time.Now().Unix()
	switch f.Kind {
	case chat.FileImage:
		m := &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(f.Mime), MediaKeyTimestamp: proto.Int64(now),
			JPEGThumbnail: preview.thumbnail,
		}
		if f.Caption != "" {
			m.Caption = proto.String(f.Caption)
		}
		if preview.width > 0 {
			m.Width, m.Height = proto.Uint32(preview.width), proto.Uint32(preview.height)
		}
		return &waE2E.Message{ImageMessage: m}
	case chat.FileVoice:
		return &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String("audio/ogg; codecs=opus"), MediaKeyTimestamp: proto.Int64(now),
			PTT: proto.Bool(true), Seconds: proto.Uint32(f.Seconds), Waveform: voiceWaveform,
		}}
	}
	m := &waE2E.DocumentMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
		Mimetype: proto.String(f.Mime), MediaKeyTimestamp: proto.Int64(now),
		FileName: proto.String(f.Name), Title: proto.String(f.Name),
	}
	if f.Caption != "" {
		m.Caption = proto.String(f.Caption)
	}
	return &waE2E.Message{DocumentMessage: m}
}

// The preview of an image: a small JPEG the chat shows until the image
// is downloaded, and the image's size.
type imagePreview struct {
	thumbnail     []byte
	width, height uint32
}

// The bounds of a preview: its longest side, and the largest image the
// bridge decodes for one, so that a huge image cannot take its memory.
const (
	previewSide      = 72
	previewMaxPixels = 40_000_000
)

// previewOf is the preview of the JPEG or PNG image r holds; the zero
// value for one it cannot read or that is too large to decode, which
// then goes without a preview.
func previewOf(r io.ReadSeeker) imagePreview {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return imagePreview{}
	}
	p := imagePreview{width: uint32(cfg.Width), height: uint32(cfg.Height)}
	if cfg.Width*cfg.Height > previewMaxPixels {
		return p
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return p
	}
	img, _, err := image.Decode(r)
	if err != nil {
		return p
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, shrink(img, previewSide), &jpeg.Options{Quality: 60}); err == nil {
		p.thumbnail = out.Bytes()
	}
	return p
}

// shrink is img with its longest side side pixels at most, by nearest
// neighbour: enough for a preview.
func shrink(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return img
	}
	tw, th := side, max(1, h*side/w)
	if h > w {
		tw, th = max(1, w*side/h), side
	}
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		for x := range tw {
			out.Set(x, y, img.At(b.Min.X+x*w/tw, b.Min.Y+y*h/th))
		}
	}
	return out
}
