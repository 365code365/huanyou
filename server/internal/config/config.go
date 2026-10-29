// Package config loads JSON configuration for the API server and node agent.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// RealityPublic is the client-side half of a node's REALITY settings.
type RealityPublic struct {
	PublicKey   string `json:"public_key"`
	ShortID     string `json:"short_id"`
	ServerName  string `json:"server_name"` // SNI shown on the wire, must be in node's server_names
	Fingerprint string `json:"fingerprint"` // uTLS fingerprint, default "chrome"
}

// Node is how the API server sees a proxy node.
type Node struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Region  string `json:"region"`
	Address string `json:"address"` // public IP or domain the app dials
	// Token authenticates the node agent when it pulls the user list. Secret.
	Token   string        `json:"token"`
	Reality RealityPublic `json:"reality"`
	// VisionPort: public port (443). Serves VLESS + XTLS-Vision + REALITY.
	VisionPort int `json:"vision_port"`
	// XHTTPPath: if set, the same port also serves VLESS + XHTTP + REALITY
	// (second transport for when Vision is disrupted). Must match the node.
	XHTTPPath string `json:"xhttp_path"`
}

type API struct {
	Listen        string `json:"listen"`
	Database      string `json:"database"`
	JWTSecret     string `json:"jwt_secret"` // or env VPN_JWT_SECRET
	TokenTTLHours int    `json:"token_ttl_hours"`
	AllowRegister bool   `json:"allow_register"`
	// TrustProxyHeader: use X-Forwarded-For for client IP (only behind your own reverse proxy).
	TrustProxyHeader bool   `json:"trust_proxy_header"`
	Nodes            []Node `json:"nodes"`
}

func LoadAPI(path string) (*API, error) {
	c := &API{Listen: ":8080", Database: "data/vpn.db", TokenTTLHours: 24 * 30, AllowRegister: true}
	if err := loadJSON(path, c); err != nil {
		return nil, err
	}
	if v := os.Getenv("VPN_JWT_SECRET"); v != "" {
		c.JWTSecret = v
	}
	seen := map[string]bool{}
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.ID == "" || n.Address == "" || n.VisionPort == 0 {
			return nil, fmt.Errorf("node #%d: id, address and vision_port are required", i)
		}
		if seen[n.ID] {
			return nil, fmt.Errorf("duplicate node id %q", n.ID)
		}
		seen[n.ID] = true
		if len(n.Token) < 24 {
			return nil, fmt.Errorf("node %s: token must be at least 24 characters", n.ID)
		}
		if n.Reality.PublicKey == "" || n.Reality.ServerName == "" {
			return nil, fmt.Errorf("node %s: reality.public_key and reality.server_name are required", n.ID)
		}
		if n.Reality.Fingerprint == "" {
			n.Reality.Fingerprint = "chrome"
		}
		if n.XHTTPPath != "" && !strings.HasPrefix(n.XHTTPPath, "/") {
			return nil, fmt.Errorf("node %s: xhttp_path must start with /", n.ID)
		}
	}
	return c, nil
}

// NodeAgent is the configuration of a proxy node.
type NodeAgent struct {
	APIURL              string `json:"api_url"`
	Token               string `json:"token"` // same value as Node.Token on the API side
	SyncIntervalSeconds int    `json:"sync_interval_seconds"`
	LogLevel            string `json:"log_level"`

	Listen     string `json:"listen"`
	VisionPort int    `json:"vision_port"` // public port, keep 443
	// XHTTPPath enables the XHTTP fallback transport on the same public port ("" = off).
	XHTTPPath string `json:"xhttp_path"`
	// XHTTPInternal is where the internal XHTTP inbound listens (loopback or unix socket).
	XHTTPInternal string `json:"xhttp_internal"`

	Reality struct {
		PrivateKey string   `json:"private_key"`
		ShortIDs   []string `json:"short_ids"`
		// Target: a real, foreign TLS 1.3 + HTTP/2 site the node impersonates,
		// e.g. "www.microsoft.com:443". Unauthenticated probes are forwarded here.
		Target      string   `json:"target"`
		ServerNames []string `json:"server_names"`
	} `json:"reality"`
}

func LoadNode(path string) (*NodeAgent, error) {
	c := &NodeAgent{SyncIntervalSeconds: 30, LogLevel: "warning", Listen: "0.0.0.0", VisionPort: 443,
		XHTTPInternal: "127.0.0.1:2001"}
	if err := loadJSON(path, c); err != nil {
		return nil, err
	}
	if v := os.Getenv("VPN_NODE_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("VPN_REALITY_PRIVATE_KEY"); v != "" {
		c.Reality.PrivateKey = v
	}
	switch {
	case c.APIURL == "" || c.Token == "":
		return nil, errors.New("api_url and token are required")
	case c.Reality.PrivateKey == "" || c.Reality.Target == "" || len(c.Reality.ServerNames) == 0:
		return nil, errors.New("reality.private_key, reality.target and reality.server_names are required")
	case len(c.Reality.ShortIDs) == 0:
		return nil, errors.New("reality.short_ids must not be empty")
	case c.XHTTPPath != "" && !strings.HasPrefix(c.XHTTPPath, "/"):
		return nil, errors.New("xhttp_path must start with /")
	}
	return c, nil
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
