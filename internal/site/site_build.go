package site

// The Build page (docs/SITE.md): a chat with a builder brain in the
// dashboard, next to its preview.
//
// A build chat is an ordinary brain run in a chat of its own: the JID
// site-build-<brain>-<n>@fridolin.dashboard, so it gets its own Claude
// session, and the brain's sandbox, plugin and MCP server as on WhatsApp.
// Whatever the brain sends to such a JID lands here instead of WhatsApp.

import (
	"encoding/json"
	"fmt"
	"os"

	"sync"
	"time"

	"github.com/Icewater1337/fridolin/internal/fsx"
	"github.com/Icewater1337/fridolin/internal/manifest"
)

// BuildMessage is one line of a build chat.
type BuildMessage struct {
	Role string `json:"role"` // user, assistant
	Text string `json:"text"`
	At   string `json:"at"`
}

type BuildChat struct {
	Thread   int            `json:"thread"`
	Messages []BuildMessage `json:"messages"`
}

// siteChats keeps the build chats, one per brain, in <data_dir>/site/build/.
type siteChats struct {
	m  *SiteManager
	mu sync.Mutex
}

// path is the file of a brain's build chat. The id arrives in a request
// URL, so only a valid brain id names a file (and so one in this folder).
func (c *siteChats) path(brainID string) (string, error) {
	if !manifest.ValidID(brainID) {
		return "", fmt.Errorf("%q is not a brain id", brainID)
	}
	return c.m.dir("build", brainID+".json"), nil
}

func (c *siteChats) loadLocked(brainID string) BuildChat {
	ch := BuildChat{Thread: 1, Messages: []BuildMessage{}}
	if p, err := c.path(brainID); err == nil {
		if data, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(data, &ch)
		}
	}
	if ch.Thread < 1 {
		ch.Thread = 1
	}
	if ch.Messages == nil {
		ch.Messages = []BuildMessage{}
	}
	return ch
}

func (c *siteChats) saveLocked(brainID string, ch BuildChat) error {
	p, err := c.path(brainID)
	if err != nil {
		return err
	}
	if len(ch.Messages) > 300 {
		ch.Messages = ch.Messages[len(ch.Messages)-300:]
	}
	data, _ := json.MarshalIndent(ch, "", "  ")
	return fsx.WriteFileAtomic(p, data, 0o600, false)
}

func (c *siteChats) get(brainID string) BuildChat {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked(brainID)
}

func (c *siteChats) add(brainID, role, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.loadLocked(brainID)
	ch.Messages = append(ch.Messages, BuildMessage{Role: role, Text: text, At: time.Now().UTC().Format(time.RFC3339)})
	return c.saveLocked(brainID, ch)
}

// newThread starts a new conversation (a new JID, so a new Claude session).
func (c *siteChats) newThread(brainID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.loadLocked(brainID)
	return c.saveLocked(brainID, BuildChat{Thread: ch.Thread + 1, Messages: []BuildMessage{}})
}

// BuildChat returns the current thread and messages for a builder.
func (m *SiteManager) BuildChat(id string) BuildChat { return m.chats.get(id) }

// AppendBuildMessage records a message in the builder's current thread.
func (m *SiteManager) AppendBuildMessage(id, role, text string) error {
	return m.chats.add(id, role, text)
}

// NewBuildThread starts the builder's next conversation.
func (m *SiteManager) NewBuildThread(id string) error { return m.chats.newThread(id) }
