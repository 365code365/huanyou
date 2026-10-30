package profile

import (
	"strings"
	"testing"

	"vpnplatform/server/internal/config"
)

// The API's host goes direct and is resolved by the system resolver; the
// profile must still compile for the native core.
func TestDirectAPIEndpoints(t *testing.T) {
	n := &config.Node{ID: "n1", Address: "203.0.113.9", VisionPort: 443, XHTTPPath: "/x",
		Reality: config.RealityPublic{PublicKey: "LvdM4-nTD6fmK5sqQdG5iJrsZppcDBdRc-aEaYGV7Tc", ShortID: "abcd", ServerName: "www.apple.com"}}
	for _, m := range []Mode{ModeDirect, ModeCluster, ModeAI, ModeGlobal} {
		p := Params{UUID: "15c2fcd0-8042-4c22-8e70-e5e36b025e93", Mode: m, Nodes: []*config.Node{n},
			DirectDomains: []string{"api.example.com"}, DirectIPs: []string{"198.51.100.7"},
			DirectHosts: map[string][]string{"api.example.com": {"198.51.100.7"}}}
		js, err := Build(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"full:api.example.com"`, `"198.51.100.7"`, `"localhost"`} {
			if !strings.Contains(js, want) {
				t.Fatalf("%s: missing %s", m, want)
			}
		}
		if _, err := BuildProto(p); err != nil {
			t.Fatalf("%s: compile: %v", m, err)
		}
	}
}
