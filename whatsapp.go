package main

import (
	"context"
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

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
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
	// Connected returns a channel that is closed while the messenger can
	// send: at once when it is connected, else when it next connects.
	Connected() <-chan struct{}
	StartPairing() error
	JoinedGroups(ctx context.Context) ([]GroupInfo, error)
}

// WAState is the connection state shown on the dashboard.
type WAState struct {
	// Mode: disabled (dev mode) or one of the mode constants in
	// waconn.go: connecting, pairing, qr-expired, connected,
	// disconnected, logged-out, replaced, outdated, banned, failed.
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
	cfg       func() *Config
	store     *Store
	handler   InboundHandler
	onIgnored IgnoredHandler
	onVote    func(pollVote)      // a vote was recorded; may be nil
	onChange  func(messageChange) // an edit or a deletion; may be nil
	// groupRules is what addresses the bridge in a group, by its brain's
	// routing; nil means defaultGroupRules.
	groupRules func(groupJID string) groupRules
	log        waLog.Logger
	media      mediaDownloader        // nil: the client; tests set a fake
	lister     groupLister            // nil: the client; tests set a fake
	notify     func(jid, text string) // sendNotice; tests record instead

	// ctx ends when the client disconnects for good. Attachment downloads
	// run under it, so shutting down does not wait for them.
	ctx    context.Context
	cancel context.CancelFunc

	// pairMu serialises pairing; pairing is set while a pairing session's
	// codes are followed (see StartPairing).
	pairMu  sync.Mutex
	pairing bool

	groups groupCache // what JoinedGroups answers with

	mu sync.Mutex // guards the fields below, client included
	// client is replaced when WhatsApp logged the device out (see
	// newSession), so it is read with cl.
	client *whatsmeow.Client
	state  WAState
	// own and ownLID are the bridge account's JIDs, as whatsmeow reported
	// them when it connected or paired; empty while unpaired.
	own, ownLID types.JID
	// online is closed while the state is "connected", and replaced by an
	// open one when it leaves it (see Connected).
	online chan struct{}
	// retry is the connection attempt the bridge scheduled itself
	// (connectAfter), and retryWait the wait it chose after the last
	// failure on WhatsApp's side, 0 once connected (failureRetryAfter).
	retry     *time.Timer
	retryWait time.Duration
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
		ctx:       ctx,
		cancel:    cancel,
		state:     WAState{Mode: "connecting", Since: time.Now()},
		// Nothing runs yet, so the device's own JIDs can be read here.
		own:    deviceStore.GetJID().ToNonAD(),
		ownLID: deviceStore.GetLID().ToNonAD(),
	}
	wa.state.OwnNumber = wa.own.User
	wa.notify = wa.sendNotice

	client.AddEventHandler(wa.eventHandler)
	return wa, nil
}

// cl returns the whatsmeow client.
func (wa *WhatsAppClient) cl() *whatsmeow.Client {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	return wa.client
}

// downloader is what attachments are downloaded with.
func (wa *WhatsAppClient) downloader() mediaDownloader {
	if wa.media != nil {
		return wa.media
	}
	return wa.cl()
}

// errOffline marks a send that failed because WhatsApp is not connected or
// not paired: worth trying again later, and not the caller's mistake.
var errOffline = errors.New("WhatsApp is not connected")

// errBadRecipient marks a send to an address WhatsApp has no chat for.
var errBadRecipient = errors.New("not a chat WhatsApp can send to")

// classifySendErr marks err, from whatsmeow, with errOffline or
// errBadRecipient where it is one, so callers can tell them apart without
// knowing whatsmeow.
func classifySendErr(err error) error {
	switch {
	case errors.Is(err, whatsmeow.ErrNotConnected), errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return fmt.Errorf("%w: %w", errOffline, err)
	case errors.Is(err, whatsmeow.ErrUnknownServer):
		return fmt.Errorf("%w: %w", errBadRecipient, err)
	}
	return err
}

// partialSendError is a chunked send that failed after some chunks went
// out: sent holds those, as the chat shows them.
type partialSendError struct {
	sent  []string
	total int
	err   error
}

func (e *partialSendError) Error() string {
	return fmt.Sprintf("sent %d of %d parts, then: %v", len(e.sent), e.total, e.err)
}

func (e *partialSendError) Unwrap() error { return e.err }

