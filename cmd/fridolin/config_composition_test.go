package main

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	outboundmsg "github.com/Icewater1337/fridolin/internal/outbound"

	"github.com/Icewater1337/fridolin/internal/claudecli"
	"github.com/Icewater1337/fridolin/internal/config"
)

// Configuration owns defaults for saved settings; providers and the chunker
// retain their independent defensive defaults for direct callers. These tests
// keep those contracts in step without imports from config into runtime code.
func TestConfigDefaultsMatchRuntimeContracts(t *testing.T) {
	cfg, err := config.Resolve(&config.Config{}, configPaths(), runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentProvider != "claude" || cfg.ClaudePath != "claude" || cfg.CodexPath != "codex" {
		t.Fatalf("provider defaults = %q, %q, %q", cfg.AgentProvider, cfg.ClaudePath, cfg.CodexPath)
	}
	if cfg.CodexModel != claudecli.DefaultCodexModel {
		t.Fatalf("config model %q differs from provider fallback %q", cfg.CodexModel, claudecli.DefaultCodexModel)
	}
	if cfg.MaxChunkSize != outboundmsg.DefaultChunkSize {
		t.Fatalf("config chunk default %d differs from chunker %d", cfg.MaxChunkSize, outboundmsg.DefaultChunkSize)
	}
	text := strings.Repeat("x", outboundmsg.DefaultChunkSize*2)
	if a, b := outboundmsg.Chunk(text, 0), outboundmsg.Chunk(text, cfg.MaxChunkSize); !reflect.DeepEqual(a, b) {
		t.Fatal("configured chunk default differs from direct-call fallback")
	}
	if _, err := config.Resolve(&config.Config{MaxChunkSize: outboundmsg.MinChunkSize}, configPaths(), runtime.GOOS); err != nil {
		t.Fatalf("chunker's minimum is refused by config: %v", err)
	}
	if _, err := config.Resolve(&config.Config{MaxChunkSize: outboundmsg.MinChunkSize - 1}, configPaths(), runtime.GOOS); err == nil {
		t.Fatal("config accepted less than the chunker's minimum")
	}
}

func TestBrainQueueModeMatchesConfiguration(t *testing.T) {
	if brainQueueInterrupt != config.QueueModeInterrupt {
		t.Fatal("runtime and configuration interrupt modes differ")
	}
}
