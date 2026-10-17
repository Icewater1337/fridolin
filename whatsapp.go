package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

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
	media     mediaDownloader        // the client; tests swap in a fake
	notify    func(jid, text string) // sendNotice; tests record instead

	// ctx ends when the client disconnects for good. Attachment downloads
	// run under it, so shutting down does not wait for them.
	ctx    context.Context
	cancel context.CancelFunc

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

	ctx, cancel := context.WithCancel(context.Background())
	wa := &WhatsAppClient{
		client:    client,
		cfg:       cfg,
		store:     store,
		handler:   handler,
		onIgnored: onIgnored,
		log:       logger,
		media:     client,
		ctx:       ctx,
		cancel:    cancel,
		state:     WAState{Mode: "connecting", Since: time.Now()},
	}
	wa.notify = wa.sendNotice

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

// Disconnect disconnects the client for good: it also ends the attachment
// downloads still running.
func (wa *WhatsAppClient) Disconnect() {
	wa.cancel()
	wa.client.Disconnect()
}

// errRefusedRecipient marks a send the bridge refuses because of where it
// would go.
var errRefusedRecipient = errors.New("refusing to send to a Status, broadcast list or channel")

// checkRecipient refuses Status, broadcast-list and channel addresses. A send
// to status@broadcast posts this account's own Status to every contact, and
// one to a newsletter posts in a public channel the account runs. whatsmeow
// cannot send to other broadcast lists yet; once it can, a send would reach
// every member. Nothing the bridge says belongs there, whoever asked for it:
// a brain's reply, its reply_chat, or a script calling /send.
func checkRecipient(jid types.JID) error {
	if jid.Server == types.BroadcastServer || jid.Server == types.NewsletterServer {
		return fmt.Errorf("%w: %s", errRefusedRecipient, jid)
	}
	return nil
}

// parseRecipient parses the JID of a chat to send to and refuses the ones
// checkRecipient refuses.
func parseRecipient(jid string) (types.JID, error) {
	recipient, err := types.ParseJID(jid)
	if err != nil {
		return types.JID{}, fmt.Errorf("parsing JID %q: %w", jid, err)
	}
	if err := checkRecipient(recipient); err != nil {
		return types.JID{}, err
	}
	return recipient, nil
}

