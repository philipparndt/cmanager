PREFIX  ?= $(HOME)/.local
BINDIR  := $(PREFIX)/bin

.PHONY: all build run tidy install clean fmt vet test

all: build

## build: compile cmanager into ./bin
build: tidy
	@mkdir -p bin
	go build -o bin/cmanager .
	@# Ad-hoc re-sign so the binary runs cleanly on macOS after a rebuild.
	@command -v codesign >/dev/null 2>&1 && codesign -s - -f bin/cmanager 2>/dev/null || true
	@echo "built bin/cmanager"

## run: open the picker
run: tidy
	go run .

## tidy: resolve and download module dependencies
tidy:
	go mod tidy

## test: run tests
test:
	go test ./...

## install: build and copy the binary onto your PATH ($(BINDIR))
# install (unlink + fresh inode), not cp, to avoid macOS code-signature SIGKILL.
install: build
	@mkdir -p $(BINDIR)
	install -m 0755 bin/cmanager $(BINDIR)/cmanager
	@echo "installed cmanager into $(BINDIR)"
	@echo "next: wire the Claude Code hook and add the tmux snippet — see README.md"

## fmt: gofmt the source
fmt:
	go fmt ./...

## vet: run go vet
vet:
	go vet ./...

## clean: remove build artifacts
clean:
	rm -rf bin
