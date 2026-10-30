//go:build darwin

package main

import (
	"errors"
	"fmt"
	stdnet "net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// macOS: the HTTP, HTTPS and SOCKS proxies of every enabled network service
// (System Settings → Network → <service> → Proxies) are pointed at the core
// with networksetup. Administrator accounts (the default first account) may
// do this without a password; standard accounts get an error.
//
// Speed: every networksetup write goes through configd and they serialize
// (≈ 0.1–0.2 s each), and a Mac easily has 7+ services (Wi-Fi, Thunderbolt
// Bridge, iPhone USB, other VPNs…). So:
//   - the service carrying the default route is switched first and the core
//     reports "connected" / "disconnected" right after it; the other services
//     follow in the background (they only matter after a network change);
//   - each service needs at most 4 calls (setting a proxy server also turns
//     it on; the bypass list is only written when it differs).

type proxyKind struct{ get, set, state string }

var kinds = []proxyKind{
	{"-getwebproxy", "-setwebproxy", "-setwebproxystate"},
	{"-getsecurewebproxy", "-setsecurewebproxy", "-setsecurewebproxystate"},
	{"-getsocksfirewallproxy", "-setsocksfirewallproxy", "-setsocksfirewallproxystate"},
}

var bypassDomains = []string{
	"localhost", "127.0.0.1", "::1", "*.local", "169.254/16",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
}

type proxyEntry struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
	Port    string `json:"port"`
}

type serviceProxy struct {
	Service string       `json:"service"`
	Proxies []proxyEntry `json:"proxies"` // same order as kinds
	Bypass  []string     `json:"bypass"`
}

func networksetup(args ...string) (string, error) {
	out, err := exec.Command("/usr/sbin/networksetup", args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	// networksetup often exits 0 on failure and only prints "** Error: …".
	if err == nil && strings.Contains(s, "Error") {
		err = errors.New(s)
	} else if err != nil && s != "" {
		err = errors.New(s)
	}
	return s, err
}

// services lists the enabled network services (Wi-Fi, Ethernet, …), the one
// carrying the default route first.
func services() ([]string, error) {
	out, err := networksetup("-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	var list []string
	for i, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if i == 0 && strings.Contains(l, "asterisk") || l == "" || strings.HasPrefix(l, "*") {
			continue
		}
		list = append(list, l)
	}
	if len(list) == 0 {
		return nil, errors.New("没有可用的网络服务")
	}
	if p := primaryService(); p != "" {
		for i, s := range list {
			if s == p {
				list[0], list[i] = list[i], list[0]
				break
			}
		}
	}
	return list, nil
}

// primaryService is the network service in use (e.g. "Wi-Fi" for en0): the
// one of the default route's interface, or, when that is a tunnel of another
// VPN app (utunN), the first service in the service order whose interface is
// up with an IPv4 address. "" if none can be told.
func primaryService() string {
	order, err := networksetup("-listnetworkserviceorder")
	if err != nil {
		return ""
	}
	// "(1) Wi-Fi" followed by "(Hardware Port: Wi-Fi, Device: en0)".
	type svcDev struct{ name, dev string }
	var list []svcDev
	name := ""
	for _, l := range strings.Split(order, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "(Hardware Port:") {
			if _, d, ok := strings.Cut(l, "Device: "); ok && name != "" {
				if d = strings.TrimSuffix(d, ")"); d != "" {
					list = append(list, svcDev{name, d})
				}
			}
			name = ""
			continue
		}
		if _, rest, ok := strings.Cut(l, ") "); ok && strings.HasPrefix(l, "(") && !strings.HasPrefix(rest, "*") {
			name = rest
		}
	}
	if out, err := exec.Command("/sbin/route", "-n", "get", "default").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && k == "interface" {
				for _, s := range list {
					if s.dev == strings.TrimSpace(v) {
						return s.name
					}
				}
			}
		}
	}
	for _, s := range list {
		ifc, err := stdnet.InterfaceByName(s.dev)
		if err != nil || ifc.Flags&stdnet.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*stdnet.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				return s.name
			}
		}
	}
	return ""
}

