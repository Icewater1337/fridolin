package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"github.com/Icewater1337/fridolin/internal/access"
	"github.com/Icewater1337/fridolin/internal/chat"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	"github.com/Icewater1337/fridolin/internal/textx"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// IgnoredHandler is told about chats that wrote but are not allowed, so the
// dashboard can offer to allow them. It never receives message text.
type IgnoredHandler func(kind, jid, name, sender, pushName string)

// Client wraps a whatsmeow client.
type Client struct {
	cfg       func() Settings
	container DeviceContainer
	polls     PollStore
	chunks    func(string, int) []string
	identity  Identity
	outbound  Outbound
	text      Text
	inbox     func(string) mediafiles.Inbox
	handler   func(context.Context, chat.Inbound)
	onIgnored IgnoredHandler
	onVote    func(Vote)        // a vote was recorded; may be nil
	onChange  func(chat.Change) // an edit or a deletion; may be nil
	// groupRules is what addresses the bridge in a group, by its brain's
	// routing; nil means defaultGroupRules.
	groupRules func(groupJID string) GroupRules
	log        waLog.Logger
	media      Downloader             // nil: the client; tests set a fake
	lister     GroupLister            // nil: the client; tests set a fake
	notify     func(jid, text string) // sendNotice; tests record instead

	// ctx ends when the client disconnects for good. Attachment downloads,
	// group lookups and what the handlers send run under it, so shutting
	// down does not wait for them. What is only read from the disk runs
	// under storeCtx instead.
	ctx    context.Context
	cancel context.CancelFunc

	// intake holds each chat's message events until its worker handles
	// them, in the order they came (see chatQueues); downloads bounds the
	// attachments downloaded at once.
	intake    chatQueues
	downloads downloadSlots

	// pairMu serialises pairing; pairing is set while a pairing session's
	// codes are followed (see StartPairing).
	pairMu  sync.Mutex
	pairing bool

	groups groupCache // what JoinedGroups answers with

	mu sync.Mutex // guards the fields below, client included
	// client is replaced when WhatsApp logged the device out (see
	// newSession), so it is read with cl.
	client *whatsmeow.Client
	state  chat.LinkState
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

// New creates the transport without connecting it.
func New(opts Options) (*Client, error) {
	deviceStore, err := opts.Container.GetFirstDevice(context.Background())
	if err != nil {
		return nil, fmt.Errorf("getting device store: %w", err)
	}
	logger := opts.Log

	client := whatsmeow.NewClient(deviceStore, logger)

	ctx, cancel := context.WithCancel(context.Background())
	wa := &Client{
		client:     client,
		cfg:        opts.Settings,
		container:  opts.Container,
		polls:      opts.Polls,
		chunks:     opts.Chunks,
		identity:   opts.Identity,
		intake:     chatQueues{mask: opts.Text.MaskChat},
		outbound:   opts.Outbound,
		text:       opts.Text,
		inbox:      opts.Inbox,
		handler:    opts.OnInbound,
		onIgnored:  opts.OnIgnored,
		onVote:     opts.OnVote,
		onChange:   opts.OnChange,
		groupRules: opts.GroupRules,
		log:        logger,
		media:      opts.Downloader,
		lister:     opts.Groups,
		ctx:        ctx,
		cancel:     cancel,
		state:      chat.LinkState{Mode: "connecting", Since: time.Now()},
		// Nothing runs yet, so the device's own JIDs can be read here.
		own:    deviceStore.GetJID().ToNonAD(),
		ownLID: deviceStore.GetLID().ToNonAD(),
	}
	wa.state.OwnNumber = wa.own.User
	wa.notify = opts.Notify
	if wa.notify == nil {
		wa.notify = wa.sendNotice
	}

	client.AddEventHandler(wa.HandleEvent)
	return wa, nil
}

// storeCtx is ctx without its end, for what is only read from the disk:
// whatsmeow's map between LIDs and numbers and its message secrets, and
// the polls the bridge sent. Disconnect ends ctx to cut the downloads off,
// and then still has the chats' workers hand on the messages they hold;
// those reads must not fail then, and they take too little time to be
// worth cutting off.
func (wa *Client) storeCtx() context.Context { return context.WithoutCancel(wa.ctx) }

// cl returns the whatsmeow client.
func (wa *Client) cl() *whatsmeow.Client {
	wa.mu.Lock()
	defer wa.mu.Unlock()
	return wa.client
}

// downloader is what attachments are downloaded with.
func (wa *Client) downloader() Downloader {
	if wa.media != nil {
		return wa.media
	}
	return wa.cl()
}

// SendMessage sends a plain-text WhatsApp message to the given JID.
func (wa *Client) SendMessage(ctx context.Context, jid string, text string) error {
	recipient, err := wa.outbound.Recipient(jid)
	if err != nil {
		return err
	}
	_, err = wa.cl().SendMessage(ctx, recipient, &waE2E.Message{
		Conversation: proto.String(text),
	})
	return wa.outbound.Classify(err)
}

// SendChunked splits text into chunks and sends each as a separate message,
// with a small delay between them so they arrive in order, for /send,
// whose caller hears of a failure; brains' replies go through the Outbox.
// A failure part of the way is a *partialSendError.
func (wa *Client) SendChunked(ctx context.Context, jid string, text string) error {
	return wa.outbound.Chunks(ctx, wa.chunks(text, wa.cfg().MaxChunkSize), func(chunk string) error {
		return wa.SendMessage(ctx, jid, chunk)
	})
}

// SendPoll sends a WhatsApp poll and returns the message ID.
// maxSelectable is the max options a voter can pick (1 = single choice, 0 = unlimited).
// The ID is chosen here and given to sending before the poll goes out:
// a vote can arrive before SendMessage returns, and ProcessVote drops a
// vote for a poll that is not kept yet.
func (wa *Client) SendPoll(ctx context.Context, jid string, question string, options []string, maxSelectable int, sending func(id string)) (string, error) {
	recipient, err := wa.outbound.Recipient(jid)
	if err != nil {
		return "", err
	}
	cli := wa.cl()
	msg := cli.BuildPollCreation(question, options, maxSelectable)
	id := cli.GenerateMessageID()
	if sending != nil {
		sending(string(id))
	}
	resp, err := cli.SendMessage(ctx, recipient, msg, whatsmeow.SendRequestExtra{ID: id})
	if err != nil {
		return "", err
	}
	return string(resp.ID), nil
}

// JoinedGroups lists the groups the bridge account is in, as WhatsApp
// said last: kept until an event changes them or groupsTTL passes.
func (wa *Client) JoinedGroups(ctx context.Context) ([]chat.Group, error) {
	cli := wa.groupsFrom()
	if !cli.IsLoggedIn() {
		return nil, fmt.Errorf("not connected to WhatsApp")
	}
	return wa.groups.get(ctx, func(ctx context.Context) ([]chat.Group, error) {
		groups, err := cli.GetJoinedGroups(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]chat.Group, 0, len(groups))
		for _, g := range groups {
			out = append(out, chat.Group{JID: g.JID.String(), Name: g.Name, Participants: len(g.Participants)})
		}
		return out, nil
	})
}

