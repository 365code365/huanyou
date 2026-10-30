package profile

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xtls/xray-core/common/platform"
	"github.com/xtls/xray-core/infra/conf/serial"
	"google.golang.org/protobuf/proto"

	// Config builders for every feature a profile can reference.
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Clients receive their profile as a compiled Xray protobuf config instead of
// JSON. The app then doesn't need Xray's JSON / geo-file loaders at all (a
// much smaller native library), and the China IP list for smart routing is
// compiled into the profile, so the app doesn't ship the geo file either.

//go:embed geoip-only-cn-private.dat
var cnGeoIP []byte

var (
	assetOnce sync.Once
	assetErr  error
)

// useEmbeddedAssets writes the embedded geo file to a private temp dir and
// points Xray's asset loader at it (needed once to compile "ext:" rules).
func useEmbeddedAssets() error {
	assetOnce.Do(func() {
		dir, err := os.MkdirTemp("", "vpn-geo-")
		if err != nil {
			assetErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, CNGeoIPFile), cnGeoIP, 0o600); err != nil {
			assetErr = err
			return
		}
		assetErr = os.Setenv(platform.AssetLocation, dir)
	})
	return assetErr
}

// BuildProto builds the profile and compiles it to an Xray protobuf config,
// base64-encoded for transport in JSON.
func BuildProto(p Params) (string, error) {
	js, err := Build(p)
	if err != nil {
		return "", err
	}
	return CompileJSON(js)
}

// CompileJSON compiles an Xray JSON config to base64 protobuf.
func CompileJSON(js string) (string, error) {
	if err := useEmbeddedAssets(); err != nil {
		return "", fmt.Errorf("profile assets: %w", err)
	}
	cfg, err := serial.LoadJSONConfig(strings.NewReader(js))
	if err != nil {
		return "", fmt.Errorf("compile profile: %w", err)
	}
	b, err := proto.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal profile: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
