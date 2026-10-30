// Package node runs an embedded Xray-core server (VLESS + REALITY) and keeps
// its user list in sync with the control-plane API without restarting.
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/xtls/xray-core/common/protocol"
	cserial "github.com/xtls/xray-core/common/serial"
	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/infra/conf/serial"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/vless"
	"google.golang.org/protobuf/types/known/emptypb"

	_ "github.com/xtls/xray-core/main/distro/all"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/profile"
)

const (
	TagVision  = "vless-vision"
	TagXHTTP   = "vless-xhttp"
	FlowVision = "xtls-rprx-vision"
)

// BuildServerConfig renders the Xray server JSON for this node (no users; they are added at runtime).
func BuildServerConfig(c *config.NodeAgent) ([]byte, error) {
	reality := map[string]any{
		"show":        false,
		"target":      c.Reality.Target,
		"xver":        0,
		"serverNames": c.Reality.ServerNames,
		"privateKey":  c.Reality.PrivateKey,
		"shortIds":    c.Reality.ShortIDs,
	}
	sniff := map[string]any{"enabled": true, "routeOnly": true, "destOverride": []string{"http", "tls", "quic"}}

	// Everything is served on ONE public port (443). REALITY terminates the
	// handshake; authenticated streams that are VLESS go to Vision, anything
	// else (the XHTTP client speaks HTTP/2) falls back to the internal XHTTP
	// inbound. Unauthenticated probes see the real target site.
	visionSettings := map[string]any{"clients": []any{}, "decryption": "none"}
	if c.XHTTPPath != "" {
		visionSettings["fallbacks"] = []any{map[string]any{"dest": c.XHTTPInternal, "xver": 0}}
	}
	visionStream := map[string]any{"network": "raw", "security": "reality", "realitySettings": reality}
	if c.AcceptProxyProtocol {
		visionStream["sockopt"] = map[string]any{"acceptProxyProtocol": true}
	}
	inbounds := []any{map[string]any{
		"tag": TagVision, "listen": c.Listen, "port": c.VisionPort, "protocol": "vless",
		"settings":       visionSettings,
		"streamSettings": visionStream,
		"sniffing":       sniff,
	}}
	if c.XHTTPPath != "" {
		host, port, err := splitListen(c.XHTTPInternal)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, map[string]any{
			"tag": TagXHTTP, "listen": host, "port": port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{}, "decryption": "none"},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "none",
				"xhttpSettings": map[string]any{"path": c.XHTTPPath},
			},
			"sniffing": sniff,
		})
	}

	cfg := map[string]any{
		// Privacy: never record what users visit. Xray writes an access line per
		// connection (destination host:port) unless access is "none", and DNS
		// queries unless dnsLog is false. Only warnings/errors are kept.
		"log":      map[string]any{"loglevel": c.LogLevel, "access": "none", "dnsLog": false},
		"inbounds": inbounds,
		// Egress fingerprint: the freedom outbound relays the client's raw TCP/UDP
		// payload verbatim to the exact destination the client targeted. "AsIs"
		// means the node performs no DNS re-resolution and does not rewrite the
		// destination, so the client's own TLS ClientHello (its real uTLS/browser
		// fingerprint) reaches the origin unchanged and no proxy headers
		// (Via / X-Forwarded-For) are ever added. To the destination this looks
		// like an ordinary client connection, not a proxied one.
		"outbounds": []any{
			map[string]any{"tag": "direct", "protocol": "freedom",
				"settings": map[string]any{"domainStrategy": "AsIs"}},
			map[string]any{"tag": "block", "protocol": "blackhole"},
		},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{
			// Free resources of idle / half-closed connections sooner so a busy
			// node can hold many more users: idle 180 s, and 2 s / 5 s after
			// one side finishes (defaults are 300 / 1 / 2 but held buffers longer).
			"handshake": 4, "connIdle": 180, "uplinkOnly": 2, "downlinkOnly": 5,
			// 64 KB per-connection buffer: enough for full speed, bounded memory
			// with thousands of connections (Xray's default scales with RAM).
			"bufferSize": 64,
			// Makes Xray wrap each user's traffic with our counters (traffic stats + speed limit).
			"statsUserUplink": true, "statsUserDownlink": true,
		}}},
		"routing": map[string]any{
			// Resolve domains so the private-range rule also catches e.g. "localhost.example" -> 127.0.0.1.
			"domainStrategy": "IPIfNonMatch",
			"rules": []any{
				// Never let users reach the node's own LAN / loopback / cloud metadata endpoints.
				map[string]any{"ip": profile.PrivateCIDRs, "outboundTag": "block"},
				map[string]any{"protocol": []string{"bittorrent"}, "outboundTag": "block"},
			},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// splitListen accepts "host:port" (TCP) or "@name" / "/path.sock" (unix socket, port 0).
func splitListen(addr string) (string, int, error) {
	if strings.HasPrefix(addr, "@") || strings.HasPrefix(addr, "/") {
		return addr, 0, nil
	}
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("xhttp_internal %q: %w", addr, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, fmt.Errorf("xhttp_internal %q: bad port", addr)
	}
	return host, port, nil
}

type Xray struct {
	inst *core.Instance
	ihm  inbound.Manager
	tags map[string]string // inbound tag -> VLESS flow
}

// StartXray starts the embedded Xray server. st receives per-user traffic and
// enforces per-user speed limits.
func StartXray(c *config.NodeAgent, st *Stats) (*Xray, error) {
	raw, err := BuildServerConfig(c)
	if err != nil {
		return nil, err
	}
	pb, err := serial.LoadJSONConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("load xray config: %w", err)
	}
	// Install our stats manager in place of Xray's (see stats.go).
	registerStats(st)
	pb.App = append(pb.App, cserial.ToTypedMessage(&emptypb.Empty{}))
	inst, err := core.New(pb)
	if err != nil {
		return nil, fmt.Errorf("create xray: %w", err)
	}
	if err := inst.Start(); err != nil {
		return nil, fmt.Errorf("start xray: %w", err)
	}
	ihm, ok := inst.GetFeature(inbound.ManagerType()).(inbound.Manager)
	if !ok {
		inst.Close()
		return nil, fmt.Errorf("xray inbound manager unavailable")
	}
	tags := map[string]string{TagVision: FlowVision}
	if c.XHTTPPath != "" {
		tags[TagXHTTP] = "" // Vision flow only applies to raw TCP
	}
	return &Xray{inst: inst, ihm: ihm, tags: tags}, nil
}

