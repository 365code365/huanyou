// huanyou-core is the Windows (desktop) VPN core. The Flutter app starts it
// as a child process and talks to it over stdin / stdout:
//
//	huanyou-core run      first stdin line: the compiled profile (base64
//	                      protobuf from GET /api/v1/profile?format=pb).
//	                      stdout: one JSON line per state change, e.g.
//	                      {"status":"connected","port":49152} or
//	                      {"status":"error","message":"..."}.
//	                      Any later stdin line ("stop") or EOF stops the core,
//	                      restores the system proxy and exits. EOF also covers
//	                      the app crashing or being closed.
//	huanyou-core restore  puts back the system proxy saved by a run that did
//	                      not exit cleanly (killed, power loss). The app calls
//	                      it on start.
//	huanyou-core version  prints the Xray version.
//
// Desktop runs in system-proxy mode: Xray listens on a loopback mixed
// (HTTP + SOCKS5) port and the Windows proxy setting points to it. This needs
// neither administrator rights nor a TUN driver. The profile's routing
// (China direct, AI only, global) is kept as is.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdnet "net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/proxy/socks" // mixed inbound: socks/server.go answers HTTP proxy requests too
	"google.golang.org/protobuf/proto"

	_ "vpnplatform/mobile/vpncore" // registers the Xray features the profile uses
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "run":
		os.Exit(run())
	case "restore":
		if err := restoreProxy(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(core.Version())
	default:
		fmt.Fprintln(os.Stderr, "usage: huanyou-core run|restore|version")
		os.Exit(2)
	}
}

var out = json.NewEncoder(os.Stdout)

func emit(status, message string, port int) {
	m := map[string]any{"status": status}
	if message != "" {
		m["message"] = message
	}
	if port > 0 {
		m["port"] = port
	}
	_ = out.Encode(m)
}

func run() int {
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		emit("error", "没有收到连接配置", 0)
		return 1
	}
	emit("connecting", "", 0)

	// Stop on "stop", stdin EOF (app gone) or a console signal. Watched from
	// the start, so a connect can be cancelled while it is starting.
	// "reload <profile>" swaps the profile in place (another node or mode)
	// on the same local port: the system proxy is not touched at all.
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	reloads := make(chan string, 4)
	go func() {
		for {
			l, err := in.ReadString('\n')
			if cfg, ok := strings.CutPrefix(strings.TrimSpace(l), "reload "); ok {
				reloads <- cfg
				if err == nil {
					continue
				}
			}
			stop()
			return
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		stop()
	}()
	stopped := func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}

	inst, port, err := start(strings.TrimSpace(line))
	if stopped() {
		if inst != nil {
			inst.Close()
		}
		emit("disconnected", "", 0)
		return 0
	}
	if err != nil {
		emit("error", err.Error(), 0)
		return 1
	}
	if err := setProxy(port); err != nil {
		inst.Close()
		_ = restoreProxy() // undo the services already switched
		if stopped() {
			emit("disconnected", "", 0)
			return 0
		}
		emit("error", "无法设置系统代理："+err.Error(), 0)
		return 1
	}
	if !stopped() {
		emit("connected", "", port)
	wait:
		for {
			select {
			case <-done:
				break wait
			case cfg := <-reloads:
				emit("connecting", "", 0)
				next, err := build(cfg, port)
				if err == nil {
					inst.Close()
					inst = nil
					if err = next.Start(); err != nil {
						next.Close()
					}
				}
				if err != nil {
					// The old profile is gone or the new one is bad: stop
					// cleanly rather than leave a dead proxy behind.
					emit("error", "切换线路失败："+err.Error(), 0)
					stop()
					break wait
				}
				inst = next
				emit("connected", "", port)
			}
		}
	}
	emit("disconnecting", "", 0)
	// macOS reports back as soon as the network in use is restored (the
	// other services follow); elsewhere restoreProxy does it all at once.
	reported := false
	closeInst := func() {
		if inst != nil {
			inst.Close()
			inst = nil
		}
	}
	onPrimary = func() {
		closeInst()
		emit("disconnected", "", 0)
		reported = true
	}
	perr := restoreProxy()
	if !reported {
		closeInst()
	}
	if perr != nil {
		emit("error", "恢复系统代理失败："+perr.Error(), 0)
		return 1
	}
	if !reported {
		emit("disconnected", "", 0)
	}
	return 0
}

// start decodes the compiled profile, swaps its TUN inbound for a loopback
// mixed proxy inbound on a free port and starts Xray.
func start(config string) (*core.Instance, int, error) {
	port, err := freePort()
	if err != nil {
		return nil, 0, err
	}
	inst, err := build(config, port)
	if err != nil {
		return nil, 0, err
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return nil, 0, err
	}
	return inst, port, nil
}

// build prepares (does not start) an Xray instance for the profile on port.
func build(config string, port int) (*core.Instance, error) {
	raw, err := base64.StdEncoding.DecodeString(config)
	if err != nil {
		return nil, errors.New("连接配置格式不对，请更新 App 或稍后重试")
	}
	cfg := &core.Config{}
	if err := proto.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	inbound, err := mixedInbound(port)
	if err != nil {
		return nil, err
	}
	cfg.Inbound = []*core.InboundHandlerConfig{inbound}
	if err := resolveForIPRules(cfg); err != nil {
		return nil, err
	}
	return core.New(cfg)
}

func mixedInbound(port int) (*core.InboundHandlerConfig, error) {
	return &core.InboundHandlerConfig{
		Tag: "proxy-in",
		ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
			PortList: &net.PortList{Range: []*net.PortRange{net.SinglePortRange(net.Port(port))}},
			Listen:   net.NewIPOrDomain(net.LocalHostIP),
			SniffingSettings: &proxyman.SniffingConfig{
				Enabled:             true,
				RouteOnly:           true,
				DestinationOverride: []string{"http", "tls", "quic"},
			},
		}),
		ProxySettings: serial.ToTypedMessage(&socks.ServerConfig{
			AuthType:   socks.AuthType_NO_AUTH,
			UdpEnabled: true,
			Address:    net.NewIPOrDomain(net.LocalHostIP),
		}),
	}, nil
}

// resolveForIPRules: through TUN the destination is already an IP, so the
// profile's IP rules (private ranges, mainland China direct) match directly.
// Proxy clients send host names instead; IPOnDemand resolves them (via the
// profile's DNS-over-HTTPS) when an IP rule is reached.
func resolveForIPRules(cfg *core.Config) error {
	for i, app := range cfg.App {
		if app.Type != serial.GetMessageType(&router.Config{}) {
			continue
		}
		msg, err := app.GetInstance()
		if err != nil {
			return err
		}
		rc := msg.(*router.Config)
		if rc.DomainStrategy == router.Config_AsIs {
			rc.DomainStrategy = router.Config_IpOnDemand
		}
		cfg.App[i] = serial.ToTypedMessage(rc)
	}
	return nil
}

func freePort() (int, error) {
	l, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*stdnet.TCPAddr).Port, nil
}
