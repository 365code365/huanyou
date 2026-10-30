// Package vpncore is the gomobile library embedded in the Android and iOS apps.
//
// It runs an Xray-core client whose "tun" inbound reads/writes packets on the
// TUN file descriptor provided by Android VpnService or the iOS
// NEPacketTunnelProvider. The client config JSON is produced by the API server
// (GET /api/v1/profile).
//
// Build:  gomobile bind -target=android ...   /   gomobile bind -target=ios ...
// (see mobile/build.sh)
package vpncore

import (
	"encoding/base64"
	"errors"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/xtls/xray-core/common/platform"
	core "github.com/xtls/xray-core/core"
	"google.golang.org/protobuf/proto"
	"github.com/xtls/xray-core/transport/internet"

)

// Protector excludes a socket from the VPN so Xray's own connections to the
// node don't loop back into the tunnel. Android: VpnService.protect(fd).
// iOS does not need it (pass nil): the extension's sockets bypass its own tunnel.
type Protector interface {
	Protect(fd int32) bool
}

var (
	mu        sync.Mutex
	instance  *core.Instance
	protector Protector
	hookOnce  sync.Once
)

func installHook() {
	hookOnce.Do(func() {
		_ = internet.RegisterDialerController(func(network, address string, c syscall.RawConn) error {
			mu.Lock()
			p := protector
			mu.Unlock()
			if p == nil {
				return nil
			}
			var perr error
			if err := c.Control(func(fd uintptr) {
				if !p.Protect(int32(fd)) {
					perr = errors.New("vpncore: protect socket failed")
				}
			}); err != nil {
				return err
			}
			return perr
		})
	})
}

// Start launches Xray with a compiled profile (base64 protobuf from the API,
// GET /api/v1/profile?format=pb) reading packets from tunFd. assetDir is kept
// for compatibility; compiled profiles carry their geo data inline.
func Start(config string, tunFd int32, assetDir string, p Protector) error {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		return errors.New("vpncore: already running")
	}
	if tunFd <= 0 {
		return errors.New("vpncore: invalid tun fd")
	}
	if err := os.Setenv(platform.TunFdKey, strconv.Itoa(int(tunFd))); err != nil {
		return err
	}
	if assetDir != "" {
		if err := os.Setenv(platform.AssetLocation, assetDir); err != nil {
			return err
		}
	}
	protector = p
	installHook()

	// The API compiles the profile to Xray's protobuf config (base64), so the
	// app needs neither Xray's JSON loaders nor geo files (see features.go).
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(config))
	if err != nil {
		return errors.New("vpncore: profile is not a compiled config; update the app or server")
	}
	cfg := &core.Config{}
	if err := proto.Unmarshal(raw, cfg); err != nil {
		return err
	}
	inst, err := core.New(cfg)
	if err != nil {
		return err
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return err
	}
	instance = inst
	return nil
}

// Stop shuts Xray down. Safe to call when not running.
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	if instance == nil {
		return nil
	}
	err := instance.Close()
	instance = nil
	protector = nil
	debug.FreeOSMemory()
	return err
}

func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return instance != nil
}

// SetMemoryLimit caps the Go heap (MiB). iOS Network Extensions are killed
// above ~50 MB, so the iOS app calls this with a value around 30.
func SetMemoryLimit(mib int64) {
	debug.SetMemoryLimit(mib << 20)
	debug.SetGCPercent(10)
}

func Version() string { return core.Version() }
