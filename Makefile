BINARY  := backfire
PREFIX  ?= /usr/local
BINDIR  := $(PREFIX)/bin
VERSION := $(shell cat VERSION 2>/dev/null)

.PHONY: all build install uninstall test vet tidy clean run panel bot

all: build

## build: compile the single binary into ./backfire
build:
	go build -trimpath -o $(BINARY) .

## install: build and copy the binary to $(BINDIR)
install: build
	install -Dm755 $(BINARY) $(BINDIR)/$(BINARY)

## uninstall: remove the installed binary
uninstall:
	rm -f $(BINDIR)/$(BINARY)

## test: run the unit and end-to-end tests
test:
	go test ./...

## vet: run go vet across the module
vet:
	go vet ./...

## tidy: sync go.mod / go.sum
tidy:
	go mod tidy

## clean: remove build artifacts
clean:
	rm -f $(BINARY)

## run: open the interactive menu from source
run:
	go run .

## panel: run the web panel from source (needs /etc/backfire/webui.json)
panel:
	go run . -webui

## bot: run the telegram bot from source (needs /etc/backfire/telegram.json)
bot:
	go run . -bot
