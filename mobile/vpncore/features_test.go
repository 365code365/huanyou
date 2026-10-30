package vpncore

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	core "github.com/xtls/xray-core/core"
	"google.golang.org/protobuf/proto"
)

// TestCompiledProfilesBuild checks that compiled profiles from the API
// (GET /api/v1/profile?format=pb, every mode) can be instantiated with the
// trimmed feature set in features.go — i.e. every protocol / transport they
// use is registered. Profiles are read from $VPNCORE_PROFILES/*.pb64.
func TestCompiledProfilesBuild(t *testing.T) {
	dir := os.Getenv("VPNCORE_PROFILES")
	if dir == "" {
		t.Skip("set VPNCORE_PROFILES to a directory of *.pb64 compiled profiles")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.pb64"))
	if len(files) == 0 {
		t.Fatal("no profiles found")
	}
	for _, f := range files {
		b64, _ := os.ReadFile(f)
		raw, err := base64.StdEncoding.DecodeString(string(b64))
		if err != nil {
			t.Fatal(err)
		}
		cfg := &core.Config{}
		if err := proto.Unmarshal(raw, cfg); err != nil {
			t.Fatalf("%s: unmarshal: %v", filepath.Base(f), err)
		}
		// The TUN inbound needs a real device; drop it, check everything else builds.
		cfg.Inbound = nil
		inst, err := core.New(cfg)
		if err != nil {
			t.Fatalf("%s: build: %v", filepath.Base(f), err)
		}
		inst.Close()
		t.Logf("%s ok (%d bytes)", filepath.Base(f), len(raw))
	}
}