// HandleEvent handles all whatsmeow events, on whatsmeow's one event
// goroutine, which must not wait: a message, an edit, a deletion or a
// poll vote goes onto its chat's queue, whose worker handles it (see
// chatQueues); the connection's events go to connectionEvent. The group
// list sees each first, as group and connection events are what drop it.
func (wa *Client) HandleEvent(rawEvt interface{}) {
	wa.groups.observe(rawEvt)
	if evt, ok := rawEvt.(*events.Message); ok {
		wa.enqueue(evt)
		return
	}
	wa.connectionEvent(rawEvt)
}

// enqueue puts evt on its chat's queue. One in no chat the bridge handles
// (see incomingChat) is dropped at once, which costs nothing.
func (wa *Client) enqueue(evt *events.Message) {
	chatJID, ok := wa.queueChat(evt.Info.MessageSource)
	if !ok {
		wa.handleMessage(evt)
		return
	}
	if err := wa.intake.add(chatJID, func() { wa.ProcessMessage(evt) }); errors.Is(err, errIntakeClosed) {
		wa.log.Debugf("Dropped message %s: the client is disconnecting", evt.Info.ID)
	}
}

// queueChat is the queue src's message goes on: its chat, as incomingChat
// finds it. Resolve a LID through the number WhatsApp sent along or the
// local identity store before choosing the queue, just as the worker
// chooses the message's chat. Otherwise a LID-only photo and text sent by
// number could overtake each other. This lookup never contacts WhatsApp;
// downloads and group queries stay on the workers. ok is false for a
// message in no chat the bridge handles.
func (wa *Client) queueChat(src types.MessageSource) (string, bool) {
	chatJID, ok := incomingChat(src)
	if !ok {
		return "", false
	}
	chatJID = chatJID.ToNonAD()
	if chatJID.Server == types.HiddenUserServer {
		alt := types.EmptyJID
		if !src.IsFromMe && (src.Sender.IsEmpty() || src.Sender.ToNonAD() == chatJID) {
			alt = src.SenderAlt
		}
		chatJID = wa.identity.Canonical(wa.storeCtx(), wa.LIDStore(), chatJID, alt)
	}
	return chatJID.String(), true
}