// sendChunks sends chunks one by one with send, pausing between them
// unless ctx ends. A failure after the first chunk is a *partialSendError
// naming what went out.
func sendChunks(ctx context.Context, chunks []string, pause time.Duration, send func(chunk string) error) error {
	for i, chunk := range chunks {
		err := ctx.Err()
		if i > 0 && pause > 0 && err == nil {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(pause):
			}
		}
		if err == nil {
			err = send(chunk)
		}
		if err != nil {
			if i > 0 {
				return &partialSendError{sent: chunks[:i], total: len(chunks), err: err}
			}
			return err
		}
	}
	return nil
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
	_, err = wa.cl().SendMessage(ctx, recipient, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return classifySendErr(err)
}

// SendChunked splits text into chunks and sends each as a separate message,
// with a small delay between them so they arrive in order, for /send,
// whose caller hears of a failure; brains' replies go through the Outbox.
// A failure part of the way is a *partialSendError.
func (wa *WhatsAppClient) SendChunked(ctx context.Context, jid string, text string) error {
	return sendChunks(ctx, ChunkMessage(text, wa.cfg().MaxChunkSize), outboxPace, func(chunk string) error {
		return wa.SendMessage(ctx, jid, chunk)
	})
}

// SendPoll sends a WhatsApp poll and returns the message ID.
// maxSelectable is the max options a voter can pick (1 = single choice, 0 = unlimited).
func (wa *WhatsAppClient) SendPoll(ctx context.Context, jid string, question string, options []string, maxSelectable int) (string, error) {
	recipient, err := parseRecipient(jid)
	if err != nil {
		return "", err
	}
	cli := wa.cl()
	msg := cli.BuildPollCreation(question, options, maxSelectable)
	resp, err := cli.SendMessage(ctx, recipient, msg)
	if err != nil {
		return "", err
	}
	return string(resp.ID), nil
}

// JoinedGroups lists the groups the bridge account is in, as WhatsApp
// said last: kept until an event changes them or groupsTTL passes.
func (wa *WhatsAppClient) JoinedGroups(ctx context.Context) ([]GroupInfo, error) {
	cli := wa.groupsFrom()
	if !cli.IsLoggedIn() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}
	return wa.groups.get(ctx, func(ctx context.Context) ([]GroupInfo, error) {
		groups, err := cli.GetJoinedGroups(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]GroupInfo, 0, len(groups))
		for _, g := range groups {
			out = append(out, GroupInfo{JID: g.JID.String(), Name: g.Name, Participants: len(g.Participants)})
		}
		return out, nil
	})
}

// eventHandler handles all whatsmeow events: messages here, the
// connection's in connectionEvent. The group list sees each first, as
// group and connection events are what drop it.
func (wa *WhatsAppClient) eventHandler(rawEvt interface{}) {
	wa.groups.observe(rawEvt)
	if evt, ok := rawEvt.(*events.Message); ok {
		if evt.Message.GetPollUpdateMessage() != nil {
			wa.handlePollVote(evt)
		} else {
			wa.handleMessage(evt)
		}
		return
	}
	wa.connectionEvent(rawEvt)
}

// handlePollVote decrypts a poll vote and records it (see recordVote).
func (wa *WhatsAppClient) handlePollVote(evt *events.Message) {
	pollVote, err := wa.cl().DecryptPollVote(wa.ctx, evt)
	if err != nil {
		wa.log.Errorf("Failed to decrypt poll vote from %s: %v", evt.Info.Sender, err)
		return
	}
	pollMsgID := evt.Message.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()
	if pollMsgID == "" {
		wa.log.Warnf("Poll vote has no creation message key — skipping")
		return
	}
	// One voter, however WhatsApp addressed them: a vote changed later
	// must replace the first, not count as a second voter's.
	voter := canonicalUser(wa.ctx, wa.lidStore(), evt.Info.Sender, evt.Info.SenderAlt)
	wa.recordVote(pollMsgID, voter, pollVote.GetSelectedOptions())
}

