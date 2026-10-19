package logx

import (
	"bytes"
	"encoding/json"
	"log"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogRingKeepsCompleteLinesAndCursor(t *testing.T) {
	t.Parallel()
	r := NewRing(3)
	for _, p := range []string{"one\nt", "wo\nthree", "\nfour\npartial"} {
		if n, err := r.Write([]byte(p)); err != nil || n != len(p) {
			t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(p))
		}
	}
	for _, tc := range []struct {
		after int64
		limit int
		seqs  []int64
		text  []string
		next  int64
	}{
		{0, 0, []int64{2, 3, 4}, []string{"two", "three", "four"}, 4},
		{0, 2, []int64{3, 4}, []string{"three", "four"}, 4},
		{3, 10, []int64{4}, []string{"four"}, 4},
		{8, 10, nil, nil, 8},
	} {
		lines, next := r.Since(tc.after, tc.limit)
		var seqs []int64
		var texts []string
		for _, line := range lines {
			seqs, texts = append(seqs, line.Seq), append(texts, line.Text)
			if line.TS.IsZero() {
				t.Error("a complete line has no timestamp")
			}
		}
		if !reflect.DeepEqual(seqs, tc.seqs) || !reflect.DeepEqual(texts, tc.text) || next != tc.next {
			t.Errorf("Since(%d, %d) = %v %q %d; want %v %q %d", tc.after, tc.limit, seqs, texts, next, tc.seqs, tc.text, tc.next)
		}
	}
	lines, _ := r.Since(0, 0)
	lines[0].Text = "changed by reader"
	again, _ := r.Since(0, 0)
	if again[0].Text != "two" {
		t.Fatal("a reader changed the ring's retained line")
	}
}

func TestLogRingConcurrentWriters(t *testing.T) {
	t.Parallel()
	r := NewRing(100)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() { _, _ = r.Write([]byte("line\n")); _, _ = r.Since(0, 0) })
	}
	wg.Wait()
	lines, next := r.Since(0, 0)
	if len(lines) != 100 || next != 100 {
		t.Fatalf("retained %d lines, cursor %d; want 100, 100", len(lines), next)
	}
	for i, line := range lines {
		if line.Seq != int64(i+1) || line.Text != "line" {
			t.Errorf("line %d: %+v", i, line)
		}
	}
}

func TestLogLineJSON(t *testing.T) {
	t.Parallel()
	line := Line{Seq: 7, TS: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Text: "ready"}
	got, err := json.Marshal(line)
	if want := `{"seq":7,"ts":"2026-01-02T03:04:05Z","text":"ready"}`; err != nil || string(got) != want {
		t.Fatalf("JSON = %s, %v; want %s", got, err, want)
	}
}

func TestWaLoggerLevelsAndSubmodules(t *testing.T) {
	// The adapter intentionally routes through the process logger.
	prev, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags); log.SetPrefix(prefix) })
	log.SetFlags(0)
	log.SetPrefix("")
	for _, tc := range []struct {
		level string
		first int
	}{{"debug", 0}, {"INFO", 1}, {"warn", 2}, {"ERROR", 3}, {"unknown", 1}} {
		t.Run(tc.level, func(t *testing.T) {
			var out bytes.Buffer
			log.SetOutput(&out)
			logger := NewWaLogger("WhatsApp", tc.level).Sub("Socket")
			logger.Debugf("event %d", 1)
			logger.Infof("event %d", 2)
			logger.Warnf("event %d", 3)
			logger.Errorf("event %d", 4)
			all := []string{"[WhatsApp/Socket DEBUG] event 1\n", "[WhatsApp/Socket INFO] event 2\n", "[WhatsApp/Socket WARN] event 3\n", "[WhatsApp/Socket ERROR] event 4\n"}
			if want := strings.Join(all[tc.first:], ""); out.String() != want {
				t.Errorf("log = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestLogRingWrapsWithoutChangingCursorOrPartialLines(t *testing.T) {
	t.Parallel()
	ring := NewRing(2)
	for _, part := range []string{"one\ntwo\nthree", "\nfour\nfi", "ve\n"} {
		if _, err := ring.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	got, next := ring.Since(0, 0)
	if len(got) != 2 || got[0].Seq != 4 || got[0].Text != "four" || got[1].Seq != 5 || got[1].Text != "five" || next != 5 {
		t.Fatalf("wrapped ring: %+v %d", got, next)
	}
	got, next = ring.Since(4, 1)
	if len(got) != 1 || got[0].Text != "five" || next != 5 {
		t.Fatalf("cursor: %+v %d", got, next)
	}
	for _, size := range []int{0, -1} {
		empty := NewRing(size)
		_, _ = empty.Write([]byte("line\n"))
		if got, next := empty.Since(3, 10); len(got) != 0 || next != 3 {
			t.Fatalf("disabled ring: %+v %d", got, next)
		}
	}
}
