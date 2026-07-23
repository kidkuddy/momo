package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kidkuddy/momo/internal/core"
)

// Claude runs a Claude Code session per message.
//
// The session is not asked to return an answer for momo to post. It is told where
// the conversation is and given momo's own CLI, and it replies for itself. That is
// the whole point: an agent that can send, upload a diff, react and open a poll is a
// participant in the room, not a function returning a string.
//
// momo still posts the transcript tail if the session finishes without having said
// anything, so a silent failure is visible rather than looking like a hang.
type Claude struct {
	// Binary is the executable to run. Defaults to "claude".
	Binary string
	// MomoBinary is the absolute path to momo itself, handed to the session so it
	// can reply. Without it the agent has no way to answer.
	MomoBinary string
	// Workdir bounds where a session operates when the task does not override it.
	Workdir string
	// Timeout bounds one run so a wedged session cannot hold the sync loop forever.
	Timeout time.Duration
	// SocketPath tells the spawned CLI to forward to the running daemon rather than
	// open the crypto store, which two processes cannot safely share.
	SocketPath string
	// PermissionMode controls what the session may do without asking.
	//
	// A headless session has nobody to ask, so anything short of bypassing the
	// prompt means the agent refuses the action and the reply never arrives — it
	// reports back that it needed approval, which no one will ever give.
	//
	// Bypassing is consistent with what momo already is: with this engine enabled,
	// the allowlisted user runs Claude Code on this host. The narrower posture is
	// AllowedTools. The real answer is an approval gate in the chat itself, which
	// is on the roadmap and not built.
	PermissionMode string
	// AllowedTools optionally narrows what the session may use, e.g.
	// "Bash(momo:*) Read Grep". Empty means whatever PermissionMode allows.
	AllowedTools string
	// Env is extra environment for the session, typically the Matrix config so the
	// forwarded CLI can fall back to direct access.
	Env []string
}

func (Claude) Name() string { return "claude" }

// claudeResult is the subset of `claude -p --output-format json` we rely on.
type claudeResult struct {
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Subtype   string `json:"subtype"`
}

func (c Claude) Run(ctx context.Context, t core.Task) (core.Answer, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bin := c.Binary
	if bin == "" {
		bin = "claude"
	}
	// A daemon inherits whatever PATH it was started with, which may contain a
	// per-session shim that vanishes. Resolve once, up front, so a long-running
	// daemon fails loudly at startup rather than silently weeks later.
	if resolved, err := exec.LookPath(bin); err == nil {
		bin = resolved
	}
	workdir := t.Workdir
	if workdir == "" {
		workdir = c.Workdir
	}

	args := c.baseArgs()
	// Resuming is what makes a thread one conversation. A stale or unknown id makes
	// claude fail outright, so a failed resume falls back to a fresh session below.
	if t.ResumeID != "" {
		args = append(args, "--resume", t.ResumeID)
	}
	args = append(args, c.brief(t))

	answer, err := c.exec(ctx, bin, args, workdir)
	if err != nil && t.ResumeID != "" {
		// The recorded session may have expired or been cleaned up. Losing the
		// thread's memory is much better than refusing to answer at all.
		args = append(c.baseArgs(), c.brief(t))
		answer, err = c.exec(ctx, bin, args, workdir)
	}
	return answer, err
}

func (c Claude) baseArgs() []string {
	args := []string{"-p", "--output-format", "json"}
	mode := c.PermissionMode
	if mode == "" {
		mode = "bypassPermissions"
	}
	if mode != "default" {
		args = append(args, "--permission-mode", mode)
	}
	if c.AllowedTools != "" {
		args = append(args, "--allowedTools", c.AllowedTools)
	}
	return args
}

func (c Claude) exec(ctx context.Context, bin string, args []string, workdir string) (core.Answer, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), c.Env...)
	if c.SocketPath != "" {
		cmd.Env = append(cmd.Env, "MOMO_SOCKET="+c.SocketPath)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return core.Answer{Reply: "the session timed out"}, nil
	}

	// Output is one JSON object. Parse it even when the exit code is non-zero: a
	// failed run still reports a session id worth keeping.
	var res claudeResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil {
		if runErr != nil {
			return core.Answer{}, fmt.Errorf("%s: %w: %s", bin, runErr, tail(stderr.String(), 400))
		}
		return core.Answer{}, fmt.Errorf("could not parse %s output: %w", bin, err)
	}
	if runErr != nil && res.SessionID == "" {
		return core.Answer{}, fmt.Errorf("%s failed: %s", bin, tail(stderr.String(), 400))
	}

	answer := core.Answer{SessionID: res.SessionID}
	// The session was told to reply through the CLI. If it did, the room already has
	// the answer and posting res.Result would duplicate it. There is no signal for
	// "did it call momo send", so the daemon decides by watching whether anything
	// was actually sent — see the bot. Here we only carry the text.
	answer.Reply = strings.TrimSpace(res.Result)
	if res.IsError {
		answer.Reply = "the session ended with an error: " + answer.Reply
	}
	return answer, nil
}

// brief is the prompt handed to the session. It tells the agent where it is, how to
// answer, and that answering is its job rather than returning text.
func (c Claude) brief(t core.Task) string {
	momo := c.MomoBinary
	if momo == "" {
		momo = "momo"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `You are answering a message in a Matrix chat. Reply by running the momo CLI.

Room:   %s
Thread: %s
From:   %s

To answer, run:

    %s send '%s' "your reply" --thread '%s'

That command is how you talk to the person. Anything momo can do in Matrix you can
do the same way: upload a file, react, edit a message, open a poll. Run
%s --help to see the commands, and read the matrix-cli skill if it is available.

Rules:
- Reply at least once. Silence reads as a hang.
- Keep replies short; this is a chat, not a report. Use a fenced code block for code.
- Quote the room and thread ids in single quotes: they contain characters the shell
  would otherwise expand.
- Do not include secrets, tokens or environment variables in a reply. Room history
  is durable.

The message:

%s`, t.RoomID, t.ThreadRoot, t.Sender, momo, t.RoomID, t.ThreadRoot, momo, t.Prompt)
	return b.String()
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
