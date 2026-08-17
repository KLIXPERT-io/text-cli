.PHONY: build vet test fmt install clean release release-snapshot

VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)

build:
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o text ./cmd/text

vet:
	go vet ./...

test:
	go test ./...

fmt:
	gofmt -l -w ./cmd ./internal

install:
	go install -ldflags "-s -w -X main.version=$(VERSION)" ./cmd/text

clean:
	rm -rf text dist/

release-snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean
