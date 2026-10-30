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
	// ModeDirect: one node (CN IPs direct, everything else via that node).
	ModeDirect Mode = "direct"
	// ModeCluster: all enabled nodes behind one latency-balanced selector.
	ModeCluster Mode = "cluster"
	// ModeAI: one node, but only AI service domains go through the proxy.
	ModeAI Mode = "ai"
	// ModeGlobal: one node, everything (except LAN) via proxy.
	ModeGlobal Mode = "global"
)

func ParseMode(s string) Mode {
	switch Mode(s) {
	case ModeCluster:
		return ModeCluster
	case ModeAI:
		return ModeAI
	case ModeGlobal:
		return ModeGlobal
	default:
		return ModeDirect
	}
}

// CNGeoIPFile is shipped inside the app (see app/scripts/fetch_geo.sh).
const CNGeoIPFile = "geoip-only-cn-private.dat"

// TunDNS is the DNS server address the apps put on the TUN interface.
const TunDNS = "172.19.0.2"

var PrivateCIDRs = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "224.0.0.0/4",
	"240.0.0.0/4", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
}

// Params controls how a client profile is built.
type Params struct {
	UUID      string
	Mode      Mode
	Nodes     []*config.Node // one for direct/ai/global; all enabled for cluster
	AIDomains []string       // used only in ai mode
	// The API's own host names and addresses: the app's requests (and the
	// download site) go straight there, not through a node on the other side
	// of the world, and the host is resolved by the system, not over DoH.
	DirectDomains []string
	DirectIPs     []string
	// DirectHosts pins DirectDomains to these addresses (the API knows where
	// it runs), so the app's host name needs no lookup at all inside the tunnel.
	DirectHosts map[string][]string
}

