package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// GET /admin/api/server/info: the machine this API runs on, its public IP,
// where the domain resolves to (is it this server?), the certificate users
// see, and which catalog node shares the server. Shown on the backup / clone
// page so an operator knows which server is live before migrating.

var (
	startedAt = time.Now()

	publicIPMu  sync.Mutex
	publicIPVal string
	publicIPAt  time.Time

	binHashOnce sync.Once
	binHashVal  string
)

func (s *Server) adminServerInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	host, _ := os.Hostname()
	local := localIPs()
	pub := publicIP(ctx)
	mine := map[string]bool{}
	for _, ip := range local {
		mine[ip] = true
	}
	if pub != "" {
		mine[pub] = true
	}

	domain := ""
	if u, err := url.Parse(s.cfg.PublicURL); err == nil {
		domain = u.Hostname()
	}
	var resolved []string
	pointsHere := mine[domain] || domain == "127.0.0.1" || domain == "localhost"
	if domain != "" && net.ParseIP(domain) == nil {
		resolved = resolveA(ctx, domain)
		for _, ip := range resolved {
			if mine[ip] {
				pointsHere = true
			}
		}
	}

	// Catalog nodes on this machine.
	var nodes []map[string]any
	for _, n := range s.cat().Nodes {
		if mine[n.Address] {
			nodes = append(nodes, map[string]any{"id": n.ID, "name": n.Name, "port": n.VisionPort, "disabled": n.Disabled})
		}
	}

	mode := "behind_proxy" // nginx (or similar) terminates TLS in front
	switch {
	case s.cfg.TLS.Domain != "" && s.cfg.TLS.Passthrough != "":
		mode = "tls_shared"
	case s.cfg.TLS.Domain != "":
		mode = "tls"
	case !s.cfg.TrustProxyHeader:
		mode = "plain"
	}

	out := map[string]any{
		"hostname":       host,
		"os":             osName(),
		"kernel":         readTrim("/proc/sys/kernel/osrelease"),
		"arch":           runtime.GOARCH,
		"cpus":           runtime.NumCPU(),
		"load":           loadAvg(),
		"uptime_seconds": hostUptime(),
		"process_uptime": int64(time.Since(startedAt).Seconds()),
		"go_version":     runtime.Version(),
		"binary_sha256":  binaryHash(),
		"public_ip":      pub,
		"local_ips":      local,
		"domain":         domain,
		"resolved":       resolved,
		"points_here":    pointsHere,
		"tls_mode":       mode,
		"passthrough":    s.cfg.TLS.Passthrough,
		"nodes_here":     nodes,
		"db_driver":      s.store.Driver(),
		"time":           time.Now().Format(time.RFC3339),
	}
	if mem := memInfo(); mem != nil {
		out["memory"] = mem
	}
	if d := diskInfo(s.cfg.Database); d != nil {
		out["disk"] = d
	}
	if domain != "" && net.ParseIP(domain) == nil {
		if c := peerCert(ctx, domain); c != nil {
			out["cert"] = c
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func localIPs() []string {
	var ips []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		ips = append(ips, ipn.IP.String())
	}
	return ips
}

// publicIP asks a couple of "what is my IP" services, cached for 10 minutes.
func publicIP(ctx context.Context) string {
	publicIPMu.Lock()
	cached, fresh := publicIPVal, publicIPVal != "" && time.Since(publicIPAt) < 10*time.Minute
	publicIPMu.Unlock()
	if fresh {
		return cached
	}
	hc := &http.Client{Timeout: 4 * time.Second}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip", "https://ipv4.icanhazip.com"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		req.Header.Set("User-Agent", "curl/8")
		resp, err := hc.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if ip := strings.TrimSpace(string(b)); net.ParseIP(ip) != nil {
			publicIPMu.Lock()
			publicIPVal, publicIPAt = ip, time.Now()
			publicIPMu.Unlock()
			return ip
		}
	}
	return cached
}

// resolveA asks public resolvers (not /etc/hosts or a local cache) where the
// domain points, which is what users see.
func resolveA(ctx context.Context, domain string) []string {
	for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53", "223.5.5.5:53"} {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "udp", server)
		}}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		ips, err := r.LookupIP(c, "ip4", domain)
		cancel()
		if err == nil && len(ips) > 0 {
			out := make([]string, 0, len(ips))
			for _, ip := range ips {
				out = append(out, ip.String())
			}
			return out
		}
	}
	ips, _ := net.DefaultResolver.LookupHost(ctx, domain)
	return ips
}

