PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin
HOOK_SRC := hook/cmanager-hook.sh
HOOK_DST := $(HOME)/.claude/cmanager/cmanager-hook.sh

.PHONY: all build run tidy install install-hook clean fmt vet test

all: build

## build: compile cmanager and cld into ./bin
build: tidy
	@mkdir -p bin
	go build -o bin/cmanager .
	go build -o bin/cld ./cmd/cld
	@# Ad-hoc re-sign so the binaries run cleanly on macOS after a rebuild.
	@command -v codesign >/dev/null 2>&1 && codesign -s - -f bin/cmanager bin/cld 2>/dev/null || true
	@echo "built bin/cmanager and bin/cld"

## run: run the cmanager TUI
run: tidy
	go run .

## tidy: resolve and download module dependencies
tidy:
	go mod tidy

## test: run the smoke tests
test:
	go test ./...

## install: build and copy both binaries onto your PATH ($(BINDIR))
# Use `install` (unlink + create a fresh inode) rather than `cp` (truncate in
# place): on macOS, overwriting a previously-run binary in place invalidates the
# kernel's cached code signature and the next run is SIGKILLed ("zsh: killed").
install: build
	@mkdir -p $(BINDIR)
	install -m 0755 bin/cmanager $(BINDIR)/cmanager
	install -m 0755 bin/cld $(BINDIR)/cld
	@echo "installed cmanager and cld into $(BINDIR)"

## install-hook: copy the Notification hook into ~/.claude/cmanager
install-hook:
	@mkdir -p $(HOME)/.claude/cmanager
	cp $(HOOK_SRC) $(HOOK_DST)
	chmod +x $(HOOK_DST)
	@echo "installed hook at $(HOOK_DST)"
	@echo "now add it to ~/.claude/settings.json (see README.md)"

## fmt: gofmt the source
fmt:
	go fmt ./...

## vet: run go vet
vet:
	go vet ./...

## clean: remove build artifacts
clean:
	rm -rf bin
