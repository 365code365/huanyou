package vpncore

// Only the Xray features the client profile (server/internal/profile) uses.
// Importing main/distro/all instead pulls in every protocol and transport
// (VMess, Trojan, Shadowsocks, WireGuard, Hysteria/QUIC, gRPC, KCP,
// WebSocket, the gRPC commander, TOML/YAML loaders …) and roughly doubles the
// size of the native library. If the profile starts using another protocol,
// add its package here.
import (
	// core app features
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/dns"
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/observatory" // leastPing balancer in cluster mode
	_ "github.com/xtls/xray-core/app/policy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/app/router"

	// inbound: TUN; outbounds: VLESS to the node, freedom (direct), blackhole, dns
	_ "github.com/xtls/xray-core/proxy/blackhole"
	_ "github.com/xtls/xray-core/proxy/dns"
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/tun"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"

	// transports: RAW(TCP)+REALITY (Vision) and XHTTP+REALITY
	_ "github.com/xtls/xray-core/transport/internet/headers/noop"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/udp"

)
