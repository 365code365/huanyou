package profile

import (
	"encoding/json"
	"strings"
	"testing"

	"vpnplatform/server/internal/config"
)

func dnsServers(t *testing.T, js string) []any {
	t.Helper()
	var cfg struct {
		DNS struct {
			Servers []any `json:"servers"`
		} `json:"dns"`
	}
	if err := json.Unmarshal([]byte(js), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.DNS.Servers
}

func TestDNSByMode(t *testing.T) {
	node := &config.Node{ID: "a", Address: "a.example.com", VisionPort: 443,
		Reality: config.RealityPublic{PublicKey: "LvdM4-nTD6fmK5sqQdG5iJrsZppcDBdRc-aEaYGV7Tc", ShortID: "ab", ServerName: "www.apple.com", Fingerprint: "chrome"}}
	build := func(m Mode) string {
		js, err := Build(Params{UUID: "u", Mode: m, Nodes: []*config.Node{node}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CompileJSON(js); err != nil {
			t.Fatalf("%s: compile: %v", m, err)
		}
		return js
	}
	// 智能: Chinese services resolved at home, only for those names; the rest via the tunnel.
	js := build(ModeDirect)
	srv := dnsServers(t, js)
	first, _ := srv[0].(map[string]any)
	if first["address"] != "https+local://223.5.5.5/dns-query" || first["skipFallback"] != true ||
		!strings.Contains(js, `"domain:bilibili.com"`) {
		t.Fatalf("direct mode servers: %v", srv)
	}
	if srv[len(srv)-1] != "https://8.8.8.8/dns-query" {
		t.Fatalf("default resolver must be tunnel DoH: %v", srv)
	}
	if !strings.Contains(js, `{"domain":["domain:cn"`) {
		t.Fatal("no direct rule for Chinese domains")
	}
	// AI 优先: AI names via the tunnel only, everything else at home.
	srv = dnsServers(t, build(ModeAI))
	ai, _ := srv[0].(map[string]any)
	if ai["address"] != "https://1.1.1.1/dns-query" || !strings.Contains(strings.Join(toStrings(ai["domains"]), ","), "openai.com") {
		t.Fatalf("ai mode: AI names must use tunnel DoH: %v", srv)
	}
	if srv[len(srv)-1] != "https+local://1.12.12.12/dns-query" {
		t.Fatalf("ai mode default must be domestic: %v", srv)
	}
	// 全局: everything through the tunnel.
	for _, s := range dnsServers(t, build(ModeGlobal)) {
		if str, _ := s.(string); strings.Contains(str, "+local") {
			t.Fatalf("global mode resolves at home: %v", s)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			s, _ := x.(string)
			out = append(out, s)
		}
	}
	return out
}
