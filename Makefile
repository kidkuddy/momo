# goolm is mautrix's pure-Go olm. Without this tag it links libolm through cgo and
# the build needs olm.h from the system. Every go command here must carry it.
TAGS := -tags=goolm
BIN  := momo

build:
	go build $(TAGS) -o $(BIN) ./cmd/momo

test:
	go test $(TAGS) ./...

vet:
	go vet $(TAGS) ./...

fmt:
	gofmt -w ./cmd ./internal

check: fmt vet test

run: build
	set -a; . ./.env; set +a; ./$(BIN) daemon

# The recovery key lives in the login keychain, so these need no arguments.
KEY = $(shell security find-generic-password -s momo-matrix-recovery-key -w 2>/dev/null)

crosssign: build
	set -a; . ./.env; set +a; ./$(BIN) crosssign "$(KEY)"

backup: build
	set -a; . ./.env; set +a; ./$(BIN) backup "$(KEY)"

restore: build
	set -a; . ./.env; set +a; ./$(BIN) restore "$(KEY)"

clean:
	rm -f $(BIN)

.PHONY: build test vet fmt check run crosssign backup restore clean

# ---- run as a background service (macOS) --------------------------------
# PROFILE=<name> is required; the daemon runs under that profile's identity.
PROFILE ?= momo
PLIST   := $(HOME)/Library/LaunchAgents/com.github.kidkuddy.momo.$(PROFILE).plist
# The service must NOT run the repo's build output: rebuilding replaces the file
# under a starting process and wedges it in dyld before main() runs.
PREFIX  ?= $(HOME)/.local/bin
INSTALLED := $(PREFIX)/momo

install: build
	@mkdir -p $(PREFIX)
	@install -m 0755 $(BIN) $(INSTALLED)
	@echo "installed $(INSTALLED)"

# Renders the plist. Load it yourself with `make service-load` once you have
# read it — it starts momo at every login.
service: install
	@mkdir -p $(HOME)/Library/LaunchAgents
	@sed -e 's|__BINARY__|$(INSTALLED)|g' \
	     -e 's|__PROFILE__|$(PROFILE)|g' \
	     -e 's|__DIR__|$(HOME)/.momo/$(PROFILE)|g' \
	     contrib/momo.plist.template > $(PLIST)
	@echo "wrote $(PLIST)"
	@echo "review it, then: make service-load"

service-load:
	launchctl bootstrap gui/$$(id -u) $(PLIST)
	@echo "momo is running under launchd; logs at $(HOME)/.momo/$(PROFILE)/momo.log"

service-unload:
	launchctl bootout gui/$$(id -u)/com.github.kidkuddy.momo.$(PROFILE) 2>/dev/null || true
	@echo "momo service stopped"

service-status:
	@launchctl print gui/$$(id -u)/com.github.kidkuddy.momo.$(PROFILE) 2>/dev/null \
	  | grep -E "state|pid|last exit" || echo "not loaded"

.PHONY: install service service-load service-unload service-status

# ---- skills -------------------------------------------------------------
# Symlink momo's skills into ~/.claude/skills so any Claude Code session — a
# krakoa workflow step, a cron, an interactive session — can drive momo without
# knowing where this repo lives. Symlinks, so editing here updates them.
SKILLS := $(HOME)/.claude/skills

install-skills:
	@mkdir -p $(SKILLS)
	@for s in .claude/skills/*/; do \
	  name=$$(basename $$s); \
	  rm -rf "$(SKILLS)/$$name"; \
	  ln -s "$(CURDIR)/$$s" "$(SKILLS)/$$name"; \
	  echo "linked $$name"; \
	done

uninstall-skills:
	@for s in .claude/skills/*/; do \
	  name=$$(basename $$s); \
	  [ -L "$(SKILLS)/$$name" ] && rm "$(SKILLS)/$$name" && echo "removed $$name" || true; \
	done

.PHONY: install-skills uninstall-skills
