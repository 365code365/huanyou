export GOTOOLCHAIN ?= go1.26.1

.PHONY: build linux vet

build:
	go build -trimpath -o bin/ ./cmd/...

# Static Linux binaries for servers
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/linux-amd64/vpn-api ./cmd/api
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/linux-amd64/vpn-node ./cmd/node

vet:
	go vet ./...
