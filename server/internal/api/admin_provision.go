package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/geoip"
	"vpnplatform/server/internal/keys"
	"vpnplatform/server/internal/provision"
)

// systemdUnit is the service installed on a new node (mirrors deploy/vpn-node.service).
const systemdUnit = `[Unit]
Description=VPN proxy node (Xray VLESS + REALITY)
After=network-online.target
Wants=network-online.target

[Service]
User=vpn
Group=vpn
ExecStart=/usr/local/bin/vpn-node -config /etc/vpn/node.json
Restart=always
RestartSec=3
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`

// adminGeoIP resolves an address to a country/city so the node form can
// auto-fill the region. GET /admin/api/geoip?ip=1.2.3.4
func (s *Server) adminGeoIP(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请提供 IP 或域名")
		return
	}
	loc, err := geoip.Lookup(r.Context(), ip)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "geoip_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ip": loc.IP, "country_code": loc.CountryCode, "country": loc.Country,
		"region": loc.Region, "city": loc.City, "suggested_name": loc.SuggestedName(),
	})
}

// provisionBody is the shared request for precheck and install.
type provisionBody struct {
	Host       string `json:"host"`
	Port       int    `json:"port"` // SSH port, default 22
	User       string `json:"user"` // default root
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	NodePort   int    `json:"node_port"` // public port the node listens on, default 443
	Name       string `json:"name"`      // optional, auto from geo if empty
	Region     string `json:"region"`    // optional, auto from geo if empty
	ServerName string `json:"server_name"`
	// Rental info, required for new nodes (provision): expiry and vendor.
	ExpiresAt *time.Time `json:"expires_at"`
	Vendor    string     `json:"vendor"`
	VendorURL string     `json:"vendor_url"`
}

func (b provisionBody) target() provision.Target {
	return provision.Target{Host: strings.TrimSpace(b.Host), Port: b.Port, User: b.User,
		Password: b.Password, PrivateKey: b.PrivateKey}
}

func (b provisionBody) nodePort() int {
	if b.NodePort > 0 && b.NodePort <= 65535 {
		return b.NodePort
	}
	return 443
}

// adminPrecheckNode SSHes in and runs a read-only environment check, so the
// operator sees whether the box is suitable (and what to fix) before installing.
func (s *Server) adminPrecheckNode(w http.ResponseWriter, r *http.Request) {
	var body provisionBody
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Host) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写服务器地址")
		return
	}
	if body.Password == "" && strings.TrimSpace(body.PrivateKey) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写 SSH 密码或私钥")
		return
	}
	bins, _ := s.loadNodeBinaries()
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	rep, err := provision.Check(ctx, body.target(), body.nodePort(), func(a string) bool {
		_, ok := bins[a]
		return ok
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ssh_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// adminProvisionNode installs vpn-node on a server over SSH, then registers it
// in the catalog. The operator only supplies the SSH address and credentials;
// REALITY keys, token and region are generated / detected automatically.
func (s *Server) adminProvisionNode(w http.ResponseWriter, r *http.Request) {
	var body provisionBody
	if !decode(w, r, &body) {
		return
	}
	host := strings.TrimSpace(body.Host)
	if host == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写服务器地址")
		return
	}
	for _, n := range s.cat().Nodes {
		if strings.EqualFold(n.Address, host) {
			writeErr(w, http.StatusConflict, "node_exists",
				fmt.Sprintf("%s 已经是节点「%s」，重复安装会覆盖它的配置。如需重装请先删除该节点。", host, n.Name))
			return
		}
	}
	if body.Password == "" && strings.TrimSpace(body.PrivateKey) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写 SSH 密码或私钥")
		return
	}
	if body.ExpiresAt == nil {
		writeErr(w, http.StatusBadRequest, "no_expiry", "请选择服务器到期时间")
		return
	}
	// Architecture-matched binaries to upload.
	bins, err := s.loadNodeBinaries()
	if err != nil {
		writeErr(w, http.StatusFailedDependency, "no_binary", err.Error())
		return
	}

	// Geo-detect region / name from the address (best effort).
	region := strings.ToUpper(strings.TrimSpace(body.Region))
	name := strings.TrimSpace(body.Name)
	if region == "" || name == "" {
		if loc, err := geoip.Lookup(r.Context(), host); err == nil {
			if region == "" {
				region = loc.CountryCode
			}
			if name == "" {
				name = loc.SuggestedName()
			}
		}
	}
	if name == "" {
		name = host
	}

	serverName := strings.TrimSpace(body.ServerName)
	if serverName == "" {
		serverName = "www.apple.com"
	}
	realityTarget := s.cfg.Provision.RealityTarget
	if !strings.Contains(realityTarget, ":") {
		realityTarget = serverName + ":443"
	}

	// Generate node credentials.
	tok, err := keys.RandomToken(32)
	if err != nil {
		s.internal(w, err)
		return
	}
	priv, pub, sid, err := keys.RealityKeys()
	if err != nil {
		s.internal(w, err)
		return
	}
	xpath, err := keys.ShortID()
	if err != nil {
		s.internal(w, err)
		return
	}
	nodeID, err := newNodeID(region)
	if err != nil {
		s.internal(w, err)
		return
	}
	visionPort := body.nodePort()

	apiURL := s.cfg.PublicURL
	if apiURL == "" {
		writeErr(w, http.StatusFailedDependency, "no_public_url",
			"请先在 api.json 配置 public_url，新节点需要用它回连 API")
		return
	}
	nodeCfg := map[string]any{
		"api_url":               apiURL,
		"token":                 tok,
		"sync_interval_seconds": 30,
		"log_level":             "warning",
		"listen":                "0.0.0.0",
		"vision_port":           visionPort,
		"xhttp_path":            "/" + xpath[:12],
		"xhttp_internal":        "127.0.0.1:2001",
		"reality": map[string]any{
			"private_key":  priv,
			"short_ids":    []string{sid},
			"target":       realityTarget,
			"server_names": []string{serverName},
		},
	}
	nodeJSON, _ := json.MarshalIndent(nodeCfg, "", "  ")

	// From here on the response is a stream of NDJSON events so the panel can
	// show live progress: {"type":"log","text":...} ... then one "done"/"error".
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	emit := func(v map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(v)
		_ = rc.Flush()
	}
	emit(map[string]any{"type": "log", "text": fmt.Sprintf("目标 %s，地区 %s，名称 %s\n", host, region, name)})

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()
	res, provErr := provision.Run(ctx, body.target(), provision.Install{
		NodeJSON: nodeJSON, ServiceUnit: []byte(systemdUnit),
		Binaries: bins, Port: visionPort,
		Log: writerFunc(func(p []byte) { emit(map[string]any{"type": "log", "text": string(p)}) }),
	})
	if provErr != nil {
		emit(map[string]any{"type": "error", "message": provErr.Error()})
		return
	}

	// Install succeeded: register the node in the catalog.
	node := config.Node{
		ID: nodeID, Name: name, Region: region, Address: host,
		Token: tok, RealityPrivateKey: priv, RealityTarget: realityTarget,
		VisionPort: visionPort, XHTTPPath: "/" + xpath[:12], Weight: 1,
		ExpiresAt: body.ExpiresAt, Vendor: strings.TrimSpace(body.Vendor), VendorURL: strings.TrimSpace(body.VendorURL),
		Reality: config.RealityPublic{
			PublicKey: pub, ShortID: sid, ServerName: serverName, Fingerprint: "chrome",
		},
	}
	next, err := s.live.Update(func(c *config.Catalog) error {
		c.Nodes = append(c.Nodes, node)
		return nil
	}, s.saveCatalog(r))
	if err != nil {
		emit(map[string]any{"type": "error", "message": "节点已安装，但保存到后台失败：" + err.Error()})
		return
	}
	if s.monitor != nil {
		go s.monitor.CheckNow(context.Background())
	}
	emit(map[string]any{"type": "done", "node": s.adminNodeView(next.NodeByID(nodeID)), "arch": res.Arch})
}

