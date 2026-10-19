package logx

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTextHandlerPreservesLegacyLinesAndEncodesAttributes(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	handler := NewTextHandler(&out, nil)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	record := slog.NewRecord(at, slog.LevelInfo, "Bridge stopped.\n", 0)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "2026/01/02 03:04:05 Bridge stopped.\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	out.Reset()
	child := handler.WithAttrs([]slog.Attr{slog.String("component", "worker")}).WithGroup("run")
	record = slog.NewRecord(at, slog.LevelWarn, "finished", 0)
	record.AddAttrs(slog.String("reason", "two words"), slog.Int("attempt", 2))
	if err := child.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	want := "2026/01/02 03:04:05 finished component=worker run.reason=\"two words\" run.attempt=2\n"
	if got := out.String(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTextHandlerSerializesChildrenAndHonorsLevels(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	var level slog.LevelVar
	level.Set(slog.LevelWarn)
	logger := slog.New(NewTextHandler(&out, &level))
	logger.Info("filtered")
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() { logger.With("worker", i).Warn(fmt.Sprintf("line-%d", i)) })
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 32 || strings.Contains(out.String(), "filtered") {
		t.Fatalf("unexpected lines: %s", out.String())
	}
	for i := range 32 {
		if strings.Count(out.String(), fmt.Sprintf("line-%d worker=%d\n", i, i)) != 1 {
			t.Fatalf("missing or interleaved worker %d: %s", i, out.String())
		}
	}
	level.Set(slog.LevelDebug)
	logger.Debug("enabled")
	if !strings.Contains(out.String(), "enabled\n") {
		t.Fatal("level change ignored")
	}
}

func TestDefaultBridgeKeepsStandardLogAndSlogInOneRing(t *testing.T) {
	previous, writer, flags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() { slog.SetDefault(previous); log.SetOutput(writer); log.SetFlags(flags) })
	ring := NewRing(10)
	slog.SetDefault(NewLogger(ring))
	log.Printf("legacy %d", 1)
	slog.Info("structured")
	lines, _ := ring.Since(0, 10)
	if len(lines) != 2 || !strings.HasSuffix(lines[0].Text, " legacy 1") || !strings.HasSuffix(lines[1].Text, " structured") {
		t.Fatalf("bridge output: %+v", lines)
	}
	if strings.Contains(lines[0].Text, "INFO") || strings.Contains(lines[1].Text, "INFO") {
		t.Fatal("legacy line shape gained a level prefix")
	}
}
