package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyLayoutIsUnchanged(t *testing.T) {
	// An install that predates profiles must keep working with files in the working
	// directory, or upgrading momo silently orphans its identity.
	p, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "" || p.State != "state.json" || p.Crypto != "momo.db" {
		t.Fatalf("legacy layout changed: %+v", p)
	}
}

func TestProfilePathsAreSelfContained(t *testing.T) {
	t.Setenv("MOMO_HOME", t.TempDir())
	p, err := Load("work")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.State, p.Crypto, p.History, p.Socket} {
		if filepath.Dir(path) != p.Dir {
			t.Errorf("%s escapes the profile directory %s", path, p.Dir)
		}
	}
	// Two daemons sharing a socket would answer for each other's accounts.
	if filepath.Dir(p.Socket) == os.TempDir() {
		t.Error("socket is in a shared location; profiles would collide")
	}
	if _, err := os.Stat(p.Dir); err != nil {
		t.Errorf("profile directory not created: %v", err)
	}
}

func TestConfigFileLoadsButEnvironmentWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOMO_HOME", home)
	dir := filepath.Join(home, "work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# a comment\n\nHOMESERVER=https://from-file\nENGINE=claude\nBOT_USER=\"quoted\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// An explicit override on the command line must beat the stored profile.
	t.Setenv("ENGINE", "echo")

	if _, err := Load("work"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("HOMESERVER"); got != "https://from-file" {
		t.Errorf("HOMESERVER = %q, want the file value", got)
	}
	if got := os.Getenv("BOT_USER"); got != "quoted" {
		t.Errorf("BOT_USER = %q, want quotes stripped", got)
	}
	if got := os.Getenv("ENGINE"); got != "echo" {
		t.Errorf("ENGINE = %q, want the environment to win", got)
	}
}

// The profile must propagate to spawned processes, or an agent session's `momo send`
// reaches the wrong daemon — or none.
func TestLoadExportsProfileName(t *testing.T) {
	t.Setenv("MOMO_HOME", t.TempDir())
	t.Setenv("MOMO_PROFILE", "")
	if _, err := Load("work"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("MOMO_PROFILE"); got != "work" {
		t.Errorf("MOMO_PROFILE = %q, want work", got)
	}
}

func TestProfileNameCannotEscapeRoot(t *testing.T) {
	t.Setenv("MOMO_HOME", t.TempDir())
	for _, name := range []string{"../escape", "a/b", `a\b`} {
		if _, err := Load(name); err == nil {
			t.Errorf("accepted profile name %q", name)
		}
	}
}
