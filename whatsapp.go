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
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Messenger is what the bridge needs from a chat network. The real one is
// WhatsApp; dev mode (--no-whatsapp) swaps in a fake that records outgoing
// messages, so the dashboard and brains can be tried without pairing a phone.
type Messenger interface {
	Connect() error
	Disconnect()
	SendMessage(ctx context.Context, jid, text string) error
	SendChunked(ctx context.Context, jid, text string) error
	SendPoll(ctx context.Context, jid, question string, options []string, maxSelectable int) (string, error)
	State() WAState
	StartPairing() error
	JoinedGroups(ctx context.Context) ([]GroupInfo, error)
}

// WAState is the connection state shown on the dashboard.
type WAState struct {
	// Mode: disabled, connecting, pairing, qr-expired, connected,
	// disconnected, logged-out, replaced.
	Mode      string    `json:"mode"`
	OwnNumber string    `json:"own_number,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Since     time.Time `json:"since"`
	QRCode    string    `json:"-"` // current pairing code; served as an image, never as text
	QRAt      time.Time `json:"-"`
}

// GroupInfo is a group the bridge account belongs to.
type GroupInfo struct {
	JID          string `json:"jid"`
	Name         string `json:"name"`
	Participants int    `json:"participants"`
}

// InboundHandler receives every allowed message.
type InboundHandler func(ctx context.Context, m Inbound)

// IgnoredHandler is told about chats that wrote but are not allowed, so the
// dashboard can offer to allow them. It never receives message text.
type IgnoredHandler func(kind, jid, name, sender, pushName string)

// WhatsAppClient wraps a whatsmeow client.
type WhatsAppClient struct {
	client    *whatsmeow.Client
	cfg       func() *Config
	store     *Store
	handler   InboundHandler
	onIgnored IgnoredHandler
	log       waLog.Logger

	mu    sync.Mutex
	state WAState
}

// NewWhatsAppClient creates a client. It does NOT connect — call Connect.
func NewWhatsAppClient(cfg func() *Config, store *Store, handler InboundHandler, onIgnored IgnoredHandler) (*WhatsAppClient, error) {
	logger := newWaLogger("WhatsApp", cfg().LogLevel)

	deviceStore, err := store.Container.GetFirstDevice(context.Background())
	if err != nil {
		return nil, fmt.Errorf("getting device store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, logger)

	wa := &WhatsAppClient{
		client:    client,
		cfg:       cfg,
		store:     store,
		handler:   handler,
		onIgnored: onIgnored,
		log:       logger,
		state:     WAState{Mode: "connecting", Since: time.Now()},
	}

	client.AddEventHandler(wa.eventHandler)
	return wa, nil
}

func (wa *WhatsAppClient) setState(mode, detail string) {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	if wa.state.Mode != mode {
		wa.state.Since = time.Now()
	}
	wa.state.Mode, wa.state.Detail = mode, detail
	if mode != "pairing" {
		wa.state.QRCode = ""
	}
	if id := wa.client.Store.ID; id != nil {
		wa.state.OwnNumber = id.User
	}
}

// State returns the current connection state.
func (wa *WhatsAppClient) State() WAState {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	return wa.state
}

// Connect connects with the stored session, or starts QR pairing in the
// background when there is none. It does not block on pairing: the code is
// shown on the dashboard (and in the terminal, when there is one).
func (wa *WhatsAppClient) Connect() error {
	if wa.client.Store.ID != nil {
		wa.setState("connecting", "")
		if err := wa.client.Connect(); err != nil {
			return fmt.Errorf("connecting: %w", err)
		}
		return nil
	}
	return wa.StartPairing()
}

// StartPairing begins (or restarts) QR pairing for an unpaired bridge.
func (wa *WhatsAppClient) StartPairing() error {
	if wa.client.Store.ID != nil {
		return fmt.Errorf("already paired — this bridge has a stored WhatsApp session")
	}
	if wa.client.IsConnected() {
		wa.client.Disconnect()
	}
	qrChan, err := wa.client.GetQRChannel(context.Background())
	if err != nil {
		return fmt.Errorf("getting QR channel: %w", err)
	}
	if err := wa.client.Connect(); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	wa.setState("pairing", "Scan the code: WhatsApp → Settings → Linked Devices → Link a Device")

	tty := isTerminal(os.Stdout)
	go func() {
		for evt := range qrChan {
			switch evt.Event {
			case "code":
				wa.mu.Lock()
				wa.state.QRCode, wa.state.QRAt = evt.Code, time.Now()
				wa.mu.Unlock()
				if tty {
					fmt.Println("Scan this code with WhatsApp (Settings → Linked Devices → Link a Device),")
					fmt.Println("or open the dashboard:")
					qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
				} else {
					log.Printf("WhatsApp pairing: new QR code available on the dashboard")
				}
			case "success":
				wa.setState("connected", "Paired")
			case "timeout":
				wa.setState("qr-expired", "The pairing code expired — start pairing again from the dashboard")
			default:
				log.Printf("WhatsApp pairing: %s", evt.Event)
				if evt.Error != nil {
					wa.setState("qr-expired", evt.Error.Error())
				}
			}
		}
	}()
	return nil
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
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
	chunks := ChunkMessage(text, wa.cfg().MaxChunkSize)
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
func (wa *WhatsAppClient) SendPoll(ctx context.Context, jid string, question string, options []string, maxSelectable int) (string, error) {
	recipient, err := types.ParseJID(jid)
	if err != nil {
		return "", fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	msg := wa.client.BuildPollCreation(question, options, maxSelectable)
	resp, err := wa.client.SendMessage(ctx, recipient, msg)
	if err != nil {
		return "", err
	}
	return string(resp.ID), nil
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

// JoinedGroups lists the groups the bridge account is in.
func (wa *WhatsAppClient) JoinedGroups(ctx context.Context) ([]GroupInfo, error) {
	if !wa.client.IsLoggedIn() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}
	groups, err := wa.client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]GroupInfo, 0, len(groups))
	for _, g := range groups {
		out = append(out, GroupInfo{JID: g.JID.String(), Name: g.Name, Participants: len(g.Participants)})
	}
	return out, nil
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
		wa.setState("connected", "")
	case *events.PairSuccess:
		wa.log.Infof("Paired with WhatsApp as %s", evt.ID.User)
		wa.setState("connected", "Paired")
	case *events.Disconnected:
		wa.setState("disconnected", "Connection lost — reconnecting automatically")
	case *events.StreamReplaced:
		wa.log.Warnf("Another client took over this WhatsApp session")
		wa.setState("replaced", "Another program is using this WhatsApp session — is a second bridge running?")
	case *events.LoggedOut:
		wa.log.Warnf("Logged out from WhatsApp (reason %v)", evt.Reason)
		wa.setState("logged-out", "WhatsApp logged this device out. Remove it under Linked Devices on your phone, move bridge.db aside and pair again.")
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
		if wa.onIgnored != nil {
			wa.onIgnored("dm", msg.Info.Chat.String(), "", senderNumber, msg.Info.PushName)
		}
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

	go wa.handler(context.Background(), Inbound{
		JID: chatJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Source: "whatsapp", At: msg.Info.Timestamp,
	})
}

func mediaList(p string) []string {
	if p == "" {
		return nil
	}
	return []string{p}
}

// handleGroupMessage processes messages from allowed groups.
//
// A message is "tagged" — and so gets a visible reply — when it carries a file,
// matches a configured trigger keyword, @-mentions the bridge account, or is
// forwarded. Untagged text still runs through the brain so it stays aware of
// the conversation, but produces no reply.
func (wa *WhatsAppClient) handleGroupMessage(msg *events.Message) {
	cfg := wa.cfg()
	groupJID := msg.Info.Chat.String()
	senderNumber := msg.Info.Sender.User

	groupName := ""
	if info, err := wa.client.GetGroupInfo(context.Background(), msg.Info.Chat); err == nil && info != nil {
		groupName = info.Name
	}

	// Unknown groups are logged and offered on the dashboard: that is how you
	// find a group's JID to allow it.
	if !wa.isAllowedGroup(groupJID) {
		log.Printf("Group message from %s (%q) by %s: ignored (not in allowed_groups)", groupJID, groupName, senderNumber)
		if wa.onIgnored != nil {
			wa.onIgnored("group", groupJID, groupName, senderNumber, msg.Info.PushName)
		}
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

	textMatches := cfg.Triggers.Matches(text)
	captionMatches := cfg.Triggers.Matches(attachmentCaption)
	isTagged := hasFile || textMatches || captionMatches || wa.isBotMentioned(msg.Message) || isForwarded

	// Untagged plain text: run the brain, send nothing.
	silent := !isTagged && text != ""

	if captionMatches && !textMatches {
		text = attachmentCaption
	}
	text = cfg.Triggers.Strip(text)

	if ext := msg.Message.GetExtendedTextMessage(); ext != nil {
		if replyContext := extractReplyContext(ext.GetContextInfo()); replyContext != "" {
			text = replyContext + text
		}
	}

	mediaPath, caption := wa.downloadAttachment(msg, groupJID)
	if caption != "" {
		caption = cfg.Triggers.Strip(caption)
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
	go wa.handler(context.Background(), Inbound{
		JID: groupJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Silent: silent, Source: "whatsapp", At: msg.Info.Timestamp,
	})
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
// sanitised so a hostile filename cannot escape the media directory. An
// existing file is never overwritten: two documents called "scan.pdf" both
// survive.
func (wa *WhatsAppClient) mediaPath(name string) string {
	return safeMediaPath(wa.cfg().MediaDir, name)
}

func safeMediaPath(dir, name string) string {
	name = filepath.Base(filepath.Clean(name))
	name = strings.TrimLeft(name, ".")
	if name == "" || name == "/" {
		name = "file"
	}
	p := filepath.Join(dir, name)
	if _, err := os.Lstat(p); err != nil {
		return p
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		alt := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Lstat(alt); err != nil {
			return alt
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, time.Now().UnixNano(), ext))
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

// isAllowed reports whether the phone number may DM the bridge.
func (wa *WhatsAppClient) isAllowed(number string) bool {
	return numberAllowed(wa.cfg(), number)
}

func numberAllowed(cfg *Config, number string) bool {
	if len(cfg.AllowedNumbers) == 0 {
		return cfg.AllowsAnyDM()
	}
	for _, n := range cfg.AllowedNumbers {
		if n == number {
			return true
		}
	}
	return false
}

// isAllowedGroup reports whether the group JID is in allowed_groups.
// Unlike DMs, groups are deny-by-default: an empty list means no groups.
func (wa *WhatsAppClient) isAllowedGroup(jid string) bool {
	for _, g := range wa.cfg().AllowedGroups {
		if g == jid {
			return true
		}
	}
	return false
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

// ── dev mode ─────────────────────────────────────────────────────────────

// devMessenger stands in for WhatsApp in --no-whatsapp mode. Outgoing
// messages are kept in memory and shown on the dashboard; incoming ones are
// injected through the dev API.
type devMessenger struct {
	cfg    func() *Config
	mu     sync.Mutex
	outbox []DevMessage
}

// DevMessage is one message the bridge "sent" in dev mode.
type DevMessage struct {
	TS   time.Time `json:"ts"`
	JID  string    `json:"jid"`
	Text string    `json:"text"`
	Poll []string  `json:"poll,omitempty"`
}

func (d *devMessenger) Connect() error { return nil }
func (d *devMessenger) Disconnect()    {}
func (d *devMessenger) State() WAState {
	return WAState{Mode: "disabled", Detail: "Dev mode (--no-whatsapp): messages are simulated"}
}
func (d *devMessenger) StartPairing() error {
	return fmt.Errorf("WhatsApp is disabled in dev mode")
}
func (d *devMessenger) JoinedGroups(context.Context) ([]GroupInfo, error) { return nil, nil }

func (d *devMessenger) record(m DevMessage) {
	m.TS = time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.outbox = append(d.outbox, m)
	if len(d.outbox) > 200 {
		d.outbox = d.outbox[len(d.outbox)-200:]
	}
	log.Printf("[dev] → %s: %s", m.JID, truncate(m.Text, 200))
}

func (d *devMessenger) SendMessage(_ context.Context, jid, text string) error {
	d.record(DevMessage{JID: jid, Text: text})
	return nil
}

func (d *devMessenger) SendChunked(ctx context.Context, jid, text string) error {
	for _, c := range ChunkMessage(text, d.cfg().MaxChunkSize) {
		d.record(DevMessage{JID: jid, Text: c})
	}
	return nil
}

func (d *devMessenger) SendPoll(_ context.Context, jid, q string, opts []string, _ int) (string, error) {
	d.record(DevMessage{JID: jid, Text: q, Poll: opts})
	return fmt.Sprintf("dev-poll-%d", time.Now().UnixNano()), nil
}

// Outbox returns the recorded messages, newest first.
func (d *devMessenger) Outbox() []DevMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]DevMessage, 0, len(d.outbox))
	for i := len(d.outbox) - 1; i >= 0; i-- {
		out = append(out, d.outbox[i])
	}
	return out
}