// peerCert is the certificate served for the domain (through DNS, like a user).
func peerCert(ctx context.Context, domain string) map[string]any {
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 4 * time.Second}, Config: &tls.Config{ServerName: domain}}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(domain, "443"))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer c.Close()
	st := c.(*tls.Conn).ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return nil
	}
	cert := st.PeerCertificates[0]
	return map[string]any{
		"issuer":    strings.TrimSpace(strings.Join(cert.Issuer.Organization, " ") + " " + cert.Issuer.CommonName),
		"not_after": cert.NotAfter.Format(time.RFC3339),
		"days_left": int(time.Until(cert.NotAfter).Hours() / 24),
		"remote":    c.RemoteAddr().String(),
	}
}

func osName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func readTrim(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func loadAvg() []float64 {
	f := strings.Fields(readTrim("/proc/loadavg"))
	var out []float64
	for i := 0; i < 3 && i < len(f); i++ {
		v, _ := strconv.ParseFloat(f[i], 64)
		out = append(out, v)
	}
	return out
}

func hostUptime() int64 {
	f := strings.Fields(readTrim("/proc/uptime"))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return int64(v)
}

func memInfo() map[string]int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	kb := map[string]int64{}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			v, _ := strconv.ParseInt(f[1], 10, 64)
			kb[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	}
	return map[string]int64{"total": kb["MemTotal"], "available": kb["MemAvailable"]}
}

func diskInfo(dbPath string) map[string]int64 {
	dir := "/"
	if dbPath != "" && !strings.Contains(dbPath, "://") && !strings.Contains(dbPath, "@") {
		dir = dbPath
		if fi, err := os.Stat(dbPath); err == nil && !fi.IsDir() {
			dir = dbPath[:max(strings.LastIndex(dbPath, "/"), 1)]
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil
	}
	return map[string]int64{"total": int64(st.Blocks) * int64(st.Bsize), "free": int64(st.Bavail) * int64(st.Bsize)}
}

func binaryHash() string {
	binHashOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(exe)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			binHashVal = hex.EncodeToString(h.Sum(nil))[:12]
		}
	})
	return binHashVal
}

// apiEndpoints is this API's host name and public addresses, for profiles to
// send the app's own requests direct. Never blocks: the public IP is looked
// up in the background and cached (local interface addresses cover servers
// whose public IP is on an interface).
func (s *Server) apiEndpoints() (hosts, ips []string) {
	if u, err := url.Parse(s.cfg.PublicURL); err == nil && u.Hostname() != "" {
		h := u.Hostname()
		if ip := net.ParseIP(h); ip != nil {
			if !ip.IsLoopback() && !ip.IsPrivate() {
				ips = append(ips, h)
			}
		} else if h != "localhost" {
			hosts = append(hosts, h)
		}
	}
	seen := map[string]bool{}
	add := func(ip string) {
		p := net.ParseIP(ip)
		if p == nil || p.To4() == nil || p.IsLoopback() || p.IsPrivate() || p.IsLinkLocalUnicast() || seen[ip] {
			return
		}
		seen[ip] = true
		ips = append(ips, ip)
	}
	for _, ip := range ips {
		seen[ip] = true
	}
	for _, ip := range localIPs() {
		add(ip)
	}
	publicIPMu.Lock()
	pub, stale := publicIPVal, time.Since(publicIPAt) > 10*time.Minute
	publicIPMu.Unlock()
	add(pub)
	if stale && publicIPRefreshing.CompareAndSwap(false, true) {
		go func() {
			defer publicIPRefreshing.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			publicIP(ctx)
		}()
	}
	return hosts, ips
}

var publicIPRefreshing atomic.Bool

var (
	apiDNSMu   sync.Mutex
	apiDNS     = map[string][]string{} // domain -> public A records
	apiDNSAt   time.Time
	apiDNSBusy atomic.Bool
)

// apiHosts pins each API domain to the addresses it resolves to publicly,
// but only when those are this server's own addresses (behind a CDN or load
// balancer the pin would be wrong). Resolution runs in the background.
func (s *Server) apiHosts(domains, mine []string) map[string][]string {
	if len(domains) == 0 || len(mine) == 0 {
		return nil
	}
	own := map[string]bool{}
	for _, ip := range mine {
		own[ip] = true
	}
	apiDNSMu.Lock()
	stale := time.Since(apiDNSAt) > 10*time.Minute
	out := map[string][]string{}
	for _, d := range domains {
		ips := apiDNS[d]
		ok := len(ips) > 0
		for _, ip := range ips {
			if !own[ip] {
				ok = false
			}
		}
		if ok {
			out[d] = ips
		}
	}
	apiDNSMu.Unlock()
	if stale && apiDNSBusy.CompareAndSwap(false, true) {
		go func() {
			defer apiDNSBusy.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			m := map[string][]string{}
			for _, d := range domains {
				m[d] = resolveA(ctx, d)
			}
			apiDNSMu.Lock()
			apiDNS, apiDNSAt = m, time.Now()
			apiDNSMu.Unlock()
		}()
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
