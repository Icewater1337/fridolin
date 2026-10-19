package pipeline

import (
	"context"
	"errors"
	brainruntime "github.com/Icewater1337/fridolin/internal/brain"
	"github.com/Icewater1337/fridolin/internal/claudecli"
	"log"
	"time"
)

// Finished sends a finished brain run's answer.
func (p *Workflow) Finished(br *brainruntime.Brain, m brainruntime.Inbound, res claudecli.Result) {
	ctx := context.Background()
	if res.Err != nil {
		if errors.Is(res.Err, claudecli.ErrInterrupted) || errors.Is(res.Err, context.Canceled) {
			log.Printf("brain %s: run for %s was stopped", br.ID(), m.JID)
			return
		}
		// The error in full goes to the log and the dashboard only: it can
		// hold the CLI's stderr, and a group's members read the chat.
		log.Printf("brain %s: error for %s: %v", br.ID(), m.JID, res.Err)
		if text := ChatErrorText(res.Err, !m.IsGroup()); text != "" && !m.Silent {
			_ = p.Notice(ctx, m.JID, text)
		}
		return
	}

	// An empty response follows a question forwarded mid-run, which said
	// it all. Silent runs never reply at all.
	if res.Text == "" || m.Silent {
		return
	}
	log.Printf("brain %s: sending response (%d chars) to %s after %s", br.ID(), len(res.Text), m.JID, res.Duration.Round(time.Second))
	if err := p.Reply(ctx, br.ID(), m.JID, res.Text, p.Settings().MaxChunkSize); err != nil {
		log.Printf("brain %s: %v", br.ID(), err)
	}
}