// readService reads a service's proxy setting. raw keeps a dead loopback
// proxy as it is (used to see what restore has to undo); otherwise it is
// recorded as off (see staleOurs).
func readService(svc string, raw ...bool) (serviceProxy, error) {
	keep := len(raw) > 0 && raw[0]
	sp := serviceProxy{Service: svc, Proxies: make([]proxyEntry, len(kinds))}
	errs := make([]error, len(kinds)+1)
	var wg sync.WaitGroup
	for i, k := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := networksetup(k.get, svc)
			if err != nil {
				errs[i] = err
				return
			}
			var e proxyEntry
			for _, l := range strings.Split(out, "\n") {
				key, val, _ := strings.Cut(l, ":")
				val = strings.TrimSpace(val)
				switch strings.TrimSpace(key) {
				case "Enabled":
					e.Enabled = val == "Yes"
				case "Server":
					e.Server = val
				case "Port":
					e.Port = val
				}
			}
			if keep {
				sp.Proxies[i] = e
			} else {
				sp.Proxies[i] = staleOurs(e)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, err := networksetup("-getproxybypassdomains", svc)
		if err != nil {
			errs[len(kinds)] = err
			return
		}
		if !strings.Contains(out, "There aren't any") {
			for _, l := range strings.Split(out, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					sp.Bypass = append(sp.Bypass, l)
				}
			}
		}
	}()
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return sp, e
		}
	}
	return sp, nil
}

