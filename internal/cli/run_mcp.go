package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Icewater1337/fridolin/internal/toolcli"
)

// mcpCmd is `fridolin mcp --run ID`, the MCP server of a brain's run.
func mcpCmd(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return toolcli.RunMCP(ctx, args, os.Stdin, processToolIO())
}
