// Package profile builds the Xray-core client configuration the mobile app runs.
//
// The app feeds this JSON straight into Xray-core (via the vpncore gomobile
// library) together with the TUN file descriptor. Generating it on the server
// lets us change protocols, nodes and routing without shipping a new app.
package profile

import (
	"encoding/json"
	"fmt"

	"vpnplatform/server/internal/config"
)

type Mode string

const (
	// ModeSmart: mainland China IPs go direct, everything else through the proxy.
	ModeSmart Mode = "smart"
	// ModeGlobal: everything except LAN goes through the proxy.
	ModeGlobal Mode = "global"
)

// CNGeoIPFile is shipped inside the app (see app/scripts/fetch_geo.sh).
const CNGeoIPFile = "geoip-only-cn-private.dat"

// TunDNS is the DNS server address the apps put on the TUN interface.
const TunDNS = "172.19.0.2"

var PrivateCIDRs = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "224.0.0.0/4",
	"240.0.0.0/4", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
}

// Build returns the client config JSON for user uuid on node n.
func Build(n *config.Node, uuid string, mode Mode) (string, error) {
	reality := func() map[string]any {
		return map[string]any{
			"fingerprint": n.Reality.Fingerprint,
			"serverName":  n.Reality.ServerName,
			"publicKey":   n.Reality.PublicKey,
			"shortId":     n.Reality.ShortID,
		}
	}
	vless := func(port int, flow string) map[string]any {
		user := map[string]any{"id": uuid, "encryption": "none"}
		if flow != "" {
			user["flow"] = flow
		}
		return map[string]any{"vnext": []any{map[string]any{
			"address": n.Address, "port": port, "users": []any{user},
		}}}
	}

	outbounds := []any{
		map[string]any{
			"tag":      "proxy-vision",
			"protocol": "vless",
			"settings": vless(n.VisionPort, "xtls-rprx-vision"),
			"streamSettings": map[string]any{
				"network":         "raw",
				"security":        "reality",
				"realitySettings": reality(),
			},
		},
	}
	proxySelectors := []string{"proxy-vision"}
	if n.XHTTPPath != "" {
		outbounds = append(outbounds, map[string]any{
			"tag":      "proxy-xhttp",
			"protocol": "vless",
			"settings": vless(n.VisionPort, ""),
			"streamSettings": map[string]any{
				"network":         "xhttp",
				"security":        "reality",
				"realitySettings": reality(),
				"xhttpSettings":   map[string]any{"path": n.XHTTPPath, "mode": "auto"},
			},
		})
		proxySelectors = append(proxySelectors, "proxy-xhttp")
	}
	outbounds = append(outbounds,
		map[string]any{"tag": "direct", "protocol": "freedom"},
		map[string]any{"tag": "block", "protocol": "blackhole"},
		map[string]any{"tag": "dns-out", "protocol": "dns"},
	)

	rules := []any{
		// The TUN advertises 172.19.0.2 as DNS; answer those queries with Xray's DNS module.
		map[string]any{"inboundTag": []string{"tun-in"}, "port": "53", "network": "tcp,udp", "outboundTag": "dns-out"},
		// Anything else to the virtual DNS IP (e.g. Android Private DNS probing :853) is
		// rejected immediately so the OS falls back to plain DNS without waiting.
		map[string]any{"ip": []string{TunDNS + "/32"}, "outboundTag": "block"},
		// Upstream DNS queries made by the DNS module go through the proxy (prevents DNS poisoning).
		map[string]any{"inboundTag": []string{"dns-module"}, "balancerTag": "proxy"},
		map[string]any{"ip": PrivateCIDRs, "outboundTag": "direct"},
	}
	if mode == ModeSmart {
		rules = append(rules, map[string]any{"ip": []string{"ext:" + CNGeoIPFile + ":cn"}, "outboundTag": "direct"})
	}
	rules = append(rules, map[string]any{"network": "tcp,udp", "balancerTag": "proxy"})

	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"dns": map[string]any{
			"tag":           "dns-module",
			"queryStrategy": "UseIPv4",
			"servers":       []string{"8.8.8.8", "1.1.1.1"},
		},
		"inbounds": []any{map[string]any{
			"tag":      "tun-in",
			"protocol": "tun",
			"port":     0,
			"settings": map[string]any{"name": "xray0", "MTU": 1500},
			"sniffing": map[string]any{
				"enabled": true, "routeOnly": true,
				"destOverride": []string{"http", "tls", "quic"},
			},
		}},
		"outbounds": outbounds,
		// Probe each transport; if one gets blocked/throttled the balancer moves traffic to the other.
		"observatory": map[string]any{
			"subjectSelector":   proxySelectors,
			"probeUrl":          "https://www.gstatic.com/generate_204",
			"probeInterval":     "60s",
			"enableConcurrency": true,
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"balancers": []any{map[string]any{
				"tag":         "proxy",
				"selector":    proxySelectors,
				"strategy":    map[string]any{"type": "leastPing"},
				"fallbackTag": "proxy-vision",
			}},
			"rules": rules,
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal profile: %w", err)
	}
	return string(b), nil
}
