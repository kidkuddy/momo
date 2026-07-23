// Package ipc lets a second momo process act in Matrix through the running daemon
// instead of opening the crypto store itself.
//
// This exists for a correctness reason, not convenience. The daemon owns an olm
// account and the megolm ratchets that go with it. Two processes loading the same
// account and encrypting concurrently can each save a ratchet advanced from the same
// starting index, so two different messages go out under one message index. That is
// a silent cryptographic fault, and it is exactly what would happen when an agent
// session shells out to `momo send` while the daemon is running.
//
// So: if a daemon is listening, the CLI forwards the command to it over a unix
// socket and prints whatever comes back. If not, it opens the store directly, which
// is safe because nothing else holds it.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Request is one CLI invocation forwarded to the daemon.
type Request struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Response is what the daemon made of it.
type Response struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// Handler runs a forwarded command. It returns what the CLI should print.
type Handler func(ctx context.Context, req Request) (string, error)

// ErrNoDaemon means nothing is listening, so the caller should proceed directly.
var ErrNoDaemon = errors.New("no daemon listening")

// Listen binds the socket. It is separate from Serve so the daemon can claim the
// path *before* it opens the crypto store, which takes seconds of network work.
// Without that ordering there is a window where a CLI command sees no socket, falls
// back to opening the store directly, and contends with the daemon that is still
// starting.
//
// A stale socket file from a crashed daemon is removed first, since bind fails on an
// existing path even when nobody is behind it.
func Listen(path string) (net.Listener, error) {
	if isLive(path) {
		return nil, fmt.Errorf("another daemon is already listening on %s", path)
	}
	_ = os.Remove(path)

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// The socket carries the power to post as the bot, so keep it to this user.
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve binds and serves in one step, for callers with nothing to set up first.
func Serve(ctx context.Context, path string, h Handler) error {
	ln, err := Listen(path)
	if err != nil {
		return err
	}
	return ServeOn(ctx, ln, h)
}

// ServeOn serves on an already-bound listener until ctx is cancelled.
func ServeOn(ctx context.Context, ln net.Listener, h Handler) error {
	defer func() {
		ln.Close()
		if addr := ln.Addr(); addr != nil {
			os.Remove(addr.String())
		}
	}()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutting down, not a failure
			}
			return err
		}
		go handle(ctx, conn, h)
	}
}

func handle(ctx context.Context, conn net.Conn, h Handler) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))

	var req Request
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		return
	}
	out, err := h(ctx, req)
	resp := Response{Output: out}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// Send forwards a command to the daemon. It returns ErrNoDaemon when nothing is
// listening, which is a normal condition and not a failure.
func Send(path string, req Request) (string, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return "", ErrNoDaemon
	}
	defer conn.Close()
	// Generous: the command may be queued behind an engine run.
	_ = conn.SetDeadline(time.Now().Add(15 * time.Minute))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return "", err
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return "", fmt.Errorf("daemon closed the connection: %w", err)
	}
	if resp.Error != "" {
		return resp.Output, errors.New(resp.Error)
	}
	return resp.Output, nil
}

// isLive distinguishes a daemon that is running from a socket file left behind by
// one that crashed.
func isLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
