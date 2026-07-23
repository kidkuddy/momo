package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func serve(t *testing.T, h Handler) string {
	t.Helper()
	// macOS caps unix socket paths near 104 bytes, and t.TempDir() is already long.
	path := filepath.Join(t.TempDir(), "s")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, h) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	waitLive(t, path)
	return path
}

func waitLive(t *testing.T, path string) {
	t.Helper()
	for range 100 {
		if isLive(path) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket never came up")
}

func TestRoundTrip(t *testing.T) {
	path := serve(t, func(_ context.Context, req Request) (string, error) {
		return fmt.Sprintf("%s:%v", req.Command, req.Args), nil
	})
	got, err := Send(path, Request{Command: "send", Args: []string{"!r", "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "send:[!r hi]"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A failure in the daemon must reach the caller as a failure, not a silent success:
// an agent that thinks it replied when it did not leaves the room hanging.
func TestErrorsPropagate(t *testing.T) {
	path := serve(t, func(context.Context, Request) (string, error) {
		return "", errors.New("room not found")
	})
	if _, err := Send(path, Request{Command: "send"}); err == nil {
		t.Fatal("error did not propagate")
	} else if err.Error() != "room not found" {
		t.Fatalf("got %q", err)
	}
}

// No daemon is a normal condition — the CLI falls back to opening the store itself,
// so this must be distinguishable from a real failure.
func TestNoDaemonIsDistinct(t *testing.T) {
	_, err := Send(filepath.Join(t.TempDir(), "absent"), Request{Command: "send"})
	if !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("got %v, want ErrNoDaemon", err)
	}
}

// A daemon that crashed leaves its socket file behind. Binding must still succeed,
// or momo never starts again without manual cleanup.
func TestStaleSocketIsReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s")
	if err := writeFile(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, path, func(context.Context, Request) (string, error) {
			return "ok", nil
		})
	}()
	waitLive(t, path)

	got, err := Send(path, Request{Command: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("got %q", got)
	}
}

func writeFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}
