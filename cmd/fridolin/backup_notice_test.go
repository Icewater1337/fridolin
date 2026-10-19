package main

import (
	"context"
	"testing"
	"time"
)

// TestScheduledBackupNeverForces: the nightly run keeps refusing a mass
// deletion, so a wiped brain is never mirrored without the owner.
func TestScheduledBackupNeverForces(t *testing.T) {
	fx, d := scheduledBackupFixture(t)
	now := time.Now()
	res := scheduledFixtureRun(t, fx, 0)
	d.mu.Lock()
	syncs := len(d.syncs)
	d.mu.Unlock()
	if res.OK || !res.Refused || res.Trigger != "scheduled" || syncs != 0 {
		t.Errorf("scheduled run = %+v with %d syncs, want a scheduled refusal and no sync", res, syncs)
	}
	fx.manager.mu.Lock()
	last := fx.manager.state.LastScheduledDay
	fx.manager.mu.Unlock()
	if last != now.Format("2006-01-02") {
		t.Errorf("last scheduled day = %q, want today", last)
	}
}

// TestRefusalNoticeGivesUp: the notice is sent from inside the backup job,
// so a send that hangs must not keep the job (and Back up now) busy. It
// gives up after refusalNoticeTimeout and counts as not told.
func TestRefusalNoticeGivesUp(t *testing.T) {
	fx, _ := scheduledBackupFixture(t)
	defer func(d time.Duration) { refusalNoticeTimeout = d }(refusalNoticeTimeout)
	refusalNoticeTimeout = 50 * time.Millisecond
	fx.manager.notify = func(ctx context.Context, _ BackupTarget, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		res := &BackupResult{Refused: true, Trigger: "scheduled", Error: massDeletionError}
		fx.manager.noticeRefusal(context.Background(), fx.target("notes"), res)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a send that hangs keeps the backup job busy")
	}
	fx.manager.mu.Lock()
	_, told := fx.manager.state.RefusalNotices["notes"]
	fx.manager.mu.Unlock()
	if told {
		t.Error("a notice that timed out must not count as told")
	}
}

// TestRefusalNoticesForgetRemovedBrains: the saved notices drop brains that
// are gone, so the list does not grow forever and a new brain that reuses an
// id is still told about its first refusal.
func TestRefusalNoticesForgetRemovedBrains(t *testing.T) {
	fx, _ := scheduledBackupFixture(t)
	var notices []string
	fx.manager.notify = func(_ context.Context, target BackupTarget, _ string) error {
		notices = append(notices, target.ID)
		return nil
	}
	fx.manager.mu.Lock()
	fx.manager.state.RefusalNotices = map[string]time.Time{"removed": time.Now(), "notes": time.Now()}
	fx.manager.mu.Unlock()
	if res := scheduledFixtureRun(t, fx, 0); !res.Refused {
		t.Fatalf("scheduled run = %+v, want a refusal", res)
	}
	saved := newBackupManager(fx.options).state.RefusalNotices
	if _, ok := saved["removed"]; ok {
		t.Errorf("the notice of a removed brain is still saved: %v", saved)
	}
	if _, ok := saved["notes"]; !ok {
		t.Errorf("the notice of a brain that is still refused was dropped: %v", saved)
	}
	if out := notices; len(out) != 0 {
		t.Errorf("a refusal already told was sent again: %+v", out)
	}
}
