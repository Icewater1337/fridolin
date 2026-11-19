package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Icewater1337/fridolin/internal/chat"
	"github.com/Icewater1337/fridolin/internal/mcp"
	"github.com/Icewater1337/fridolin/internal/mcp/tools"
	"github.com/Icewater1337/fridolin/internal/media"
	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"
	"github.com/Icewater1337/fridolin/internal/skill"
	"github.com/Icewater1337/fridolin/internal/store"
)

// reply_file: a brain sends a file into the chat it answers (an image,
// a document or a voice note), from its folder, its media inbox or its
// temp folder, read there through os.Root as the connections' tools read
// a recording (openRunFile). The bridge keeps a copy of its own in the
// outbox's shelf, so the file goes out with the reply's retries and TTL
// whatever the brain does with its own meanwhile.

// maxCaption is the longest caption reply_file takes, in characters.
const maxCaption = 1024

// voiceNoteLimit bounds making a voice note with ffmpeg.
const voiceNoteLimit = 2 * time.Minute

func (b *App) registerReplyFileTool(r *tools.Set[mcpCaller]) {
	r.Register(mcp.Entry[mcpCaller]{
		Name:      "reply_file",
		Available: func(c mcpCaller) bool { return replyFileChat(c) != "" },
		Describe:  describeReplyFile,
		// Its caption leaves the machine for the chat.
		Call: r.GuardedOutbound(skill.RiskWrite, b.callReplyFile),
	})
}

func describeReplyFile(mcpCaller) mcp.Tool {
	return mcp.Tool{
		Description: "Send a file into the chat you answer: an image, a document (a PDF or any other file) or a voice note. It goes out ahead of your reply's text. " +
			"The file must be in your folder, your media inbox or your temp folder (such as audio you had read aloud), and at most max_media_mb. " +
			"It answers {\"queued\", \"as\", \"mime\", \"bytes\"}, and a note when it sends the file otherwise than asked.",
		InputSchema: tools.Schema(map[string]any{
			"path":    tools.Str("The file: an absolute path in your folder, your media inbox or your temp folder."),
			"caption": tools.Str(fmt.Sprintf("Text that goes with an image or a document, at most %d characters. A voice note has none: say it in your reply.", maxCaption)),
			"as": map[string]any{"type": "string", "enum": []string{chat.FileImage, chat.FileDocument, chat.FileVoice},
				"description": "image: a JPEG or PNG shown in the chat. document: a file to download, by its name. voice: audio as a voice note, made with ffmpeg unless it is Ogg Opus already; without ffmpeg it goes as a document. Left out, a JPEG or PNG is an image and anything else a document."},
			"approval_id": tools.ApprovalIDProp(),
		}, "path"),
	}
}

// replyFileChat is the chat a run's reply_file sends to: the chat its
// answer goes to, when that is a person's or a group's WhatsApp chat
// someone reads; "" for none (an observe-only run, a scheduled agent's
// or a delegated task's, a call's, whose answer is read aloud).
func replyFileChat(c mcpCaller) string {
	m := c.run.Message
	if m.Silent || m.AnswerOnly || c.run.Policy != nil && c.run.Policy.Silent {
		return ""
	}
	to := c.run.Chat
	if !chat.IsDirectJID(to) && !chat.IsGroupJID(to) {
		return ""
	}
	if _, err := parseRecipient(to); err != nil {
		return ""
	}
	return to
}

// ffmpegPath is the ffmpeg voice notes are made with, "" for none: from
// the helper folders, as rclone and tmux are found.
func (b *App) ffmpegPath() string {
	if b.hooks.ffmpeg != nil {
		return b.hooks.ffmpeg()
	}
	return lookPathIn("ffmpeg", helperPath())
}

