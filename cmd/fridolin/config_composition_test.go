package main

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

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
	if cfg.CodexModel != defaultCodexModel {
		t.Fatalf("config model %q differs from provider fallback %q", cfg.CodexModel, defaultCodexModel)
	}
	if cfg.MaxChunkSize != defaultChunkSize {
		t.Fatalf("config chunk default %d differs from chunker %d", cfg.MaxChunkSize, defaultChunkSize)
	}
	text := strings.Repeat("x", defaultChunkSize*2)
	if a, b := ChunkMessage(text, 0), ChunkMessage(text, cfg.MaxChunkSize); !reflect.DeepEqual(a, b) {
		t.Fatal("configured chunk default differs from direct-call fallback")
	}
	if _, err := config.Resolve(&config.Config{MaxChunkSize: minChunkSize}, configPaths(), runtime.GOOS); err != nil {
		t.Fatalf("chunker's minimum is refused by config: %v", err)
	}
	if _, err := config.Resolve(&config.Config{MaxChunkSize: minChunkSize - 1}, configPaths(), runtime.GOOS); err == nil {
		t.Fatal("config accepted less than the chunker's minimum")
	}
}