// recordVote stores voter's choice in the poll sent as pollMsgID, given
// as WhatsApp sends it, the hashes of the chosen options, and tells
// onVote. A vote for a poll the bridge did not keep is dropped.
func (wa *WhatsAppClient) recordVote(pollMsgID string, voter types.JID, hashes [][]byte) {
	poll, err := wa.store.SentPoll(wa.ctx, pollMsgID)
	if err != nil {
		// Not necessarily an error: the poll may predate vote tracking.
		log.Printf("Poll vote for unknown poll %s (not in DB) — skipping", pollMsgID)
		return
	}
	selected := matchVote(hashes, poll.Options)
	log.Printf("Poll vote recorded: poll=%s voter=%s options=%v", pollMsgID, voter, selected)
	if err := wa.store.SavePollVote(pollMsgID, voter.String(), selected); err != nil {
		wa.log.Errorf("Failed to save poll vote from %s: %v", voter, err)
		return
	}
	if wa.onVote != nil {
		wa.onVote(pollVote{Poll: poll, Voter: voter, Selected: selected})
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

	if msg.Info.Chat.Server == types.GroupServer {
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

	// Newer WhatsApp clients address senders by LID rather than phone
	// number. One person is one number and one chat either way: the
	// allowlist is written in numbers, and the chat's history must not
	// split in two.
	src := msg.Info.MessageSource
	if src.Sender.IsEmpty() {
		src.Sender = src.Chat
	}
	sender, resolved := wa.senderOf(src)
	if !resolved {
		log.Printf("DM: could not resolve %s to a phone number (using it as it is)", sender)
	}

	if !wa.isAllowed(sender.User) {
		log.Printf("DM from %s: ignored (not in allowed_numbers)", sender.User)
		if wa.onIgnored != nil {
			wa.onIgnored("dm", sender.String(), "", sender.User, msg.Info.PushName)
		}
		return
	}

	switch kind, target, content := classify(msg); kind {
	case kindEdit, kindRevoke:
		alt := ""
		if a := msg.Info.Chat.ToNonAD(); a != sender {
			alt = a.String()
		}
		wa.change(kind, target, sender.String(), alt, sender.User, extractText(content))
	case kindViewOnce:
		go wa.notify(sender.String(), viewOnceNotice)
	case kindOther:
		wa.log.Debugf("DM %s from %s: nothing the bridge reads (a reaction, a sticker, …)", msg.Info.ID, sender.User)
	default:
		wa.handleDMMessage(msg, sender)
	}
}

// change hands an edit or a deletion of the message target, by sender,
// in chat (with its other JID alt, for a DM), to the bridge (onChange).
// text is an edit's new text, as the message's own path would read it.
func (wa *WhatsAppClient) change(kind msgKind, target, chat, alt, sender, text string) {
	c := messageChange{Kind: changeRevoke, Chat: chat, ChatAlt: alt, Target: target, Sender: sender, Text: text}
	if kind == kindEdit {
		c.Kind = changeEdit
	}
	log.Printf("%s of message %s in %s by %s", c.Kind, target, chat, sender)
	if wa.onChange != nil && target != "" {
		wa.onChange(c)
	}
}

// handleDMMessage handles an allowed direct message from sender, the
// person as canonicalUser has them.
func (wa *WhatsAppClient) handleDMMessage(msg *events.Message, sender types.JID) {
	chatJID := sender.String()
	// The chat's other JID: history from before DMs were keyed by number
	// may be under the LID WhatsApp addressed them by.
	alt := msg.Info.Chat.ToNonAD()
	if alt == sender {
		alt = lidOf(wa.ctx, wa.lidStore(), sender)
	}
	senderNumber := sender.User
	text := extractText(msg.Message)
	quoted := wa.quoteOf(msg.Message)

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

	m := Inbound{
		JID: chatJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Quoted: quoted, Source: sourceWhatsApp, At: msg.Info.Timestamp, ID: string(msg.Info.ID),
	}
	if !alt.IsEmpty() {
		m.ChatAlt = alt.String()
	}
	go wa.handler(context.Background(), m)
}

func mediaList(p string) []string {
	if p == "" {
		return nil
	}
	return []string{p}
}

// handleGroupMessage processes messages from allowed groups, by the
// members group_members admits (groupSenderAllowed).
//
// A message is "tagged" — and so gets a visible reply — when it addresses
// the bridge (see addressGroup): a trigger keyword, an @-mention of the
// bridge account, a quote-reply to one of its messages, and, unless the
// group's brain turns them off, a file or a forward. Untagged text still
// reaches the brain, whose history keeps it so that it stays aware of the
// conversation, but produces no reply.
func (wa *WhatsAppClient) handleGroupMessage(msg *events.Message) {
	cfg := wa.cfg()
	groupJID := msg.Info.Chat.String()
	// In groups that address members by LID, the number is what the
	// identity map and group_members know them by.
	sender, resolved := wa.senderOf(msg.Info.MessageSource)
	senderNumber := sender.User

	// Unknown groups are logged and offered on the dashboard: that is how you
	// find a group's JID to allow it. Both checks read the one snapshot cfg:
	// with a fresh one here, an edit between them could let a message pass
	// that neither config admits.
	if !isAllowedGroup(cfg, groupJID) {
		groupName := wa.groupName(msg.Info.Chat)
		log.Printf("Group message from %s (%q) by %s: ignored (not in allowed_groups)", groupJID, groupName, senderNumber)
		if wa.onIgnored != nil {
			wa.onIgnored("group", groupJID, groupName, senderNumber, msg.Info.PushName)
		}
		return
	}
	// A member group_members does not admit is not read at all: no run,
	// no command, no download, no edit, not even a line in the history.
	// The group is allowed, so it is not offered on the dashboard again.
	who := groupSender{Number: senderNumber, Resolved: resolved, Server: sender.Server, FromMe: msg.Info.IsFromMe}
	if !groupSenderAllowed(cfg, groupJID, who) {
		// A reaction or a sticker would not have been read anyway, and a
		// busy group sends many: they go to the debug log, as they would
		// from a member.
		if kind, _, _ := classify(msg); kind == kindOther {
			wa.log.Debugf("Group message %s in %s by %s: ignored (not admitted by group_members, and nothing the bridge reads)", msg.Info.ID, groupJID, who.masked())
			return
		}
		log.Printf("Group message in %s by %s: ignored (not admitted by group_members)", groupJID, who.masked())
		return
	}
	groupName := wa.groupName(msg.Info.Chat)

	own := wa.ownMentionJIDs()
	kind, target, content := classify(msg)
	switch kind {
	case kindEdit, kindRevoke:
		// An edit is read like the message it replaces: its trigger and
		// the bridge's mention go.
		wa.change(kind, target, groupJID, "", senderNumber, stripMentions(cfg.Triggers.Strip(extractText(content)), own))
		return
	case kindOther:
		wa.log.Debugf("Group message %s in %s: nothing the bridge reads (a reaction, a sticker, …)", msg.Info.ID, groupJID)
		return
	}

	text := extractText(msg.Message)
	addr := addressGroup(msg.Message, text, cfg.Triggers, own, wa.rulesFor(groupJID))
	// Untagged: the brain's history, no reply.
	silent := !addr.tagged
	if kind == kindViewOnce {
		if !silent {
			go wa.notify(groupJID, viewOnceNotice)
		}
		return
	}

	if addr.byCaption {
		if img := msg.Message.GetImageMessage(); img != nil {
			text = img.GetCaption()
		} else {
			text = msg.Message.GetDocumentMessage().GetCaption()
		}
	}
	// An @-mention is written into the text as "@" and the bridge's number
	// (or LID). Like a trigger keyword, it only addresses the bridge, so it
	// goes too: "@Fridolin !stop" must be the command "!stop".
	var ownMentions []types.JID
	if addr.mentioned {
		ownMentions = own
	}
	text = stripMentions(cfg.Triggers.Strip(text), ownMentions)
	quoted := wa.quoteOf(msg.Message)

	// A file is fetched only for a message that addresses the bridge. In
	// a group whose brain does not answer files, a photo is chatter, and
	// its caption is all the brain reads of it.
	var mediaPath, caption, notice string
	if silent {
		if att, ok := attachmentOf(msg.Message, msg.Info.ID); ok {
			caption = att.caption
		}
	} else {
		mediaPath, caption, notice = wa.downloadAttachment(msg, groupJID)
	}
	if notice != "" {
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
		Quoted: quoted, Silent: silent, Source: sourceWhatsApp, At: msg.Info.Timestamp, ID: string(msg.Info.ID),
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
	// Into the media folder's incoming folder, which no brain can read:
	// the brain that takes the message moves it into its own inbox.
	path, err := saveAttachment(ctx, wa.downloader(), filepath.Join(cfg.MediaDir, incomingMediaDir), att, maxBytes)
	switch {
	case errors.Is(err, errMediaTooLarge):
		wa.log.Warnf("Skipped attachment from %s: %v", chatJID, err)
		return "", att.caption, mediaTooLargeNotice(maxBytes)
	case err != nil:
		wa.log.Errorf("Failed to download attachment from %s: %v", chatJID, err)
		if wa.ctx.Err() != nil {
			return "", att.caption, "" // shutting down, not the sender's problem
		}
		// Without a word, a file sent without a caption would get no reply.
		return "", att.caption, mediaFailedNotice
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("saving %s: %w", att.kind, err)
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
	// truncate appends "…" (3 bytes) and never splits a character.
	return truncate(strings.TrimSuffix(name, ext), maxMediaNameBytes-len(ext)-len("…")) + ext
}

// isAllowed reports whether the phone number may DM the bridge.
func (wa *WhatsAppClient) isAllowed(number string) bool {
	return numberAllowed(wa.cfg(), number)
}

// numberAllowed compares number and the list as phone numbers, digits
// only, so that "+1 555 123 4567" in a config the bridge did not write
// still lets 15551234567 in. Anything that is no phone number is compared
// as it is.
func numberAllowed(cfg *Config, number string) bool {
	if len(cfg.AllowedNumbers) == 0 {
		return cfg.AllowsAnyDM()
	}
	if n, err := phoneNumber(number); err == nil {
		number = n
	}
	return slices.Contains(allowedNumbersAsNumbers(cfg.AllowedNumbers), number)
}

// rulesFor returns what addresses the bridge in the group jid, as its
// brain's routing says.
func (wa *WhatsAppClient) rulesFor(jid string) groupRules {
	if wa.groupRules == nil {
		return defaultGroupRules
	}
	return wa.groupRules(jid)
}

// isAllowedGroup reports whether the group JID is in cfg's allowed_groups.
// Unlike DMs, groups are deny-by-default: an empty list means no groups.
func isAllowedGroup(cfg *Config, jid string) bool {
	return slices.Contains(cfg.AllowedGroups, jid)
}

// ownMentionJIDs returns every JID form the bridge account may be mentioned as.
// WhatsApp emits either the phone-number JID or the linked LID form.
func (wa *WhatsAppClient) ownMentionJIDs() []types.JID {
	wa.mu.Lock()
	own, lid := wa.own, wa.ownLID
	wa.mu.Unlock()
	if own.User == "" {
		return nil
	}
	if lid.User == "" {
		lid = lidOf(wa.ctx, wa.lidStore(), own)
	}
	out := []types.JID{own}
	if lid.User != "" && lid != own {
		out = append(out, lid)
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

// senderOf is who sent src, as canonicalUser has them: by phone number
// where the message carries it (SenderAlt) or the LID store knows it.
// resolved is false for a LID nothing maps to a number, whose digits are
// WhatsApp's internal id and must match no number on a list.
func (wa *WhatsAppClient) senderOf(src types.MessageSource) (sender types.JID, resolved bool) {
	sender = canonicalUser(wa.ctx, wa.lidStore(), src.Sender, src.SenderAlt)
	return sender, sender.Server == types.DefaultUserServer
}

// groupName is the group's subject as WhatsApp has it, or "" when it
// cannot be looked up.
func (wa *WhatsAppClient) groupName(jid types.JID) string {
	if info, err := wa.cl().GetGroupInfo(context.Background(), jid); err == nil && info != nil {
		return info.Name
	}
	return ""
}

// lidStore is whatsmeow's map between LIDs and phone numbers, or nil
// without a client.
func (wa *WhatsAppClient) lidStore() store.LIDStore {
	cli := wa.cl()
	if cli == nil || cli.Store == nil {
		return nil
	}
	return cli.Store.LIDs
}

// quoteOf is quoteOf with the quoted message's author by number when the
// reply names them by LID, so the identity map finds them.
func (wa *WhatsAppClient) quoteOf(msg *waE2E.Message) *Quote {
	q := quoteOf(msg)
	if q == nil {
		return nil
	}
	p, err := types.ParseJID(msg.GetExtendedTextMessage().GetContextInfo().GetParticipant())
	if err == nil && p.Server == types.HiddenUserServer {
		q.From = canonicalUser(wa.ctx, wa.lidStore(), p, types.EmptyJID).User
	}
	return q
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

// extractText returns the plain text content of a message. A shared
// location or contact card has none, so it is written as a line the model
// can read (see locationText and contactText): without it, the message
// would get no answer at all.
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
	if loc := msg.GetLocationMessage(); loc != nil {
		return locationText(loc.GetDegreesLatitude(), loc.GetDegreesLongitude(), loc.GetName(), loc.GetAddress(), loc.GetURL())
	}
	if loc := msg.GetLiveLocationMessage(); loc != nil {
		return locationText(loc.GetDegreesLatitude(), loc.GetDegreesLongitude(), loc.GetCaption(), "", "")
	}
	if c := msg.GetContactMessage(); c != nil {
		return contactText(c.GetDisplayName(), c.GetVcard())
	}
	if cs := msg.GetContactsArrayMessage(); cs != nil {
		var cards []string
		for _, c := range cs.GetContacts() {
			if t := contactText(c.GetDisplayName(), c.GetVcard()); t != "" {
				cards = append(cards, t)
			}
		}
		return strings.Join(cards, "\n")
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

// devConnected is always closed: dev mode can always send.
var devConnected = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

func (d *devMessenger) Connected() <-chan struct{} { return devConnected }
func (d *devMessenger) Connect() error             { return nil }
func (d *devMessenger) Disconnect()                {}
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
