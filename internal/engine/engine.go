// Package engine implements core.Engine — the thing that actually answers a message.
package engine

import (
	"context"

	"github.com/kidkuddy/momo/internal/core"
)

// Echo answers with the prompt. It is the default on purpose: a stray run of momo
// must not be able to execute anything.
type Echo struct{}

func (Echo) Name() string { return "echo" }

func (Echo) Run(_ context.Context, t core.Task) (core.Answer, error) {
	return core.Answer{Reply: "echo: " + t.Prompt}, nil
}
