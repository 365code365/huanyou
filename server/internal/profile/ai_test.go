package profile

import (
	"strings"
	"testing"

	"vpnplatform/server/internal/config"
)

func TestAIModeUsesSeveralNodes(t *testing.T) {
	mk := func(id string) *config.Node {
		return &config.Node{ID: id, Address: id + ".example.com", VisionPort: 443,
			Reality: config.RealityPublic{PublicKey: "LvdM4-nTD6fmK5sqQdG5iJrsZppcDBdRc-aEaYGV7Tc", ShortID: "ab", ServerName: "www.apple.com", Fingerprint: "chrome"}}
	}
	js, err := Build(Params{UUID: "u", Mode: ModeAI, Nodes: []*config.Node{mk("a"), mk("b")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proxy-vision-a", "proxy-vision-b", "leastPing", "openai.com"} {
		if !strings.Contains(js, want) {
			t.Fatalf("AI profile missing %q", want)
		}
	}
	if _, err := CompileJSON(js); err != nil {
		t.Fatalf("compile: %v", err)
	}
}