// staleOurs: a loopback proxy whose port nobody listens on is a leftover of a
// core that was killed; saving it as "the user's setting" would bring a dead
// proxy back on restore, so it is recorded as switched off.
func staleOurs(e proxyEntry) proxyEntry {
	if !e.Enabled || (e.Server != "127.0.0.1" && e.Server != "localhost") {
		return e
	}
	c, err := stdnet.DialTimeout("tcp", stdnet.JoinHostPort("127.0.0.1", e.Port), 300*time.Millisecond)
	if err == nil {
		c.Close()
		return e
	}
	return proxyEntry{}
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeService applies sp; cur is what the service has now (nil = unknown),
// used to skip calls that would change nothing.
func writeService(sp serviceProxy, cur *serviceProxy) error {
	errs := make([]error, len(kinds)+1)
	var wg sync.WaitGroup
	for i, k := range kinds {
		if i >= len(sp.Proxies) {
			break
		}
		e := sp.Proxies[i]
		if cur != nil && i < len(cur.Proxies) && cur.Proxies[i] == e {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Setting the server also switches the proxy on.
			if e.Server != "" && (e.Enabled || cur == nil) {
				port := e.Port
				if _, err := strconv.Atoi(port); err != nil {
					port = "0"
				}
				if _, err := networksetup(k.set, sp.Service, e.Server, port); err != nil {
					errs[i] = err
					return
				}
			}
			if !e.Enabled {
				_, errs[i] = networksetup(k.state, sp.Service, "off")
			}
		}()
	}
	if cur == nil || !sameList(cur.Bypass, sp.Bypass) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bypass := sp.Bypass
			if len(bypass) == 0 {
				bypass = []string{"Empty"} // networksetup's way of clearing the list
			}
			_, errs[len(kinds)] = networksetup(append([]string{"-setproxybypassdomains", sp.Service}, bypass...)...)
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// ---- state shared between setProxy and restoreProxy ----

var (
	proxyMu   sync.Mutex
	saved     []serviceProxy // the user's settings, per service we changed
	ours      serviceProxy   // our setting (Service left empty)
	pending   sync.WaitGroup // background services still being switched
	lockFile  *os.File
	onPrimary func() // set by run(): called once the primary service is restored

	primarySvc string      // the service switched synchronously by setProxy
	aborting   atomic.Bool // restore started: background services not begun yet are skipped
)

func ourSetting(port int) serviceProxy {
	p := strconv.Itoa(port)
	sp := serviceProxy{Proxies: make([]proxyEntry, len(kinds)), Bypass: bypassDomains}
	for i := range sp.Proxies {
		sp.Proxies[i] = proxyEntry{Enabled: true, Server: "127.0.0.1", Port: p}
	}
	return sp
}

// lockCore makes sure only one core changes the proxy at a time: a second
// core would save the first one's setting as "the user's" and restore a dead
// proxy afterwards.
func lockCore() error {
	path := filepath.Join(filepath.Dir(backupPath()), "core.lock")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil // no lock possible: carry on
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return errors.New("另一个环游连接正在运行，请先断开它")
	}
	lockFile = f
	return nil
}

func setProxy(port int) error {
	if err := lockCore(); err != nil {
		return err
	}
	ours = ourSetting(port)
	all, err := services()
	if err != nil {
		return err
	}
	syncAll := primaryService() == "" // unknown network in use: switch all before reporting
	// A backup left by a killed core holds the real user setting: keep it.
	var prev []serviceProxy
	if ok, err := loadBackup(&prev); ok && err == nil {
		saved = prev
	}
	known := map[string]bool{}
	for _, sp := range saved {
		known[sp.Service] = true
	}

	apply := func(svc string) error {
		var cur *serviceProxy
		if !known[svc] {
			sp, err := readService(svc)
			if err != nil {
				return err
			}
			proxyMu.Lock()
			saved = append(saved, sp)
			err = saveBackup(saved)
			proxyMu.Unlock()
			if err != nil {
				return err
			}
			cur = &sp
		}
		sp := ours
		sp.Service = svc
		return writeService(sp, cur)
	}

	// The service in use now, synchronously: the app reports "connected" after it.
	if !syncAll {
		primarySvc = all[0]
	}
	if err := apply(all[0]); err != nil {
		return fmt.Errorf("%w（需要管理员账户）", err)
	}
	if syncAll {
		for _, svc := range all[1:] {
			_ = apply(svc)
		}
		return nil
	}
	// The others one at a time in the background: networksetup writes queue
	// up in configd, and a disconnect must not wait behind a burst of them.
	pending.Add(1)
	go func() {
		defer pending.Done()
		for _, svc := range all[1:] {
			if aborting.Load() {
				return // disconnecting already: leave the rest untouched
			}
			_ = apply(svc) // a service that refuses (e.g. a VPN profile) is skipped
		}
	}()
	return nil
}

func restoreProxy() error {
	aborting.Store(true)
	restoredFirst := ""
	// The service in use first, without waiting for the background ones: it
	// was switched synchronously, so its saved setting is complete.
	if primarySvc != "" && ours.Proxies != nil {
		proxyMu.Lock()
		var first *serviceProxy
		for i := range saved {
			if saved[i].Service == primarySvc {
				sp := saved[i]
				first = &sp
			}
		}
		proxyMu.Unlock()
		if first != nil {
			cur := ours
			cur.Service = primarySvc
			if err := writeService(*first, &cur); err == nil {
				restoredFirst = primarySvc
				callPrimary()
			}
		}
	}
	pending.Wait() // background switching that already began is undone too
	proxyMu.Lock()
	list := saved
	proxyMu.Unlock()
	if list == nil {
		// "huanyou-core restore": a previous core was killed. If a core is
		// still running (its lock is held), its proxy is live: leave it.
		if lockFile == nil {
			if err := lockCore(); err != nil {
				callPrimary()
				return nil
			}
		}
		ok, err := loadBackup(&list)
		if !ok {
			callPrimary()
			return err
		}
		if err != nil {
			// Unreadable backup: switch our proxies off everywhere.
			svcs, lerr := services()
			if lerr != nil {
				callPrimary()
				return lerr
			}
			list = nil
			for _, svc := range svcs {
				list = append(list, serviceProxy{Service: svc, Proxies: make([]proxyEntry, len(kinds))})
			}
		}
	}
	var cur *serviceProxy
	if ours.Proxies != nil {
		cur = &ours
	}
	restore := func(sp serviceProxy) error {
		if sp.Service == "" {
			return nil
		}
		var c *serviceProxy
		if cur != nil {
			x := *cur
			x.Service = sp.Service
			c = &x
		} else if now, err := readService(sp.Service, true); err == nil {
			// Recovery after a killed core: reads are much cheaper than
			// writes, so only what differs is written.
			c = &now
		}
		return writeService(sp, c)
	}
	// Primary service first, then report, then the rest. Unknown primary:
	// report only when everything is restored.
	primary := ""
	if restoredFirst == "" {
		primary = primarySvc
		if primary == "" {
			primary = primaryService()
		}
	}
	var firstErr error
	rest := list[:0:0]
	for _, sp := range list {
		if restoredFirst != "" && sp.Service == restoredFirst {
			continue
		}
		if primary != "" && sp.Service == primary {
			if err := restore(sp); err != nil {
				firstErr = err
			}
			callPrimary()
		} else {
			rest = append(rest, sp)
		}
	}
	errs := make([]error, len(rest))
	var wg sync.WaitGroup
	for i, sp := range rest {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = restore(sp)
		}()
	}
	wg.Wait()
	callPrimary()
	ok := firstErr == nil
	for _, e := range errs {
		if e == nil {
			ok = true
		} else if firstErr == nil {
			firstErr = e
		}
	}
	if !ok {
		return firstErr
	}
	err := removeBackup()
	if lockFile != nil {
		lockFile.Close()
		lockFile = nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func callPrimary() {
	if f := onPrimary; f != nil {
		onPrimary = nil
		f()
	}
}
