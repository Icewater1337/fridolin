package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// MessageHandler is the callback invoked for each incoming allowed message.
// mediaPath is non-empty when the message carried a downloaded file.
// silent=true means the message was untagged in a group: the brain still runs
// (so it keeps up with the conversation) but no reply is sent.
type MessageHandler func(ctx context.Context, jid, senderNumber, text string, mediaPath string, silent bool)

// WhatsAppClient wraps a whatsmeow client.
type WhatsAppClient struct {
	client  *whatsmeow.Client
	cfg     *Config
	store   *Store
	handler MessageHandler
	log     waLog.Logger
}

// NewWhatsAppClient creates a client. It does NOT connect — call Connect.
func NewWhatsAppClient(cfg *Config, store *Store, handler MessageHandler) (*WhatsAppClient, error) {
	logger := waLog.Stdout("WhatsApp", cfg.LogLevel, true)

	deviceStore, err := store.Container.GetFirstDevice(context.Background())
	if err != nil {
		return nil, fmt.Errorf("getting device store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, logger)

	wa := &WhatsAppClient{
		client:  client,
		cfg:     cfg,
		store:   store,
		handler: handler,
		log:     logger,
	}

	client.AddEventHandler(wa.eventHandler)
	return wa, nil
}

// Connect logs in (QR pairing on first run) and connects to WhatsApp.
func (wa *WhatsAppClient) Connect() error {
	if wa.client.Store.ID != nil {
		if err := wa.client.Connect(); err != nil {
			return fmt.Errorf("connecting: %w", err)
		}
		return nil
	}

	// No stored session — pair via QR code.
	qrChan, err := wa.client.GetQRChannel(context.Background())
	if err != nil {
		return fmt.Errorf("getting QR channel: %w", err)
	}

	if err := wa.client.Connect(); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}

	fmt.Println("Scan the QR code below with WhatsApp on your phone")
	fmt.Println("(Settings → Linked Devices → Link a Device):")
	pngPath := filepath.Join(wa.cfg.DataDir, "qrcode.png")
	for evt := range qrChan {
		if evt.Event == "code" {
			qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			// Also save a PNG, for when the bridge runs without a terminal.
			if err := qrcode.WriteFile(evt.Code, qrcode.Medium, 512, pngPath); err != nil {
				wa.log.Warnf("Failed to write QR PNG to %s: %v", pngPath, err)
			} else {
				fmt.Printf("QR code PNG also saved to: %s\n", pngPath)
			}
		} else {
			wa.log.Infof("QR channel event: %s", evt.Event)
		}
	}

	// Pairing is done; the QR image is no longer useful and is a live
	// credential until it expires, so don't leave it lying around.
	_ = os.Remove(pngPath)
	return nil
}

// Disconnect disconnects the client.
func (wa *WhatsAppClient) Disconnect() {
	wa.client.Disconnect()
}

// SendMessage sends a plain-text WhatsApp message to the given JID.
func (wa *WhatsAppClient) SendMessage(ctx context.Context, jid string, text string) error {
	recipient, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	_, err = wa.client.SendMessage(ctx, recipient, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return err
}

// SendChunked splits text into chunks and sends each as a separate message.
func (wa *WhatsAppClient) SendChunked(ctx context.Context, jid string, text string) error {
	chunks := ChunkMessage(text, wa.cfg.MaxChunkSize)
	for _, chunk := range chunks {
		if err := wa.SendMessage(ctx, jid, chunk); err != nil {
			return err
		}
		// Small delay between chunks so they arrive in order.
		if len(chunks) > 1 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return nil
}

// SendPoll sends a WhatsApp poll and returns the message ID.
// maxSelectable is the max options a voter can pick (1 = single choice, 0 = unlimited).
func (wa *WhatsAppClient) SendPoll(ctx context.Context, jid string, question string, options []string, maxSelectable int) (types.MessageID, error) {
	recipient, err := types.ParseJID(jid)
	if err != nil {
		return "", fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	msg := wa.client.BuildPollCreation(question, options, maxSelectable)
	resp, err := wa.client.SendMessage(ctx, recipient, msg)
	if err != nil {
		return "", err
	}
	return resp.ID, nil
}

// SendReaction sends an emoji reaction to a message.
func (wa *WhatsAppClient) SendReaction(ctx context.Context, jid string, msgID types.MessageID, emoji string) error {
	recipient, err := types.ParseJID(jid)
	if err != nil {
		return fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	_, err = wa.client.SendMessage(ctx, recipient, &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key: &waCommon.MessageKey{
				RemoteJID: proto.String(jid),
				ID:        proto.String(string(msgID)),
			},
			Text:              proto.String(emoji),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	})
	return err
}

// eventHandler handles all whatsmeow events.
func (wa *WhatsAppClient) eventHandler(rawEvt interface{}) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		if evt.Message.GetPollUpdateMessage() != nil {
			wa.handlePollVote(evt)
		} else {
			wa.handleMessage(evt)
		}
	case *events.Connected:
		wa.log.Infof("Connected to WhatsApp")
	case *events.LoggedOut:
		wa.log.Warnf("Logged out from WhatsApp — delete the database and re-pair to reconnect")
	}
}

// handlePollVote decrypts a poll vote and stores the voter's selections.
func (wa *WhatsAppClient) handlePollVote(evt *events.Message) {
	ctx := context.Background()

	pollVote, err := wa.client.DecryptPollVote(ctx, evt)
	if err != nil {
		wa.log.Errorf("Failed to decrypt poll vote from %s: %v", evt.Info.Sender, err)
		return
	}

	pollMsgID := evt.Message.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()
	if pollMsgID == "" {
		wa.log.Warnf("Poll vote has no creation message key — skipping")
		return
	}

	_, options, err := wa.store.GetPollOptions(pollMsgID)
	if err != nil {
		// Not necessarily an error: the poll may predate vote tracking.
		log.Printf("Poll vote for unknown poll %s (not in DB) — skipping", pollMsgID)
		return
	}

	// WhatsApp sends SHA-256 hashes of the chosen option names, not the names.
	var selected []string
	for _, hash := range pollVote.GetSelectedOptions() {
		for _, opt := range options {
			h := sha256.Sum256([]byte(opt))
			if bytes.Equal(hash, h[:]) {
				selected = append(selected, opt)
				break
			}
		}
	}

	voterJID := evt.Info.Sender.String()
	log.Printf("Poll vote recorded: poll=%s voter=%s options=%v", pollMsgID, voterJID, selected)

	if err := wa.store.SavePollVote(pollMsgID, voterJID, selected); err != nil {
		wa.log.Errorf("Failed to save poll vote from %s: %v", voterJID, err)
	}
}

func (wa *WhatsAppClient) handleMessage(msg *events.Message) {
	if msg.Info.Chat.Server == "g.us" {
		// Group messages sent from the operator's own phone are still worth
		// processing (IsFromMe is true for any device on the account), so the
		// group path does its own gating.
		wa.handleGroupMessage(msg)
		return
	}

	// DMs: IsFromMe means this account sent it, i.e. it's our own reply echoing
	// back. Anything else is an inbound DM and must pass the allowlist.
	if msg.Info.IsFromMe {
		return
	}

	senderJID := msg.Info.Sender
	senderNumber := senderJID.User
	// Newer WhatsApp clients address senders by LID rather than phone number;
	// resolve it so the allowlist can be written in plain phone numbers.
	if senderJID.Server == "lid" {
		if pnJID, err := wa.client.Store.LIDs.GetPNForLID(context.Background(), senderJID); err == nil {
			senderNumber = pnJID.User
		} else {
			log.Printf("DM: could not resolve LID %s: %v (using LID as fallback)", senderJID, err)
		}
	}

	if !wa.isAllowed(senderNumber) {
		log.Printf("DM from %s: ignored (not in allowed_numbers)", senderNumber)
		return
	}

	wa.handleDMMessage(msg, senderNumber)
}

// handleDMMessage handles an allowed direct message.
func (wa *WhatsAppClient) handleDMMessage(msg *events.Message, senderNumber string) {
	chatJID := msg.Info.Chat.String()
	text := extractText(msg.Message)

	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		if replyContext := extractReplyContext(ext.GetContextInfo()); replyContext != "" {
			text = replyContext + text
		}
	}

	mediaPath, caption := wa.downloadAttachment(msg, chatJID)
	if caption != "" {
		if text == "" {
			text = caption
		} else {
			text = caption + "\n\n" + text
		}
	}

	if text == "" && mediaPath == "" {
		return
	}

	go wa.handler(context.Background(), chatJID, senderNumber, text, mediaPath, false)
}

// handleGroupMessage processes messages from allowed groups.
//
// A message is "tagged" — and so gets a visible reply — when it carries a file,
// matches a configured trigger keyword, @-mentions the bridge account, or is
// forwarded. Untagged text still runs through the brain so it stays aware of
// the conversation, but produces no reply.
func (wa *WhatsAppClient) handleGroupMessage(msg *events.Message) {
	groupJID := msg.Info.Chat.String()
	senderNumber := msg.Info.Sender.User

	groupName := ""
	if info, err := wa.client.GetGroupInfo(context.Background(), msg.Info.Chat); err == nil && info != nil {
		groupName = info.Name
	}

	// Always log unknown groups: this is how you discover a group's JID for
	// allowed_groups.
	if !wa.isAllowedGroup(groupJID) {
		log.Printf("Group message from %s (%q) by %s: ignored (not in allowed_groups)", groupJID, groupName, senderNumber)
		return
	}

	text := extractText(msg.Message)

	// GIF-playback videos are WhatsApp GIF reactions, not content: they must
	// not count as a file upload.
	hasVideo := false
	if vid := msg.Message.GetVideoMessage(); vid != nil && !vid.GetGifPlayback() {
		hasVideo = true
	}
	hasFile := msg.Message.GetImageMessage() != nil ||
		msg.Message.GetDocumentMessage() != nil ||
		msg.Message.GetAudioMessage() != nil ||
		hasVideo

	// Image and document captions can carry the trigger. Video captions cannot:
	// they are metadata of the video, and the video itself is already a trigger.
	attachmentCaption := ""
	if img := msg.Message.GetImageMessage(); img != nil {
		attachmentCaption = img.GetCaption()
	} else if doc := msg.Message.GetDocumentMessage(); doc != nil {
		attachmentCaption = doc.GetCaption()
	}

	isForwarded := false
	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		if ci := ext.GetContextInfo(); ci != nil {
			isForwarded = ci.GetIsForwarded() || ci.GetForwardingScore() > 0
		}
	}

	textMatches := matchesTrigger(text, wa.cfg.Triggers)
	captionMatches := matchesTrigger(attachmentCaption, wa.cfg.Triggers)
	isTagged := hasFile || textMatches || captionMatches || wa.isBotMentioned(msg.Message) || isForwarded

	// Untagged plain text: run the brain, send nothing.
	silent := !isTagged && text != ""

	if captionMatches && !textMatches {
		text = attachmentCaption
	}
	text = stripTriggers(text, wa.cfg.Triggers)

	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		if replyContext := extractReplyContext(ext.GetContextInfo()); replyContext != "" {
			text = replyContext + text
		}
	}

	mediaPath, caption := wa.downloadAttachment(msg, groupJID)
	if caption != "" {
		caption = stripTriggers(caption, wa.cfg.Triggers)
		if text == "" {
			text = caption
		} else if caption != text {
			text = caption + "\n\n" + text
		}
	}

	if text == "" && mediaPath == "" {
		return
	}

	log.Printf("Group message from %s (%q) by %s: processing (silent=%v)", groupJID, groupName, senderNumber, silent)
	go wa.handler(context.Background(), groupJID, senderNumber, text, mediaPath, silent)
}

