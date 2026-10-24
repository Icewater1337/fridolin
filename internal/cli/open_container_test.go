package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// openDocker fakes docker for containerDashboardLink: ps names the core
// when running, and exec prints what open --print would.
func openDocker(running bool, printed string, execErr error, calls *[][]string) hostDeps {
	return hostDeps{docker: "/usr/bin/docker", run: func(_ context.Context, _ string, args, _ []string, stdout, stderr io.Writer) error {
		*calls = append(*calls, args)
		switch args[0] {
		case "ps":
			if running {
				fmt.Fprintln(stdout, coreContainer)
			}
			return nil
		case "exec":
			if execErr != nil {
				fmt.Fprintln(stderr, "the bridge does not answer")
				return execErr
			}
			fmt.Fprint(stdout, printed)
			return nil
		}
		return errors.New("unexpected")
	}}
}

func TestContainerDashboardLink(t *testing.T) {
	ctx := context.Background()
	host := coreEnv{}
	t.Run("asks the container's fridolin when the core runs here", func(t *testing.T) {
		var calls [][]string
		link, ok, err := containerDashboardLink(ctx, host, openDocker(true, "some log line\nhttp://127.0.0.1:8765/#login=abc\n", nil, &calls), false)
		if err != nil || !ok || link != "http://127.0.0.1:8765/#login=abc" {
			t.Fatalf("got %q %v %v", link, ok, err)
		}
		if want := []string{"exec", coreContainer, "fridolin", "open", "--print"}; !slices.Equal(calls[1], want) {
			t.Errorf("exec %q, want %q", calls[1], want)
		}
	})
	t.Run("passes --tailnet on", func(t *testing.T) {
		var calls [][]string
		if _, _, err := containerDashboardLink(ctx, host, openDocker(true, "https://fridolin-core.example.ts.net/#login=x", nil, &calls), true); err != nil {
			t.Fatal(err)
		}
		if calls[1][len(calls[1])-1] != "--tailnet" {
			t.Errorf("exec %q", calls[1])
		}
	})
	t.Run("a native bridge when no core container runs", func(t *testing.T) {
		var calls [][]string
		if _, ok, err := containerDashboardLink(ctx, host, openDocker(false, "", nil, &calls), false); ok || err != nil {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
		if len(calls) != 1 {
			t.Errorf("ran %q after ps", calls)
		}
	})
	t.Run("a native bridge without docker", func(t *testing.T) {
		if _, ok, _ := containerDashboardLink(ctx, host, hostDeps{}, false); ok {
			t.Fatal("ok without docker")
		}
	})
	t.Run("never in the container itself", func(t *testing.T) {
		var calls [][]string
		inside := coreEnv{environ: []string{"FRIDOLIN_CONTAINER=1"}}
		if _, ok, _ := containerDashboardLink(ctx, inside, openDocker(true, "http://x/", nil, &calls), false); ok || len(calls) > 0 {
			t.Fatalf("ok=%v calls=%q", ok, calls)
		}
	})
	t.Run("says why when the container's open refuses", func(t *testing.T) {
		var calls [][]string
		_, ok, err := containerDashboardLink(ctx, host, openDocker(true, "", errors.New("exit status 1"), &calls), false)
		if !ok || err == nil || !strings.Contains(err.Error(), "the bridge does not answer") {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	})
	t.Run("refuses output that is no link", func(t *testing.T) {
		var calls [][]string
		if _, ok, err := containerDashboardLink(ctx, host, openDocker(true, "nothing here", nil, &calls), false); !ok || err == nil {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	})
}