// SendMessage sends a plain-text WhatsApp message to the given JID.
func (wa *WhatsAppClient) SendMessage(ctx context.Context, jid string, text string) error {
	recipient, err := parseRecipient(jid)
	if err != nil {
		return err
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
	recipient, err := parseRecipient(jid)
	if err != nil {
		return "", err
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
	recipient, err := parseRecipient(jid)
	if err != nil {
		return err
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

// incomingChat decides, before any other work, whether an incoming message
// belongs to a chat the bridge handles at all. A contact's Status update
// arrives in status@broadcast: answering it there would post the reply as
// this account's own Status, visible to every contact. Newsletters are
// public channels. Both are dropped.
//
// A message a contact sent to one of their broadcast lists shows up on the
// phone in the direct chat with that contact, so it is moved there and
// handled as a DM from the sender. The list itself is never a chat to
// answer in. Our own broadcast-list messages echoing back are dropped, like
// any message this account sent.
func incomingChat(src types.MessageSource) (chat types.JID, ok bool) {
	if src.Chat.Server == types.NewsletterServer || src.Chat.ToNonAD() == types.StatusBroadcastJID {
		return types.JID{}, false
	}
	if src.Chat.Server == types.BroadcastServer {
		if src.IsFromMe || src.Sender.IsEmpty() {
			return types.JID{}, false
		}
		return src.Sender.ToNonAD(), true
	}
	return src.Chat, true
}

func (wa *WhatsAppClient) handleMessage(msg *events.Message) {
	chat, ok := incomingChat(msg.Info.MessageSource)
	if !ok {
		wa.log.Debugf("Ignoring message %s in %s (Status updates, channels and own broadcasts are not handled)", msg.Info.ID, msg.Info.Chat)
		return
	}
	if chat != msg.Info.Chat {
		// A broadcast-list message, now a DM. Work on a copy: the event
		// belongs to whatsmeow.
		dm := *msg
		dm.Info.Chat, dm.Info.IsGroup = chat, false
		msg = &dm
	}

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
	quoted := quoteOf(msg.Message)

	mediaPath, caption, notice := wa.downloadAttachment(msg, chatJID)
	if notice != "" {
		go wa.notify(chatJID, notice)
	}
	if caption != "" {
		if text == "" {
			text = caption
		} else {
			text = caption + "\n\n" + text
		}
	}

	if text == "" && mediaPath == "" && quoted == nil {
		return
	}

	go wa.handler(context.Background(), Inbound{
		JID: chatJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Quoted: quoted, Source: "whatsapp", At: msg.Info.Timestamp,
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
	isMentioned := wa.isBotMentioned(msg.Message)
	isTagged := hasFile || textMatches || captionMatches || isMentioned || isForwarded

	// Untagged plain text: run the brain, send nothing.
	silent := !isTagged && text != ""

	if captionMatches && !textMatches {
		text = attachmentCaption
	}
	// An @-mention is written into the text as "@" and the bridge's number
	// (or LID). Like a trigger keyword, it only addresses the bridge, so it
	// goes too: "@Fridolin !stop" must be the command "!stop".
	var ownMentions []types.JID
	if isMentioned {
		ownMentions = wa.ownMentionJIDs()
	}
	text = stripMentions(cfg.Triggers.Strip(text), ownMentions)
	quoted := quoteOf(msg.Message)

	mediaPath, caption, notice := wa.downloadAttachment(msg, groupJID)
	// Untagged chatter gets no reply. A file always tags a group message,
	// so this only guards against that rule changing.
	if notice != "" && !silent {
		go wa.notify(groupJID, notice)
	}
	if caption != "" {
		caption = stripMentions(cfg.Triggers.Strip(caption), ownMentions)
		if text == "" {
			text = caption
		} else if caption != text {
			text = caption + "\n\n" + text
		}
	}

	// A reply that is only the trigger asks about the quoted message.
	if text == "" && mediaPath == "" && quoted == nil {
		return
	}

	log.Printf("Group message from %s (%q) by %s: processing (silent=%v)", groupJID, groupName, senderNumber, silent)
	go wa.handler(context.Background(), Inbound{
		JID: groupJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Quoted: quoted, Silent: silent, Source: "whatsapp", At: msg.Info.Timestamp,
	})
}

// downloadAttachment saves whichever attachment the message carries and
// returns its local path plus the attachment's caption (both may be empty).
// A notice is what to tell the chat about a file that was not saved.
func (wa *WhatsAppClient) downloadAttachment(msg *events.Message, chatJID string) (path, caption, notice string) {
	att, ok := attachmentOf(msg.Message, msg.Info.ID)
	if !ok {
		return "", "", ""
	}
	cfg := wa.cfg()
	maxBytes := cfg.MaxMediaBytes()
	ctx, cancel := context.WithTimeout(wa.ctx, mediaDownloadTimeout)
	defer cancel()
	path, err := saveAttachment(ctx, wa.media, cfg.MediaDir, att, maxBytes)
	switch {
	case errors.Is(err, errMediaTooLarge):
		wa.log.Warnf("Skipped attachment from %s: %v", chatJID, err)
		return "", att.caption, fmt.Sprintf("⚠️ That file is over the %d MB limit, so I skipped it.", maxBytes>>20)
	case err != nil:
		wa.log.Errorf("Failed to download attachment from %s: %v", chatJID, err)
		if wa.ctx.Err() != nil {
			return "", att.caption, "" // shutting down, not the sender's problem
		}
		// Without a word, a file sent without a caption would get no reply.
		return "", att.caption, "⚠️ I couldn't download that file. Please send it again."
	}
	return path, att.caption, ""
}

// sendNotice tells a chat about a problem with its message, such as a file
// too large to download. A failure is only logged.
func (wa *WhatsAppClient) sendNotice(jid, text string) {
	ctx, cancel := context.WithTimeout(wa.ctx, 30*time.Second)
	defer cancel()
	if err := wa.SendMessage(ctx, jid, text); err != nil {
		wa.log.Warnf("Failed to send notice to %s: %v", jid, err)
	}
}

// mediaDownloadTimeout bounds one attachment download. Downloads run on
// whatsmeow's event goroutine, so a stalled one holds up every message
// behind it.
const mediaDownloadTimeout = 2 * time.Minute

// mediaDownloader is what saveAttachment needs from whatsmeow.Client; tests
// swap in a fake.
type mediaDownloader interface {
	DownloadToFile(ctx context.Context, msg whatsmeow.DownloadableMessage, file whatsmeow.File) error
}

// attachment is the file a message carries.
type attachment struct {
	kind    string // image, document, audio or video
	media   whatsmeow.DownloadableMessage
	name    string // the name to save it under, before sanitising
	size    uint64 // the size the message declares; 0 when it does not
	caption string
}

// attachmentOf returns the file a message carries. GIF-playback videos are
// WhatsApp GIF reactions, not content, so they carry none.
func attachmentOf(m *waE2E.Message, id types.MessageID) (attachment, bool) {
	switch {
	case m.GetImageMessage() != nil:
		img := m.GetImageMessage()
		return attachment{kind: "image", media: img, name: string(id) + mediaExt(img.GetMimetype(), ".jpg"), size: img.GetFileLength(), caption: img.GetCaption()}, true

	case m.GetDocumentMessage() != nil:
		doc := m.GetDocumentMessage()
		// Prefer the sender's filename, but fall back to the message ID plus an
		// extension derived from the MIME type.
		name := doc.GetFileName()
		if name == "" {
			name = string(id) + mediaExt(doc.GetMimetype(), ".bin")
		}
		return attachment{kind: "document", media: doc, name: name, size: doc.GetFileLength(), caption: doc.GetCaption()}, true

	case m.GetAudioMessage() != nil:
		audio := m.GetAudioMessage()
		return attachment{kind: "audio", media: audio, name: string(id) + mediaExt(audio.GetMimetype(), ".ogg"), size: audio.GetFileLength()}, true

	case m.GetVideoMessage() != nil && !m.GetVideoMessage().GetGifPlayback():
		vid := m.GetVideoMessage()
		return attachment{kind: "video", media: vid, name: string(id) + mediaExt(vid.GetMimetype(), ".mp4"), size: vid.GetFileLength(), caption: vid.GetCaption()}, true
	}
	return attachment{}, false
}

// mediaExts maps a MIME type, without its parameters, to the extension a
// saved attachment gets.
var mediaExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",

	"audio/ogg":   ".ogg",
	"audio/opus":  ".ogg",
	"audio/mp4":   ".m4a",
	"audio/m4a":   ".m4a",
	"audio/x-m4a": ".m4a",
	"audio/mpeg":  ".mp3",

	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"video/quicktime": ".mov",
	"video/3gpp":      ".3gp",

	"application/pdf":          ".pdf",
	"application/msword":       ".doc",
	"application/vnd.ms-excel": ".xls",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       ".xlsx",
	"text/plain": ".txt",
	"text/csv":   ".csv",
}

// mediaExt returns the extension for a MIME type such as
// "audio/ogg; codecs=opus", or def for a type it does not know.
func mediaExt(mimeType, def string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	if ext, ok := mediaExts[strings.ToLower(strings.TrimSpace(base))]; ok {
		return ext
	}
	return def
}

// errMediaTooLarge marks an attachment over max_media_mb.
var errMediaTooLarge = errors.New("attachment is larger than max_media_mb")

// saveAttachment downloads att into dir and returns where it was saved. It
// streams the download into a hidden temp file, so the attachment is never
// held in memory and a brain never sees a half-written file, and names the
// file only once it is complete. A failed download leaves nothing behind.
//
// A file over maxBytes is refused, before the download when the message
// declares its size, and as soon as it passes the limit otherwise: the
// declared size is only the sender's word.
func saveAttachment(ctx context.Context, dl mediaDownloader, dir string, att attachment, maxBytes int64) (string, error) {
	if att.size > uint64(maxBytes) {
		return "", fmt.Errorf("%w: the %s is %d bytes, over the limit of %d", errMediaTooLarge, att.kind, att.size, maxBytes)
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", fmt.Errorf("saving %s: %w", att.kind, err)
	}
	defer os.Remove(tmp.Name()) // a failed download's; a saved one was renamed

	// The cap cancels ctx once the file passes it, so nothing more is
	// fetched for this attachment.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	f := &cappedFile{f: tmp, limit: maxBytes + mediaCipherOverhead, stop: cancel}
	// whatsmeow does not rewind the file before it tries its next server.
	// When one fails partway, the next one's bytes follow the partial ones
	// and the download fails; for a file near the limit, it can pass the
	// cap instead, and the sender is wrongly told the file is too large.
	err = dl.DownloadToFile(ctx, att.media, f)
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
		return "", fmt.Errorf("%w: the %s passed the limit of %d bytes while downloading", errMediaTooLarge, att.kind, maxBytes)
	}
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", att.kind, err)
	}
	return placeMedia(tmp.Name(), dir, att.name)
}

// mediaCipherOverhead is how much larger than a file its encrypted download
// can be: up to 16 bytes of AES-CBC padding and a 10-byte MAC.
const mediaCipherOverhead = 16 + 10

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
	return fmt.Errorf("%w: %w", errMediaTooLarge, context.Canceled)
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
// dangling symlink, so a file a brain left in the shared media directory is
// neither overwritten nor followed. A taken name moves on to "-2", "-3" and
// so on, so two documents called "scan.pdf" both survive. The rename then
// replaces only the empty file it just created.
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
	// truncate appends "…" (3 bytes) and never splits a character.
	return truncate(strings.TrimSuffix(name, ext), maxMediaNameBytes-len(ext)-len("…")) + ext
}

// isAllowed reports whether the phone number may DM the bridge.
func (wa *WhatsAppClient) isAllowed(number string) bool {
	return numberAllowed(wa.cfg(), number)
}

func numberAllowed(cfg *Config, number string) bool {
	if len(cfg.AllowedNumbers) == 0 {
		return cfg.AllowsAnyDM()
	}
	return slices.Contains(cfg.AllowedNumbers, number)
}

// isAllowedGroup reports whether the group JID is in allowed_groups.
// Unlike DMs, groups are deny-by-default: an empty list means no groups.
func (wa *WhatsAppClient) isAllowedGroup(jid string) bool {
	return slices.Contains(wa.cfg().AllowedGroups, jid)
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

// stripMentions removes the @-mentions of the given accounts from text and
// trims the result. A mention is "@" and the account's user part, and must
// not be the start of a longer number.
func stripMentions(text string, jids []types.JID) string {
	for _, jid := range jids {
		if jid.User == "" {
			continue
		}
		mention := "@" + jid.User
		for from := 0; from < len(text); {
			i := strings.Index(text[from:], mention)
			if i < 0 {
				break
			}
			i += from
			end := i + len(mention)
			if (i > 0 && !boundaryBefore(text, i)) || !boundaryAt(text, end) {
				from = end
				continue
			}
			text = text[:i] + text[end:]
			from = i
		}
	}
	return strings.TrimSpace(text)
}

// quoteOf returns the message a text reply quotes, or nil.
func quoteOf(msg *waE2E.Message) *Quote {
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return extractQuote(ext.GetContextInfo())
	}
	return nil
}

// extractQuote returns the quoted message of a reply, shortened, so the model
// can see what a reply is replying to. It is nil when nothing with text was
// quoted.
func extractQuote(contextInfo *waE2E.ContextInfo) *Quote {
	if contextInfo == nil {
		return nil
	}

	quotedText := extractText(contextInfo.GetQuotedMessage())
	if quotedText == "" {
		return nil
	}

	senderJID := normalizeJID(contextInfo.GetParticipant())
	if senderJID == "" {
		return nil
	}

	const maxQuoteLen = 200
	if len(quotedText) > maxQuoteLen {
		quotedText = truncate(quotedText, maxQuoteLen)
	}

	return &Quote{From: senderJID, Text: quotedText}
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