// ProcessMessage handles one message synchronously. The event adapter calls
// it from the chat's FIFO worker; callers supplying events own their ordering.
func (wa *Client) ProcessMessage(evt *events.Message) {
	if evt.Message.GetPollUpdateMessage() != nil {
		wa.handlePollVote(evt)
		return
	}
	wa.handleMessage(evt)
}

// handlePollVote decrypts a poll vote and records it (see recordVote).
func (wa *Client) handlePollVote(evt *events.Message) {
	pollVote, err := wa.cl().DecryptPollVote(wa.storeCtx(), evt)
	if err != nil {
		// Masked, in whatsmeow's error too, which names the voter: the
		// main log names no voter (see recordVote).
		wa.log.Errorf("Failed to decrypt poll vote from %s: %v", wa.text.MaskJID(evt.Info.Sender.User, evt.Info.Sender.Server), wa.text.Redact(err.Error()))
		return
	}
	pollMsgID := evt.Message.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()
	if pollMsgID == "" {
		wa.log.Warnf("Poll vote has no creation message key — skipping")
		return
	}
	// One voter, however WhatsApp addressed them: a vote changed later
	// must replace the first, not count as a second voter's.
	voter := wa.identity.Canonical(wa.storeCtx(), wa.LIDStore(), evt.Info.Sender, evt.Info.SenderAlt)
	wa.ProcessVote(pollMsgID, voter, pollVote.GetSelectedOptions())
}