// nodeOutbounds returns the outbound definitions and their tags for one node.
// suffix keeps tags unique when several nodes are combined (cluster mode).
func nodeOutbounds(n *config.Node, uuid, suffix string) (outbounds []any, tags []string) {
	reality := map[string]any{
		"fingerprint": n.Reality.Fingerprint,
		"serverName":  n.Reality.ServerName,
		"publicKey":   n.Reality.PublicKey,
		"shortId":     n.Reality.ShortID,
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

	visionTag := "proxy-vision" + suffix
	outbounds = append(outbounds, map[string]any{
		"tag":      visionTag,
		"protocol": "vless",
		"settings": vless(n.VisionPort, "xtls-rprx-vision"),
		"streamSettings": map[string]any{
			"network":         "raw",
			"security":        "reality",
			"realitySettings": reality,
		},
	})
	tags = append(tags, visionTag)

	if n.XHTTPPath != "" {
		xhttpTag := "proxy-xhttp" + suffix
		outbounds = append(outbounds, map[string]any{
			"tag":      xhttpTag,
			"protocol": "vless",
			"settings": vless(n.VisionPort, ""),
			"streamSettings": map[string]any{
				"network":         "xhttp",
				"security":        "reality",
				"realitySettings": reality,
				"xhttpSettings":   map[string]any{"path": n.XHTTPPath, "mode": "auto"},
			},
		})
		tags = append(tags, xhttpTag)
	}
	return outbounds, tags
}

// Build returns the client config JSON for the given params.
func Build(p Params) (string, error) {
	if len(p.Nodes) == 0 {
		return "", fmt.Errorf("profile: no nodes")
	}
	var outbounds []any
	var proxyTags []string

	// Cluster uses every node; AI mode gets several stable nodes from the API
	// and lets the leastPing balancer keep the AI traffic on a working one.
	if p.Mode == ModeCluster || (p.Mode == ModeAI && len(p.Nodes) > 1) {
		for _, n := range p.Nodes {
			obs, tags := nodeOutbounds(n, p.UUID, "-"+n.ID)
			outbounds = append(outbounds, obs...)
			proxyTags = append(proxyTags, tags...)
		}
	} else {
		obs, tags := nodeOutbounds(p.Nodes[0], p.UUID, "")
		outbounds = append(outbounds, obs...)
		proxyTags = append(proxyTags, tags...)
	}

	outbounds = append(outbounds,
		map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIP"}},
		map[string]any{"tag": "block", "protocol": "blackhole"},
		map[string]any{"tag": "dns-out", "protocol": "dns"},
	)

	rules := []any{
		// TUN advertises TunDNS; answer those with Xray's DNS module.
		map[string]any{"inboundTag": []string{"tun-in"}, "port": "53", "network": "tcp,udp", "outboundTag": "dns-out"},
		// DNS module's own upstream queries go through the proxy (prevents poisoning).
		map[string]any{"inboundTag": []string{"dns-module"}, "balancerTag": "proxy"},
		// Reject other traffic aimed at the virtual DNS IP (e.g. Android Private DNS :853).
		map[string]any{"ip": []string{TunDNS + "/32"}, "outboundTag": "block"},
		map[string]any{"ip": PrivateCIDRs, "outboundTag": "direct"},
	}
	if len(p.DirectDomains) > 0 {
		full := make([]string, 0, len(p.DirectDomains))
		for _, d := range p.DirectDomains {
			full = append(full, "full:"+d)
		}
		rules = append(rules, map[string]any{"domain": full, "outboundTag": "direct"})
	}
	if len(p.DirectIPs) > 0 {
		rules = append(rules, map[string]any{"ip": p.DirectIPs, "outboundTag": "direct"})
	}
	switch p.Mode {
	case ModeAI:
		// Only AI domains go through the proxy; everything else is direct.
		domains := p.AIDomains
		if len(domains) == 0 {
			domains = config.DefaultAIDomains
		}
		matchers := make([]string, 0, len(domains))
		for _, d := range domains {
			matchers = append(matchers, "domain:"+d)
		}
		rules = append(rules,
			map[string]any{"domain": matchers, "balancerTag": "proxy"},
			map[string]any{"network": "tcp,udp", "outboundTag": "direct"},
		)
	case ModeGlobal:
		rules = append(rules, map[string]any{"network": "tcp,udp", "balancerTag": "proxy"})
	default: // direct, cluster: mainland China direct, the rest proxied
		rules = append(rules,
			map[string]any{"ip": []string{"ext:" + CNGeoIPFile + ":cn"}, "outboundTag": "direct"},
			map[string]any{"network": "tcp,udp", "balancerTag": "proxy"},
		)
	}

	cfg := map[string]any{
		// Privacy: no per-connection or DNS logging on the device either.
		"log": map[string]any{"loglevel": "warning", "access": "none", "dnsLog": false},
		// DNS over HTTPS, routed through the tunnel (dns-module -> proxy rule):
		// neither the local network nor the node's upstream ISP sees the
		// queried names. IP-literal endpoints need no bootstrap resolution.
		"dns": dnsConfig(p.DirectDomains, p.DirectHosts),
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
		"observatory": map[string]any{
			"subjectSelector":   proxyTags,
			"probeUrl":          "https://www.gstatic.com/generate_204",
			"probeInterval":     "60s",
			"enableConcurrency": true,
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"balancers": []any{map[string]any{
				"tag":         "proxy",
				"selector":    proxyTags,
				"strategy":    map[string]any{"type": "leastPing"},
				"fallbackTag": proxyTags[0],
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

// dnsConfig: DNS over HTTPS through the tunnel, except the API's own host,
// which the system resolver answers (fast, and it is our own domain).
func dnsConfig(direct []string, hosts map[string][]string) map[string]any {
	servers := []any{}
	if len(direct) > 0 {
		full := make([]string, 0, len(direct))
		for _, d := range direct {
			full = append(full, "full:"+d)
		}
		servers = append(servers, map[string]any{"address": "localhost", "domains": full, "skipFallback": true})
	}
	servers = append(servers, "https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query")
	cfg := map[string]any{"tag": "dns-module", "queryStrategy": "UseIPv4", "servers": servers}
	if len(hosts) > 0 {
		h := map[string]any{}
		for d, ips := range hosts {
			h["full:"+d] = ips
		}
		cfg["hosts"] = h
	}
	return cfg
}
