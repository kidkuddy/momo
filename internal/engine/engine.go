// Package engine implements core.Engine. It is what actually answers a message.
package engine

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// Echo answers with the prompt. It is the default on purpose: a stray run of momo
// must not be able to execute anything.
type Echo struct{}

func (Echo) Name() string { return "echo" }
func (Echo) Run(_ context.Context, prompt string) string {
	return "echo: " + prompt
}

// Claude shells out to Claude Code.
//
// A chat channel wired to this is a remote code execution surface: whoever can post
// in an allowed room runs Claude Code on this machine as the user running momo.
type Claude struct {
	Workdir string
	// Timeout bounds one run so a wedged session cannot hang the caller forever.
	Timeout time.Duration
}

func (Claude) Name() string { return "claude" }

func (c Claude) Run(ctx context.Context, prompt string) string {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", prompt)
	cmd.Dir = c.Workdir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return "engine timed out after " + timeout.String()
	}
	// Partial output is still worth returning: a non-zero exit usually means the
	// tool said something useful before failing.
	if err != nil && len(bytes.TrimSpace(out)) == 0 {
		return "engine failed: " + err.Error()
	}
	return string(out)
}