// ProcessVote stores voter's choice in the poll sent as pollMsgID, given
// as WhatsApp sends it, the hashes of the chosen options, and tells
// onVote. A vote for a poll the bridge did not keep is dropped.
func (wa *Client) ProcessVote(pollMsgID string, voter types.JID, hashes [][]byte) {
	poll, err := wa.polls.SentPoll(wa.storeCtx(), pollMsgID)
	if err != nil {
		// Not necessarily an error: the poll may predate vote tracking.
		log.Printf("Poll vote for unknown poll %s (not in DB) — skipping", pollMsgID)
		return
	}
	selected := matchVote(hashes, poll.Options)
	// Who chose what is for the debug log only: the main log is kept, and
	// sent along with bug reports.
	wa.log.Debugf("Poll vote recorded: poll=%s voter=%s options=%v", pollMsgID, voter, selected)
	if err := wa.polls.SavePollVote(pollMsgID, voter.String(), selected); err != nil {
		wa.log.Errorf("Failed to save poll vote from %s: %v", wa.text.MaskJID(voter.User, voter.Server), err)
		return
	}
	if wa.onVote != nil {
		wa.onVote(Vote{Poll: poll, Voter: voter, Selected: selected})
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
func incomingChat(src types.MessageSource) (chatJID types.JID, ok bool) {
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

func (wa *Client) handleMessage(msg *events.Message) {
	chatJID, ok := incomingChat(msg.Info.MessageSource)
	if !ok {
		wa.log.Debugf("Ignoring message %s in %s (Status updates, channels and own broadcasts are not handled)", msg.Info.ID, msg.Info.Chat)
		return
	}
	if chatJID != msg.Info.Chat {
		// A broadcast-list message, now a DM. Work on a copy: the event
		// belongs to whatsmeow.
		dm := *msg
		dm.Info.Chat, dm.Info.IsGroup = chatJID, false
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
	// Masked, as a refused group member is: anyone may write to the
	// bridge's number, and the dashboard, which offers to allow them,
	// shows it whole. A LID nothing resolves is no number allowed_numbers
	// lists, so its line is followed by the one that ignores it.
	who := wa.text.MaskSender(sender.User, resolved, sender.Server)
	if !resolved {
		log.Printf("DM: could not resolve %s to a phone number (using it as it is)", who)
	}

	// A LID is a separate identity namespace: its digits cannot match an
	// allowed phone number until WhatsApp resolves it. Keep the deliberate
	// open-empty-list mode, using one config snapshot for both decisions.
	cfg := wa.cfg()
	policy := cfg.Policy
	envelope := chat.Envelope{Sender: sender.User, SenderResolved: resolved}
	allowed := access.Authorize(policy, envelope).Allowed
	if !allowed {
		log.Printf("DM from %s: ignored (not in allowed_numbers)", who)
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
		go wa.notify(sender.String(), wa.text.ViewOnce)
	case kindOther:
		wa.log.Debugf("DM %s from %s: nothing the bridge reads (a reaction, a sticker, …)", msg.Info.ID, sender.User)
	case kindMessage:
		wa.handleDMMessage(msg, sender, policy, envelope)
	}
}

// change hands an edit or a deletion of the message target, by sender,
// in chat (with its other JID alt, for a DM), to the bridge (onChange).
// text is an edit's new text, as the message's own path would read it.
func (wa *Client) change(kind msgKind, target, chatJID, alt, sender, text string) {
	c := chat.Change{Kind: chat.ChangeRevoke, Chat: chatJID, ChatAlt: alt, Target: target, Sender: sender, Text: text}
	if kind == kindEdit {
		c.Kind = chat.ChangeEdit
	}
	log.Printf("%s of message %s in %s by %s", c.Kind, target, chatJID, sender)
	if wa.onChange != nil && target != "" {
		wa.onChange(c)
	}
}

// handleDMMessage handles an allowed direct message from sender, the
// person as canonicalUser has them.
func (wa *Client) handleDMMessage(msg *events.Message, sender types.JID, policy access.Policy, envelope chat.Envelope) {
	chatJID := sender.String()
	// The chat's other JID: history from before DMs were keyed by number
	// may be under the LID WhatsApp addressed them by.
	alt := msg.Info.Chat.ToNonAD()
	if alt == sender {
		alt = wa.identity.LID(wa.storeCtx(), wa.LIDStore(), sender)
	}
	senderNumber := sender.User
	text := extractText(msg.Message)
	quoted := wa.quoteOf(msg.Message)

	mediaPath, caption, notice := wa.fetchAttachment(msg, chatJID)
	if notice != "" {
		go wa.notify(chatJID, notice)
	}
	envelope.Text, envelope.Caption = text, caption
	text = access.Decide(policy, envelope).Text

	if text == "" && mediaPath == "" && quoted == nil {
		return
	}

	m := chat.Inbound{JID: chatJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Quoted: quoted, Source: chat.SourceWhatsApp, At: msg.Info.Timestamp, ID: string(msg.Info.ID)}
	if !alt.IsEmpty() {
		m.ChatAlt = alt.String()
	}
	// On the chat's worker, so the chat's messages reach the bridge in
	// the order they came.
	wa.handler(wa.ctx, m)
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
func (wa *Client) handleGroupMessage(msg *events.Message) {
	cfg := wa.cfg()
	groupJID := msg.Info.Chat.String()
	// In groups that address members by LID, the number is what the
	// identity map and group_members know them by.
	sender, resolved := wa.senderOf(msg.Info.MessageSource)
	senderNumber := sender.User
	who := wa.text.MaskSender(senderNumber, resolved, sender.Server)

	// Unknown groups are logged and offered on the dashboard: that is how you
	// find a group's JID to allow it. Both checks read the one snapshot cfg:
	// with a fresh one here, an edit between them could let a message pass
	// that neither config admits.
	if !access.GroupAllowed(cfg.Policy, groupJID) {
		groupName := wa.groupName(msg.Info.Chat)
		log.Printf("Group message from %s by %s: ignored (not in allowed_groups)", groupLabel(groupJID, groupName), who)
		if wa.onIgnored != nil {
			wa.onIgnored("group", groupJID, groupName, senderNumber, msg.Info.PushName)
		}
		return
	}
	// A member group_members does not admit is not read at all: no run,
	// no command, no download, no edit, not even a line in the history.
	// The group is allowed, so it is not offered on the dashboard again.
	if !access.GroupSenderAllowed(cfg.Policy, groupJID, senderNumber, resolved, msg.Info.IsFromMe) {
		// A reaction or a sticker would not have been read anyway, and a
		// busy group sends many: they go to the debug log, as they would
		// from a member.
		if kind, _, _ := classify(msg); kind == kindOther {
			wa.log.Debugf("Group message %s in %s by %s: ignored (not admitted by group_members, and nothing the bridge reads)", msg.Info.ID, groupJID, who)
			return
		}
		log.Printf("Group message in %s by %s: ignored (not admitted by group_members)", groupJID, who)
		return
	}
	// Only for the log line: an allowed group's name is never looked up.
	groupName := wa.groups.names.cached(groupJID)

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
	case kindMessage, kindViewOnce:
		// Read below, by the group's rules.
	}

	envelope := groupEnvelope(msg.Message, extractText(msg.Message), own)
	envelope.Chat, envelope.Sender, envelope.SenderResolved, envelope.FromMe = groupJID, senderNumber, resolved, msg.Info.IsFromMe
	policy := cfg.Policy
	rules := wa.rulesFor(groupJID)
	policy.Files, policy.Forwards = rules.Files, rules.Forwards
	decision := access.Decide(policy, envelope)
	silent := decision.Silent
	if kind == kindViewOnce {
		if !silent {
			go wa.notify(groupJID, wa.text.ViewOnce)
		}
		return
	}
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
		mediaPath, caption, notice = wa.fetchAttachment(msg, groupJID)
	}
	if notice != "" {
		go wa.notify(groupJID, notice)
	}
	envelope.Caption = caption
	text := access.Decide(policy, envelope).Text

	// A reply that is only the trigger asks about the quoted message.
	if text == "" && mediaPath == "" && quoted == nil {
		return
	}

	log.Printf("Group message from %s by %s: processing (silent=%v)", groupLabel(groupJID, groupName), senderNumber, silent)
	wa.handler(wa.ctx, chat.Inbound{JID: groupJID, Sender: senderNumber, Text: text, Media: mediaList(mediaPath),
		Quoted: quoted, Silent: silent, Source: chat.SourceWhatsApp, At: msg.Info.Timestamp, ID: string(msg.Info.ID)})
}

// fetchAttachment is downloadAttachment once one of the download slots
// all chats share is free (see maxDownloads). A message without a file
// needs none, and nor does one whose file declares more than max_media_mb:
// it is refused before any download, and its sender need not wait for
// the downloads of others to be told so. One that waits while the client
// disconnects goes on without a slot: its download fails at once, and the
// message goes on without the file and without a notice, as one the
// disconnect cut off.
func (wa *Client) fetchAttachment(msg *events.Message, chatJID string) (path, caption, notice string) {
	if att, ok := attachmentOf(msg.Message, msg.Info.ID); ok && att.size <= uint64(wa.cfg().MaxMediaBytes) {
		if release, ok := wa.downloads.acquire(wa.ctx); ok {
			defer release()
		}
	}
	return wa.downloadAttachment(msg, chatJID)
}

// downloadAttachment saves whichever attachment the message carries and
// returns its local path plus the attachment's caption (both may be empty).
// A notice is what to tell the chat about a file that was not saved.
func (wa *Client) downloadAttachment(msg *events.Message, chatJID string) (path, caption, notice string) {
	att, ok := attachmentOf(msg.Message, msg.Info.ID)
	if !ok {
		return "", "", ""
	}
	cfg := wa.cfg()
	maxBytes := cfg.MaxMediaBytes
	ctx, cancel := context.WithTimeout(wa.ctx, mediaDownloadTimeout)
	defer cancel()
	// Into the media folder's incoming folder, which no brain can read:
	// the brain that takes the message moves it into its own inbox.
	path, err := saveAttachmentTo(ctx, wa.downloader(), wa.inbox(filepath.Join(cfg.MediaDir, mediafiles.IncomingDir)), att, maxBytes)
	switch {
	case errors.Is(err, mediafiles.ErrTooLarge):
		wa.log.Warnf("Skipped attachment from %s: %v", chatJID, err)
		return "", att.caption, wa.text.MediaTooLarge(maxBytes)
	case err != nil:
		wa.log.Errorf("Failed to download attachment from %s: %v", chatJID, err)
		if wa.ctx.Err() != nil {
			return "", att.caption, "" // shutting down, not the sender's problem
		}
		// Without a word, a file sent without a caption would get no reply.
		return "", att.caption, wa.text.MediaFailed
	}
	return path, att.caption, ""
}

// sendNotice tells a chat about a problem with its message, such as a file
// too large to download. A failure is only logged.
func (wa *Client) sendNotice(jid, text string) {
	ctx, cancel := context.WithTimeout(wa.ctx, 30*time.Second)
	defer cancel()
	if err := wa.SendMessage(ctx, jid, text); err != nil {
		wa.log.Warnf("Failed to send notice to %s: %v", jid, err)
	}
}

// mediaDownloadTimeout bounds one attachment download. Downloads run on
// their chat's worker, so a stalled one holds up the messages its chat
// sent after it, and one of the maxDownloads slots.
const mediaDownloadTimeout = 2 * time.Minute

// Downloader is what saveAttachment needs from whatsmeow.Client; tests
// swap in a fake.
type Downloader interface {
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

// saveAttachmentTo adapts whatsmeow to the media inbox's bounded file writer.
func saveAttachmentTo(ctx context.Context, dl Downloader, inbox mediafiles.Inbox, att attachment, maxBytes int64) (string, error) {
	return inbox.Save(ctx, att.kind, att.name, att.size, maxBytes, func(ctx context.Context, f mediafiles.File) error {
		return dl.DownloadToFile(ctx, att.media, f)
	})
}

// rulesFor returns what addresses the bridge in the group jid, as its
// brain's routing says.
func (wa *Client) rulesFor(jid string) GroupRules {
	if wa.groupRules == nil {
		return GroupRules{Files: true, Forwards: true}
	}
	return wa.groupRules(jid)
}

// ownMentionJIDs returns every JID form the bridge account may be mentioned as.
// WhatsApp emits either the phone-number JID or the linked LID form.
func (wa *Client) ownMentionJIDs() []types.JID {
	wa.mu.Lock()
	own, lid := wa.own, wa.ownLID
	wa.mu.Unlock()
	if own.User == "" {
		return nil
	}
	if lid.User == "" {
		lid = wa.identity.LID(wa.storeCtx(), wa.LIDStore(), own)
	}
	out := []types.JID{own}
	if lid.User != "" && lid != own {
		out = append(out, lid)
	}
	return out
}

// senderOf is who sent src, as canonicalUser has them: by phone number
// where the message carries it (SenderAlt) or the LID store knows it.
// resolved is false for a LID nothing maps to a number, whose digits are
// WhatsApp's internal id and must match no number on a list.
func (wa *Client) senderOf(src types.MessageSource) (sender types.JID, resolved bool) {
	sender = wa.identity.Canonical(wa.storeCtx(), wa.LIDStore(), src.Sender, src.SenderAlt)
	return sender, sender.Server == types.DefaultUserServer
}

// groupLabel is how the log names a group: by its JID, and by its name
// too when the bridge knows one, which it may not for long, or ever when
// nobody opens the dashboard's group list.
func groupLabel(jid, name string) string {
	if name == "" {
		return jid
	}
	return fmt.Sprintf("%s (%q)", jid, name)
}

// groupName is the group's subject as WhatsApp has it, or "" when it
// cannot be looked up: from the names kept (see groupNameCache), or else
// asked for on the chat's worker, never on whatsmeow's event goroutine.
func (wa *Client) groupName(jid types.JID) string {
	return wa.groups.names.lookup(wa.ctx, jid.String(), func(ctx context.Context) (string, error) {
		info, err := wa.groupsFrom().GetGroupInfo(ctx, jid)
		if err != nil {
			return "", err
		}
		if info == nil {
			return "", errors.New("WhatsApp sent no group info")
		}
		return info.Name, nil
	})
}

// LIDStore is whatsmeow's map between LIDs and phone numbers, or nil
// without a client.
func (wa *Client) LIDStore() wastore.LIDStore {
	cli := wa.cl()
	if cli == nil || cli.Store == nil {
		return nil
	}
	return cli.Store.LIDs
}

// quoteOf is quoteOf with the quoted message's author by number when the
// reply names them by LID, so the identity map finds them.
func (wa *Client) quoteOf(msg *waE2E.Message) *chat.Quote {
	q := quoteOf(msg)
	if q == nil {
		return nil
	}
	p, err := types.ParseJID(msg.GetExtendedTextMessage().GetContextInfo().GetParticipant())
	if err == nil && p.Server == types.HiddenUserServer {
		q.From = wa.identity.Canonical(wa.storeCtx(), wa.LIDStore(), p, types.EmptyJID).User
	}
	return q
}

// quoteOf returns the message a text reply quotes, or nil.
func quoteOf(msg *waE2E.Message) *chat.Quote {
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return extractQuote(ext.GetContextInfo())
	}
	return nil
}

// extractQuote returns the quoted message of a reply, shortened, so the model
// can see what a reply is replying to. It is nil when nothing with text was
// quoted.
func extractQuote(contextInfo *waE2E.ContextInfo) *chat.Quote {
	if contextInfo == nil {
		return nil
	}

	quotedText := extractText(contextInfo.GetQuotedMessage())
	if quotedText == "" {
		return nil
	}

	senderJID := chat.NormalizeJID(contextInfo.GetParticipant())
	if senderJID == "" {
		return nil
	}

	const maxQuoteLen = 200
	if len(quotedText) > maxQuoteLen {
		quotedText = textx.Truncate(quotedText, maxQuoteLen)
	}

	return &chat.Quote{From: senderJID, Text: quotedText}
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