// downloadAttachment downloads whichever attachment the message carries and
// returns its local path plus the attachment's caption (both may be empty).
func (wa *WhatsAppClient) downloadAttachment(msg *events.Message, chatJID string) (path string, caption string) {
	var err error

	switch {
	case msg.Message.GetImageMessage() != nil:
		img := msg.Message.GetImageMessage()
		path, err = wa.downloadImage(img, msg.Info.ID)
		caption = img.GetCaption()

	case msg.Message.GetDocumentMessage() != nil:
		doc := msg.Message.GetDocumentMessage()
		path, err = wa.downloadDocument(doc, msg.Info.ID)
		caption = doc.GetCaption()

	case msg.Message.GetAudioMessage() != nil:
		path, err = wa.downloadAudio(msg.Message.GetAudioMessage(), msg.Info.ID)

	case msg.Message.GetVideoMessage() != nil && !msg.Message.GetVideoMessage().GetGifPlayback():
		vid := msg.Message.GetVideoMessage()
		path, err = wa.downloadVideo(vid, msg.Info.ID)
		caption = vid.GetCaption()

	default:
		return "", ""
	}

	if err != nil {
		wa.log.Errorf("Failed to download attachment from %s: %v", chatJID, err)
		return "", caption
	}
	return path, caption
}

