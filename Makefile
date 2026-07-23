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