func (b *App) callReplyFile(ctx context.Context, c mcpCaller, raw json.RawMessage) (*mcp.CallResult, error) {
	var args struct {
		Path       string `json:"path"`
		Caption    string `json:"caption"`
		As         string `json:"as"`
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return mcp.ErrorResult(`the arguments are {"path": "<absolute path>", "caption": "…", "as": "image|document|voice"}: ` + err.Error()), nil
	}
	to := replyFileChat(c)
	if to == "" {
		return mcp.ErrorResult("this run answers no chat a file can go to"), nil
	}
	switch args.As {
	case "", chat.FileImage, chat.FileDocument, chat.FileVoice:
	default:
		return mcp.ErrorResult("as is image, document or voice"), nil
	}
	caption := strings.TrimSpace(args.Caption)
	switch {
	case utf8.RuneCountInString(caption) > maxCaption:
		return mcp.ErrorResult(fmt.Sprintf("the caption is longer than %d characters: say the rest in your reply", maxCaption)), nil
	case caption != "" && args.As == chat.FileVoice:
		return mcp.ErrorResult("a voice note has no caption: say it in your reply"), nil
	}
	f, err := b.openRunFile(c.run.Brain, "path", args.Path)
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	defer f.Close()
	limit := int64(b.cfgs.Get().MaxMediaMB) << 20
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return mcp.ErrorResult(fmt.Sprintf("the file is %d MB, more than max_media_mb (%d MB)", (st.Size()+1<<20-1)>>20, limit>>20)), nil
	}
	head := make([]byte, min(st.Size(), media.SniffLen))
	if _, err := f.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	name := filepath.Base(args.Path)
	out := outgoing{name: name, mime: media.SniffType(name, head), kind: args.As}
	if out.kind == "" {
		out.kind = chat.FileDocument
		if isPhoto(out.mime) {
			out.kind = chat.FileImage
		}
	}
	if out.kind == chat.FileImage && !isPhoto(out.mime) {
		return mcp.ErrorResult(fmt.Sprintf("only a JPEG or PNG shows as an image, and this is %s: send it as a document", out.mime)), nil
	}
	shelf := b.outbox.Shelf()
	if out.kind == chat.FileVoice {
		err = b.shelveVoiceNote(ctx, shelf, f, head, limit, &out)
	} else {
		err = shelveCopy(shelf, f, limit, &out)
	}
	if err != nil {
		return mcp.ErrorResult(err.Error()), nil
	}
	file := store.OutboxFile{Blob: out.blob, Name: name, Mime: out.mime, Kind: out.kind, Seconds: out.seconds}
	if err := b.outbox.File(ctx, c.run.Brain, to, file, caption); err != nil {
		shelf.Remove(out.blob)
		return mcp.ErrorResult(err.Error()), nil
	}
	res := map[string]any{"queued": name, "as": out.kind, "mime": out.mime, "bytes": out.size}
	if out.note != "" {
		res["note"] = out.note
	}
	return jsonResult(res), nil
}

// outgoing is a file reply_file shelves: what it is, how it goes, and the
// blob of the bridge's copy.
type outgoing struct {
	name, mime, kind string
	blob             string
	size             int64
	seconds          uint32
	note             string
}

// isPhoto reports whether a file of MIME type t shows as an image.
func isPhoto(t string) bool { return t == "image/jpeg" || t == "image/png" }

// shelveCopy copies f, at most limit bytes, into a new blob of shelf.
func shelveCopy(shelf outboundmsg.Shelf, f *os.File, limit int64, out *outgoing) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	blob, name, err := shelf.Create(filepath.Ext(out.name))
	if err != nil {
		return fmt.Errorf("could not keep a copy of the file: %w", err)
	}
	n, err := io.Copy(blob, io.LimitReader(f, limit+1))
	if cerr := blob.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = fmt.Errorf("could not keep a copy of the file: %w", err)
	case n > limit:
		err = fmt.Errorf("the file is more than max_media_mb (%d MB)", limit>>20)
	}
	if err != nil {
		shelf.Remove(name)
		return err
	}
	out.blob, out.size = name, n
	return nil
}

// shelveVoiceNote shelves f as a voice note: as it is when it is Ogg
// Opus, else made one with ffmpeg; without ffmpeg, as a document.
func (b *App) shelveVoiceNote(ctx context.Context, shelf outboundmsg.Shelf, f *os.File, head []byte, limit int64, out *outgoing) error {
	if !media.IsOggOpus(head) {
		ffmpeg := b.ffmpegPath()
		if ffmpeg == "" {
			out.kind, out.note = chat.FileDocument, "ffmpeg is not installed on the bridge's machine, so it goes as a document, not a voice note"
			return shelveCopy(shelf, f, limit, out)
		}
		if err := b.convertVoiceNote(ctx, shelf, ffmpeg, f, limit, out); err != nil {
			return err
		}
	} else if err := shelveCopy(shelf, f, limit, out); err != nil {
		return err
	}
	path, err := shelf.Path(out.blob)
	if err == nil {
		out.seconds, err = oggSeconds(path)
	}
	if err != nil {
		shelf.Remove(out.blob)
		return fmt.Errorf("could not read the voice note's length: %w", err)
	}
	out.mime = "audio/ogg"
	return nil
}

// convertVoiceNote makes a voice note of f with ffmpeg into a new blob.
func (b *App) convertVoiceNote(ctx context.Context, shelf outboundmsg.Shelf, ffmpeg string, f *os.File, limit int64, out *outgoing) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	blob, name, err := shelf.Create(".ogg")
	if err != nil {
		return fmt.Errorf("could not keep a copy of the file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, voiceNoteLimit)
	defer cancel()
	err = media.ToVoiceNote(ctx, ffmpeg, f, blob)
	var size int64
	if st, serr := blob.Stat(); serr == nil {
		size = st.Size()
	}
	if cerr := blob.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		err = fmt.Errorf("could not make a voice note of it: %w", err)
	case size > limit:
		err = fmt.Errorf("the voice note is more than max_media_mb (%d MB)", limit>>20)
	}
	if err != nil {
		shelf.Remove(name)
		return err
	}
	out.blob, out.size = name, size
	return nil
}

// oggSeconds is how long the Ogg Opus file at path plays.
func oggSeconds(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return media.OggOpusSeconds(f, st.Size())
}
