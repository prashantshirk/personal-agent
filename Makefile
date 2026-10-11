.PHONY: build test linux

build:
	mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o bin/secondbrain ./cmd/secondbrain

test:
	go test ./...

linux:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$${GOARCH:-amd64} go build -trimpath -ldflags="-s -w" -o bin/secondbrain-linux ./cmd/secondbrain
