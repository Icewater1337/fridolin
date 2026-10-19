package cli

import (
	"fmt"
	"github.com/Icewater1337/fridolin/internal/toolcli"
	"os"
)

func mcpSiteCmd() {
	url, token := os.Getenv("FRIDOLIN_BUILDER_URL"), os.Getenv("FRIDOLIN_BUILDER_TOKEN")
	if url == "" || token == "" {
		fmt.Fprintln(os.Stderr, "fridolin mcp-site: FRIDOLIN_BUILDER_URL and FRIDOLIN_BUILDER_TOKEN must be set (Fridolin sets them for builder brains)")
		os.Exit(2)
	}
	toolcli.ServeSiteMCP(os.Stdin, os.Stdout, url, token)
}