// mediaPath builds a safe destination path inside MediaDir. The base name is
// sanitised so a hostile filename cannot escape the media directory.
func (wa *WhatsAppClient) mediaPath(name string) string {
	name = filepath.Base(filepath.Clean(name))
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "/" {
		name = "file"
	}
	return filepath.Join(wa.cfg.MediaDir, name)
}

func (wa *WhatsAppClient) downloadImage(img *waE2E.ImageMessage, msgID types.MessageID) (string, error) {
	data, err := wa.client.Download(context.Background(), img)
	if err != nil {
		return "", fmt.Errorf("downloading image: %w", err)
	}

	ext := ".jpg"
	switch img.GetMimetype() {
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}

	path := wa.mediaPath(string(msgID) + ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing image to %q: %w", path, err)
	}
	return path, nil
}

func (wa *WhatsAppClient) downloadDocument(doc *waE2E.DocumentMessage, msgID types.MessageID) (string, error) {
	data, err := wa.client.Download(context.Background(), doc)
	if err != nil {
		return "", fmt.Errorf("downloading document: %w", err)
	}

	// Prefer the sender's filename, but fall back to the message ID plus an
	// extension derived from the MIME type.
	filename := doc.GetFileName()
	if filename == "" {
		ext := ".bin"
		switch doc.GetMimetype() {
		case "application/pdf":
			ext = ".pdf"
		case "application/msword":
			ext = ".doc"
		case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
			ext = ".docx"
		case "application/vnd.ms-excel":
			ext = ".xls"
		case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
			ext = ".xlsx"
		case "text/plain":
			ext = ".txt"
		case "text/csv":
			ext = ".csv"
		}
		filename = string(msgID) + ext
	}

	path := wa.mediaPath(filename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing document to %q: %w", path, err)
	}
	return path, nil
}