func (x *Xray) Close() error { return x.inst.Close() }

func (x *Xray) userManager(ctx context.Context, tag string) (proxy.UserManager, error) {
	h, err := x.ihm.GetHandler(ctx, tag)
	if err != nil {
		return nil, err
	}
	gi, ok := h.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("inbound %s: no proxy", tag)
	}
	um, ok := gi.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("inbound %s: not a user manager", tag)
	}
	return um, nil
}

// AddUser registers a VLESS user on every inbound.
func (x *Xray) AddUser(ctx context.Context, email, uuid string) error {
	for tag, flow := range x.tags {
		um, err := x.userManager(ctx, tag)
		if err != nil {
			return err
		}
		mu, err := (&protocol.User{
			Email:   email,
			Account: cserial.ToTypedMessage(&vless.Account{Id: uuid, Flow: flow, Encryption: "none"}),
		}).ToMemoryUser()
		if err != nil {
			return fmt.Errorf("user %s: %w", email, err)
		}
		if err := um.AddUser(ctx, mu); err != nil {
			return fmt.Errorf("add %s to %s: %w", email, tag, err)
		}
	}
	return nil
}

// RemoveUser removes a VLESS user from every inbound.
func (x *Xray) RemoveUser(ctx context.Context, email string) error {
	var firstErr error
	for tag := range x.tags {
		um, err := x.userManager(ctx, tag)
		if err == nil {
			err = um.RemoveUser(ctx, email)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("remove %s from %s: %w", email, tag, err)
		}
	}
	return firstErr
}
