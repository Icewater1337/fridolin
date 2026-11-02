package config

import (
	"fmt"
	"unicode/utf8"
)

// CallsConfig is WhatsApp voice calls (docs/CALLS.md): the owner calls
// Fridolin's number and talks to the brain that gets their direct
// messages, and the core brain's errands call people Fridolin wrote to
// before. Off by default; turning it on takes a restart, since the call
// stack must be in place before WhatsApp connects. Calls need the
// ElevenLabs connection (with a voice) to speak, and it or the OpenAI
// connection to listen.
type CallsConfig struct {
	// Enabled answers the owner's calls and lets errands call.
	Enabled bool `json:"enabled,omitempty"`
	// Greeting is what Fridolin says when it answers the owner's call;
	// "" is DefaultCallGreeting.
	Greeting string `json:"greeting,omitempty"`
	// Notice is what Fridolin says after its introduction when it calls
	// someone for an errand: that it is an AI and keeps notes. ""
	// is DefaultCallNotice.
	Notice string `json:"notice,omitempty"`
}

// The defaults of calls.
const (
	DefaultCallGreeting = "Hi, Fridolin here. What can I do for you?"
	DefaultCallNotice   = "I'm an AI assistant, and I keep notes of this call."
	// MaxCallLine is the longest greeting or notice.
	MaxCallLine = 300
)

// GreetingOrDefault is c's greeting, or DefaultCallGreeting.
func (c CallsConfig) GreetingOrDefault() string {
	if c.Greeting != "" {
		return c.Greeting
	}
	return DefaultCallGreeting
}

// NoticeOrDefault is c's notice, or DefaultCallNotice.
func (c CallsConfig) NoticeOrDefault() string {
	if c.Notice != "" {
		return c.Notice
	}
	return DefaultCallNotice
}

func (c CallsConfig) validate() error {
	for name, s := range map[string]string{"calls.greeting": c.Greeting, "calls.notice": c.Notice} {
		if utf8.RuneCountInString(s) > MaxCallLine {
			return fmt.Errorf("%s must be at most %d characters", name, MaxCallLine)
		}
	}
	return nil
}