func (wa *WhatsAppClient) downloadAudio(audio *waE2E.AudioMessage, msgID types.MessageID) (string, error) {
	data, err := wa.client.Download(context.Background(), audio)
	if err != nil {
		return "", fmt.Errorf("downloading audio: %w", err)
	}

	ext := ".ogg"
	mimetype := audio.GetMimetype()
	if strings.Contains(mimetype, "mp4") || strings.Contains(mimetype, "m4a") {
		ext = ".m4a"
	} else if strings.Contains(mimetype, "mpeg") {
		ext = ".mp3"
	}

	path := wa.mediaPath(string(msgID) + ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing audio to %q: %w", path, err)
	}
	return path, nil
}

func (wa *WhatsAppClient) downloadVideo(vid *waE2E.VideoMessage, msgID types.MessageID) (string, error) {
	data, err := wa.client.Download(context.Background(), vid)
	if err != nil {
		return "", fmt.Errorf("downloading video: %w", err)
	}

	ext := ".mp4"
	mimetype := vid.GetMimetype()
	if strings.Contains(mimetype, "webm") {
		ext = ".webm"
	} else if strings.Contains(mimetype, "quicktime") || strings.Contains(mimetype, "mov") {
		ext = ".mov"
	} else if strings.Contains(mimetype, "3gpp") {
		ext = ".3gp"
	}

	path := wa.mediaPath(string(msgID) + ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("writing video to %q: %w", path, err)
	}
	return path, nil
}

// isAllowed reports whether the phone number is in the DM allowlist.
// An empty allowlist allows everyone — see the security notes in the README.
func (wa *WhatsAppClient) isAllowed(number string) bool {
	if len(wa.cfg.AllowedNumbers) == 0 {
		return true
	}
	for _, n := range wa.cfg.AllowedNumbers {
		if n == number {
			return true
		}
	}
	return false
}

