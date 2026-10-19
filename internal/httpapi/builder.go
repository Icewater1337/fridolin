package httpapi

import (
	"context"
)

type BuilderAPI struct {
	BrainForToken func(string) string
	Context       func(string) any
	Check         func(context.Context, string) (any, error)
	Preview       func(context.Context, string) (any, error)
	Sync          func(context.Context, string) (string, error)
	Propose       func(context.Context, string, string, string) (any, any, error)
}
