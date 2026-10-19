package main

import (
	"context"
	"errors"
	wastore "go.mau.fi/whatsmeow/store"
	"log"
	"time"
)

// Run connects the client, retrying until connected, permanently refused,
// or cancelled. Its caller owns the goroutine and joins it on shutdown.
func (wa *WhatsAppClient) Run(ctx context.Context) { runConnectLoop(ctx, wa.Connect) }

// Connection retries: the first after connectFirstRetry, doubling up to
// connectMaxRetry.
const (
	connectFirstRetry = 5 * time.Second
	connectMaxRetry   = 2 * time.Minute
)

// connectLoop connects to WhatsApp, retrying while the network is down.
// Once connected, whatsmeow reconnects by itself. An error that another
// try cannot fix ends it: the dashboard's state says what to do.
func runConnectLoop(ctx context.Context, connect func() error) {
	for delay := connectFirstRetry; ctx.Err() == nil; delay = min(2*delay, connectMaxRetry) {
		err := connect()
		if err == nil {
			return
		}
		if errors.Is(err, wastore.ErrDeviceDeleted) || errors.Is(err, errPairingLive) {
			log.Printf("WhatsApp connect failed: %v", err)
			return
		}
		log.Printf("WhatsApp connect failed: %v — retrying in %v", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