// isAllowedGroup reports whether the group JID is in allowed_groups.
// Unlike DMs, groups are deny-by-default: an empty list means no groups.
func (wa *WhatsAppClient) isAllowedGroup(jid string) bool {
	for _, g := range wa.cfg.AllowedGroups {
		if g == jid {
			return true
		}
	}
	return false
}

// matchesTrigger reports whether text contains any configured trigger keyword,
// matched case-insensitively.
func matchesTrigger(text string, triggers []string) bool {
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, t := range triggers {
		if t == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// stripTriggers removes every occurrence of every trigger keyword from text,
// case-insensitively, and trims the result.
func stripTriggers(text string, triggers []string) string {
	for _, t := range triggers {
		if t == "" {
			continue
		}
		lowerT := strings.ToLower(t)
		for {
			idx := strings.Index(strings.ToLower(text), lowerT)
			if idx < 0 {
				break
			}
			text = text[:idx] + text[idx+len(t):]
		}
	}
	return strings.TrimSpace(text)
}

// isBotMentioned reports whether the bridge's own JID appears in the message's
// mention list, i.e. someone @-tagged the bridge account by name.
func (wa *WhatsAppClient) isBotMentioned(msg *waE2E.Message) bool {
	if msg == nil || wa.client.Store.ID == nil {
		return false
	}
	ownJIDs := wa.ownMentionJIDs()
	if len(ownJIDs) == 0 {
		return false
	}

	// Mentions usually ride on ExtendedTextMessage, but some clients attach
	// ContextInfo to the media message instead.
	var mentioned []string
	collect := func(ci *waE2E.ContextInfo) {
		if ci != nil {
			mentioned = append(mentioned, ci.GetMentionedJID()...)
		}
	}
	collect(msg.GetExtendedTextMessage().GetContextInfo())
	collect(msg.GetImageMessage().GetContextInfo())
	collect(msg.GetVideoMessage().GetContextInfo())
	collect(msg.GetDocumentMessage().GetContextInfo())
	collect(msg.GetAudioMessage().GetContextInfo())

	for _, mentionedJID := range mentioned {
		parsed, err := types.ParseJID(mentionedJID)
		if err != nil {
			continue
		}
		parsed = parsed.ToNonAD()
		for _, own := range ownJIDs {
			if parsed == own {
				return true
			}
		}
	}
	return false
}

// ownMentionJIDs returns every JID form the bridge account may be mentioned as.
// WhatsApp emits either the phone-number JID or the linked LID form.
func (wa *WhatsAppClient) ownMentionJIDs() []types.JID {
	if wa.client.Store.ID == nil {
		return nil
	}

	ctx := context.Background()
	seen := make(map[string]struct{}, 2)
	out := make([]types.JID, 0, 2)
	add := func(jid types.JID) {
		jid = jid.ToNonAD()
		if jid.User == "" || jid.Server == "" {
			return
		}
		key := jid.String()
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, jid)
	}

	add(*wa.client.Store.ID)
	if alt, err := wa.client.Store.GetAltJID(ctx, *wa.client.Store.ID); err == nil {
		add(alt)
	}

	return out
}

// extractReplyContext renders quoted-message context as a short prefix line,
// so the model can see what a reply is replying to.
func extractReplyContext(contextInfo *waE2E.ContextInfo) string {
	if contextInfo == nil {
		return ""
	}

	quotedText := extractText(contextInfo.GetQuotedMessage())
	if quotedText == "" {
		return ""
	}

	senderJID := contextInfo.GetParticipant()
	if senderJID == "" {
		return ""
	}
	if idx := strings.Index(senderJID, "@"); idx > 0 {
		senderJID = senderJID[:idx]
	}

	const maxQuoteLen = 200
	if len(quotedText) > maxQuoteLen {
		quotedText = truncate(quotedText, maxQuoteLen)
	}

	return fmt.Sprintf("[reply to %s: %q]\n", senderJID, quotedText)
}

// extractText returns the plain text content of a message.
func extractText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if t := msg.GetConversation(); t != "" {
		return t
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}
