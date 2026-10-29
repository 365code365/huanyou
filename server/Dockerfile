# Builds both binaries: /usr/local/bin/vpn-api and /usr/local/bin/vpn-node
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vpn-api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vpn-node ./cmd/node

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 vpn
COPY --from=build /out/ /usr/local/bin/
USER vpn
# api:  docker run ... vpn-api -config /etc/vpn/api.json
# node: docker run --network host ... vpn-node -config /etc/vpn/node.json
ENTRYPOINT []