// writerFunc adapts a callback to io.Writer.
type writerFunc func(p []byte)

func (f writerFunc) Write(p []byte) (int, error) {
	f(append([]byte(nil), p...))
	return len(p), nil
}

// loadNodeBinaries reads the configured vpn-node binaries, keyed by `uname -m`.
func (s *Server) loadNodeBinaries() (map[string][]byte, error) {
	out := map[string][]byte{}
	add := func(unameArch, path string) error {
		if path == "" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil // missing binary is not fatal unless none are available
		}
		out[unameArch] = b
		return nil
	}
	_ = add("x86_64", s.cfg.Provision.BinaryAmd64)
	_ = add("aarch64", s.cfg.Provision.BinaryArm64)
	if b, ok := out["aarch64"]; ok {
		out["arm64"] = b
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有可用的 vpn-node 二进制，请在 api.json 的 provision.binary_amd64 / binary_arm64 配置路径")
	}
	return out, nil
}

// newNodeID builds a catalog-valid id like "us-3f9a" from a region code.
func newNodeID(region string) (string, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	prefix := strings.ToLower(strings.TrimSpace(region))
	if prefix == "" {
		prefix = "node"
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b[:])), nil
}

// adminUpgradeNode pushes the API server's current vpn-node binary to an
// installed node over SSH and restarts it. POST /admin/api/nodes/{id}/upgrade
func (s *Server) adminUpgradeNode(w http.ResponseWriter, r *http.Request) {
	n := s.cat().NodeByID(r.PathValue("id"))
	if n == nil {
		writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
		return
	}
	var body provisionBody
	if !decode(w, r, &body) {
		return
	}
	if body.Password == "" && strings.TrimSpace(body.PrivateKey) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写 SSH 密码或私钥")
		return
	}
	body.Host = n.Address
	bins, err := s.loadNodeBinaries()
	if err != nil {
		writeErr(w, http.StatusFailedDependency, "no_binary", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	var log strings.Builder
	arch, err := provision.Upgrade(ctx, body.target(), bins, &log)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "upgrade_failed", "message": err.Error(), "log": log.String()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "arch": arch, "log": log.String()})
}
