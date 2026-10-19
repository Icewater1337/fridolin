package app_test

import (
	"testing"

	"github.com/Icewater1337/fridolin/internal/app"
	"github.com/Icewater1337/fridolin/internal/cli"
)

func TestMain(m *testing.M) { app.RunTestMain(m, cli.Main, cli.Serve) }
