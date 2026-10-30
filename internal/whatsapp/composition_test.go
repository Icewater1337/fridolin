package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/Icewater1337/fridolin/internal/logx"
	mediafiles "github.com/Icewater1337/fridolin/internal/media"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// These supplied services are test fixtures. Command tests retain coverage
// of configuration projection, privacy formatting, SQL polls and runtime
// callbacks; transport tests check when these consumers are called.
type testGroupRules struct{ files, forwards bool }

var defaultTestGroupRules = testGroupRules{files: true, forwards: true}

func testChunks(text string, _ int) []string { return []string{text} }
func testInbox(dir string) mediafiles.Inbox  { return mediafiles.Inbox{Dir: dir} }
func maskPhone(number string) string         { return "phone-…" + number[max(0, len(number)-2):] }
func maskChatJID(raw string) string {
	jid, _ := types.ParseJID(raw)
	if jid.Server == types.GroupServer {
		return raw
	}
	return "…" + jid.User[max(0, len(jid.User)-2):] + "@" + jid.Server
}
func testText() Text {
	return Text{
		ViewOnce: "view-once notice", MediaFailed: "⚠️ I couldn't download that file. Please send it again.",
		MediaTooLarge: func(n int64) string {
			return fmt.Sprintf("⚠️ That file is over the %d MB limit, so I skipped it.", n>>20)
		},
		MaskSender: func(number string, resolved bool, server string) string {
			if resolved {
				return maskPhone(number)
			}
			return "…" + number[max(0, len(number)-2):] + "@" + server
		},
		MaskJID: func(user, server string) string { return maskChatJID(user + "@" + server) }, MaskChat: maskChatJID,
		Redact: func(s string) string { return strings.ReplaceAll(s, otherNumber, maskPhone(otherNumber)) },
	}
}
func testIdentity() Identity {
	return Identity{
		Canonical: func(ctx context.Context, lids store.LIDStore, jid, alt types.JID) types.JID {
			jid, alt = jid.ToNonAD(), alt.ToNonAD()
			if jid.Server != types.HiddenUserServer {
				return jid
			}
			if alt.Server == types.DefaultUserServer && alt.User != "" {
				return alt
			}
			if lids != nil {
				if pn, err := lids.GetPNForLID(ctx, jid); err == nil && pn.User != "" {
					return pn.ToNonAD()
				}
			}
			return jid
		},
		LID: func(ctx context.Context, lids store.LIDStore, pn types.JID) types.JID {
			if lids == nil || pn.Server != types.DefaultUserServer {
				return types.EmptyJID
			}
			lid, err := lids.GetLIDForPN(ctx, pn.ToNonAD())
			if err != nil || lid.User == "" {
				return types.EmptyJID
			}
			return lid.ToNonAD()
		},
	}
}

type fixedDeviceContainer struct{ device *store.Device }

func (c fixedDeviceContainer) GetFirstDevice(context.Context) (*store.Device, error) {
	return c.device, nil
}
func (c fixedDeviceContainer) NewDevice() *store.Device { return &store.Device{} }

type testQueuedPolls struct{ poll SentPoll }

func (s testQueuedPolls) SentPoll(ctx context.Context, _ string) (SentPoll, error) {
	return s.poll, ctx.Err()
}
func (testQueuedPolls) SavePollVote(string, string, []string) error { return nil }

func captureLog(t *testing.T) func() []string {
	t.Helper()
	ring := logx.NewRing(50)
	prev := log.Writer()
	log.SetOutput(ring)
	t.Cleanup(func() { log.SetOutput(prev) })
	return func() []string {
		lines, _ := ring.Since(0, 50)
		var out []string
		for _, l := range lines {
			out = append(out, l.Text)
		}
		return out
	}
}

const anyMember = "*"

func isConnected(c interface{ Connected() <-chan struct{} }) bool {
	select {
	case <-c.Connected():
		return true
	default:
		return false
	}
}

type testNoPolls struct{}

func (testNoPolls) SentPoll(context.Context, string) (SentPoll, error) {
	return SentPoll{}, sql.ErrNoRows
}
func (testNoPolls) SavePollVote(string, string, []string) error { return nil }
