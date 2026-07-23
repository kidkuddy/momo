// Package config resolves which account momo is acting as.
//
// A profile is a directory holding one bot's entire identity: its credentials, its
// crypto store, its history, and its socket. Running two bots means two profiles and
// two daemons, with nothing shared — which matters because the crypto store cannot
// be shared even between processes of the same bot, let alone different ones.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile is one bot's paths. Settings themselves stay in the environment, which is
// where every other package already reads them from.
type Profile struct {
	// Name is empty for the legacy layout: files in the working directory, config
	// from the ambient environment. That is what an existing install looks like, so
	// it keeps working untouched.
	Name    string
	Dir     string
	State   string
	Crypto  string
	History string
	Socket  string
}

// Root is where profiles live. MOMO_HOME overrides it, mostly for tests.
func Root() string {
	if v := os.Getenv("MOMO_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".momo"
	}
	return filepath.Join(home, ".momo")
}

// Load resolves a profile and applies its config file to the environment.
//
// An empty name selects the legacy layout. Otherwise the profile directory is
// created if missing, and `<dir>/config` is read as KEY=VALUE lines.
//
// Variables already set in the environment win over the file, so a one-off
// `ENGINE=claude momo daemon` still overrides what the profile says.
func Load(name string) (*Profile, error) {
	if name == "" {
		return &Profile{
			State:   envOr("STATE_FILE", "state.json"),
			Crypto:  envOr("CRYPTO_DB", "momo.db"),
			History: envOr("HISTORY_DB", "history.db"),
			Socket:  envOr("MOMO_SOCKET", filepath.Join(os.TempDir(), "momo.sock")),
		}, nil
	}
	if strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("profile name %q must not contain a path separator", name)
	}

	dir := filepath.Join(Root(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := applyConfigFile(filepath.Join(dir, "config")); err != nil {
		return nil, err
	}

	p := &Profile{
		Name:    name,
		Dir:     dir,
		State:   envOr("STATE_FILE", filepath.Join(dir, "state.json")),
		Crypto:  envOr("CRYPTO_DB", filepath.Join(dir, "momo.db")),
		History: envOr("HISTORY_DB", filepath.Join(dir, "history.db")),
		// The socket lives in the profile directory rather than a shared temp path,
		// so two profiles cannot collide and an agent session always reaches the
		// daemon that spawned it.
		Socket: envOr("MOMO_SOCKET", filepath.Join(dir, "momo.sock")),
	}
	// Pass the profile down to anything momo spawns, so a `momo send` from inside an
	// agent session resolves the same profile rather than the default one.
	os.Setenv("MOMO_PROFILE", name)
	return p, nil
}

// applyConfigFile reads KEY=VALUE lines, skipping blanks and # comments. Values
// already present in the environment are left alone.
func applyConfigFile(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil // a profile without a config file is valid; env may supply everything
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	for line := 1; scan.Scan(); line++ {
		text := strings.TrimSpace(scan.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
	return scan.Err()
}

// List returns the profile names that exist.
func List() ([]string, error) {
	entries, err := os.ReadDir(Root())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
