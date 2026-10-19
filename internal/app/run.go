package app

import (
	"context"
	"errors"
)

// Run starts the application once and owns its complete shutdown. A dashboard
// restart is returned as ErrRestart after storage closes and the lock is free;
// only the CLI replaces the process. Ordinary cancellation is a normal stop.
func (b *App) Run(parent context.Context) error {
	b.runMu.Lock()
	if b.runUsed {
		b.runMu.Unlock()
		return errors.New("application has already run")
	}
	ctx, cancel := context.WithCancelCause(parent)
	b.runUsed, b.runCancel = true, cancel
	b.runMu.Unlock()
	defer func() {
		cancel(nil)
		b.Stop()
		b.runMu.Lock()
		b.runCancel = nil
		b.runMu.Unlock()
	}()
	if err := b.Start(ctx); err != nil {
		return err
	}
	if errors.Is(context.Cause(ctx), ErrRestart) {
		return ErrRestart
	}
	return nil
}

func (b *App) canRestart() bool {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	return b.restart != nil || b.runCancel != nil
}

// RequestRestart asks Run to finish with ErrRestart. It returns false when no
// run owns the application and no embedding owner supplied a restart callback.
func (b *App) RequestRestart() bool {
	if b.restart != nil {
		b.restart()
		return true
	}
	b.runMu.Lock()
	cancel := b.runCancel
	b.runMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel(ErrRestart)
	return true
}
