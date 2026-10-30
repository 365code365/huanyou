package node

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	core "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
	"golang.org/x/crypto/curve25519"

	"vpnplatform/server/internal/config"
)

// TestNodeEndToEnd runs the node's Xray server and a real Xray client on this
// machine and checks, for both transports (Vision and XHTTP via the REALITY
// fallback): traffic flows, the client's real source IP is reported for the
// user (not loopback), and SetBlockedSources cuts that source off and back on.
//
// Needs internet access (REALITY target and a public destination):
//
//	VPN_TEST_E2E=1 go test ./internal/node -run TestNodeEndToEnd -v
func TestNodeEndToEnd(t *testing.T) {
	if os.Getenv("VPN_TEST_E2E") == "" {
		t.Skip("set VPN_TEST_E2E=1")
	}
	lan := lanIP(t)
	priv := make([]byte, 32)
	rand.Read(priv)
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	nodePort, xhttpPort := freePort(t), freePort(t)

	c := &config.NodeAgent{LogLevel: "warning", Listen: "0.0.0.0", VisionPort: nodePort,
		XHTTPPath: "/xh", XHTTPInternal: fmt.Sprintf("127.0.0.1:%d", xhttpPort)}
	c.Reality.PrivateKey = enc.EncodeToString(priv)
	c.Reality.ShortIDs = []string{"abcd"}
	c.Reality.Target = "www.apple.com:443"
	c.Reality.ServerNames = []string{"www.apple.com"}

	st := NewStats()
	x, err := StartXray(c, st)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	const uuid = "4f1b1b8e-6d2a-4c3e-9b5a-1f2e3d4c5b6a"
	if err := x.AddUser(context.Background(), "u42", uuid); err != nil {
		t.Fatal(err)
	}

	for _, transport := range []string{"vision", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			socks := freePort(t)
			stream := map[string]any{"network": "raw"}
			user := fmt.Sprintf(`{"id":%q,"encryption":"none","flow":"xtls-rprx-vision"}`, uuid)
			if transport == "xhttp" {
				stream = map[string]any{"network": "xhttp", "xhttpSettings": map[string]any{"path": "/xh"}}
				user = fmt.Sprintf(`{"id":%q,"encryption":"none"}`, uuid)
			}
			stream["security"] = "reality"
			stream["realitySettings"] = map[string]any{"serverName": "www.apple.com", "fingerprint": "chrome",
				"publicKey": enc.EncodeToString(pub), "shortId": "abcd"}
			cfg := fmt.Sprintf(`{"log":{"loglevel":"warning"},
				"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"udp":false}}],
				"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":%q,"port":%d,"users":[%s]}]},
				"streamSettings":%s}]}`, socks, lan, nodePort, user, mustJSON(stream))
			pb, err := serial.LoadJSONConfig(strings.NewReader(cfg))
			if err != nil {
				t.Fatal(err)
			}
			client, err := core.New(pb)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			st.DrainIPs() // reset
			if err := fetch(socks); err != nil {
				t.Fatalf("request through %s: %v", transport, err)
			}
			ips := st.DrainIPs()["u42"]
			if _, ok := ips[lan]; !ok {
				t.Fatalf("source %s not recorded for u42, got %v", lan, ips)
			}
			if _, ok := ips["127.0.0.1"]; ok {
				t.Fatalf("loopback recorded: %v", ips)
			}

			if err := x.SetBlockedSources([]string{lan + "/32"}); err != nil {
				t.Fatal(err)
			}
			if err := fetch(socks); err == nil {
				t.Fatal("blocked source still got through")
			}
			if err := x.SetBlockedSources(nil); err != nil {
				t.Fatal(err)
			}
			if err := fetch(socks); err != nil {
				t.Fatalf("after unblocking: %v", err)
			}
		})
	}
}

// fetch requests a public page through the local SOCKS5 port.
func fetch(socksPort int) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(12 * time.Second))
	host := "www.baidu.com"
	conn.Write([]byte{5, 1, 0})
	r := bufio.NewReader(conn)
	if b := make([]byte, 2); readFull(r, b) != nil || b[1] != 0 {
		return fmt.Errorf("socks greeting")
	}
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, 80)
	conn.Write(req)
	rep := make([]byte, 10)
	if err := readFull(r, rep); err != nil || rep[1] != 0 {
		return fmt.Errorf("socks connect: %v %v", err, rep)
	}
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	line, err := r.ReadString('\n')
	if err != nil {
		return fmt.Errorf("no response: %w", err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 ") {
		return fmt.Errorf("bad response %q", line)
	}
	return nil
}

func readFull(r io.Reader, b []byte) error { _, err := io.ReadFull(r, b); return err }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// lanIP is a non-loopback IPv4 address of a physical interface (en*/eth*),
// so the node sees a real source address. VPN tunnel addresses are skipped.
func lanIP(t *testing.T) string {
	if v := os.Getenv("VPN_TEST_LAN_IP"); v != "" {
		return v
	}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || !(strings.HasPrefix(i.Name, "en") || strings.HasPrefix(i.Name, "eth")) {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
				return n.IP.String()
			}
		}
	}
	t.Skip("no LAN address; set VPN_TEST_LAN_IP")
	return ""
}
