export GOTOOLCHAIN ?= go1.26.1

.PHONY: build linux run vet

build:
	go build -trimpath -o bin/paycenter ./cmd/paycenter

# Static Linux binary (pure-Go SQLite, no CGO)
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/linux-amd64/paycenter ./cmd/paycenter

run:
	go run ./cmd/paycenter

vet:
	go vet ./...
