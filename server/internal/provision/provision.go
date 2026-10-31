// Package provision installs a vpn-node on a fresh server over SSH, so an
// operator can add a node by typing its SSH address instead of running a
// sequence of shell commands by hand.
//
// Robustness note: some hosts (e.g. with a BT/宝塔 panel) wrap the SSH exec
// shell and mangle compound command lines (anything with && / $() may silently
// produce no output). So we never send compound commands: payload files and a
// self-contained install script are uploaded with single `cat > file`
// redirects, and the script is run by path (`bash <stage>/x.sh`). All the shell
// logic lives inside the uploaded file, where it runs normally.
package provision

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Target is how to reach the server over SSH.
type Target struct {
	Host       string
	Port       int    // default 22
	User       string // default root
	Password   string
	PrivateKey string // PEM (optional)
	// HostKey is the server's expected SSH host key: "<type> SHA256:…"
	// (e.g. "ssh-ed25519 SHA256:…"; the fingerprint as ssh-keygen -lf prints
	// it). When set, a server showing another key is refused: someone in
	// between would get the password and the uploaded secrets. Empty: first
	// contact, any key is accepted and returned so it can be pinned from
	// then on.
	HostKey string
}

// Report is the result of a read-only environment check.
type Report struct {
	Arch        string   `json:"arch"`         // uname -m, e.g. "x86_64"
	OS          string   `json:"os"`           // PRETTY_NAME
	Systemd     bool     `json:"systemd"`      //
	Port        int      `json:"port"`         // the port we checked
	PortFree    bool     `json:"port_free"`    //
	PortUser    string   `json:"port_user"`    // process holding the port, e.g. "nginx"
	SuggestPort int      `json:"suggest_port"` // a free port if the requested one is busy
	RAMMB       int      `json:"ram_mb"`       //
	DiskFreeGB  int      `json:"disk_free_gb"` //
	TimeSynced  bool     `json:"time_synced"`  //
	PkgMgr      string   `json:"pkg_mgr"`      // apt | yum | none
	Virt        string   `json:"virt"`         // systemd-detect-virt
	ArchOK      bool     `json:"arch_ok"`      // a matching node binary is available
	Fatal       []string `json:"fatal"`        // blocking problems
	Warn        []string `json:"warn"`         // non-blocking warnings
	Log         string   `json:"log"`          // raw check output
	HostKey     string   `json:"host_key"`     // SSH host key fingerprint the server showed
}

// OK reports whether installation can proceed.
func (r *Report) OK() bool { return len(r.Fatal) == 0 }

// Install is everything needed to lay down a working node.
type Install struct {
	NodeJSON    []byte            // /etc/vpn/node.json
	ServiceUnit []byte            // systemd unit
	Binaries    map[string][]byte // by `uname -m`, e.g. {"x86_64": …, "aarch64": …}
	Port        int               // public port, opened in the firewall
	Log         io.Writer         // optional live transcript
}

// Result reports a completed install.
type Result struct {
	Arch    string `json:"arch"`
	Log     string `json:"log"`
	HostKey string `json:"host_key"` // SSH host key fingerprint, pinned for later upgrades
}

// ErrHostKey: the server showed another SSH host key than the pinned one.
var ErrHostKey = errors.New("SSH 主机密钥与记录的不一致：可能有人在中间拦截，或服务器重装过系统。请在服务商控制台核实后再操作")

func (t Target) addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// dial connects and authenticates; it returns the client and the host key the
// server showed ("<type> SHA256:…"), also with ErrHostKey (to show it), but
// not after other failures. The connection is closed when ctx ends, so no
// SSH call outlives the operation.
func (t Target) dial(ctx context.Context) (*ssh.Client, string, error) {
	user := t.User
	if user == "" {
		user = "root"
	}
	var auth []ssh.AuthMethod
	if strings.TrimSpace(t.PrivateKey) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(t.PrivateKey))
		if err != nil {
			return nil, "", fmt.Errorf("私钥解析失败：%w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		auth = append(auth, ssh.Password(t.Password))
	}
	if len(auth) == 0 {
		return nil, "", fmt.Errorf("必须提供密码或私钥")
	}
	// A pin is "<key type> SHA256:<fingerprint>". The type matters: a server
	// has several host keys and shows the one of the first type the client
	// asks for, so later dials ask for exactly the pinned type.
	wantType, want := "", strings.TrimSpace(t.HostKey)
	if typ, fp, ok := strings.Cut(want, " "); ok {
		wantType, want = typ, strings.TrimSpace(fp)
	}
	var seen string
	cfg := &ssh.ClientConfig{
		User: user, Auth: auth,
		// Runs before any credential is sent.
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			seen = key.Type() + " " + fp
			if want != "" && fp != want {
				return ErrHostKey
			}
			return nil
		},
		HostKeyAlgorithms: hostKeyAlgos(want, wantType),
		Timeout:           12 * time.Second,
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", t.addr())
	if err != nil {
		return nil, "", fmt.Errorf("无法连接 %s：%w", t.addr(), err)
	}
	// ClientConfig.Timeout covers only the TCP connect: a server that accepts
	// and then never answers would hang the handshake forever.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		conn.Close()
		if errors.Is(err, ErrHostKey) {
			return nil, seen, ErrHostKey // the key shown, for an explicit re-trust
		}
		if wantType != "" && strings.Contains(err.Error(), "no common algorithm for host key") {
			return nil, "", fmt.Errorf("服务器不再提供记录的 %s 主机密钥：可能重装过系统或改过 SSH 配置，请核实后重新信任主机密钥", wantType)
		}
		// No key returned: a server that did not let us log in is not
		// necessarily the one we meant (wrong port), so nothing gets pinned.
		return nil, "", fmt.Errorf("SSH 登录失败（请检查账号密码 / 端口）：%w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	context.AfterFunc(ctx, func() { conn.Close() })
	return ssh.NewClient(c, chans, reqs), seen, nil
}

// hostKeyAlgos is the host key algorithm list for a dial. With a pin of a
// known type: only that type, so the server shows the pinned key and not
// another one it also has. Without a pin: Ed25519 first (Go would pick ECDSA
// first), the key operators usually compare (ssh_host_ed25519_key.pub).
// Plain keys only: a host certificate changes at every renewal.
func hostKeyAlgos(pinned, keyType string) []string {
	switch {
	case pinned == "":
		return []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	case keyType == ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	case keyType == ssh.KeyAlgoED25519, keyType == ssh.KeyAlgoECDSA256, keyType == ssh.KeyAlgoECDSA384, keyType == ssh.KeyAlgoECDSA521:
		return []string{keyType}
	}
	return nil // a pin without (or with an unknown) type: Go's defaults
}

// stage creates a private directory for the uploads (node token, REALITY
// key, secrets, database): files in /tmp are readable by every local user,
// and were left there when an install failed. done removes it.
func stage(ctx context.Context, c *ssh.Client) (dir string, done func(), err error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	dir = "/var/tmp/vpn-stage-" + hex.EncodeToString(b[:])
	if out, err := runScript(ctx, c, "mkdir -m 700 "+dir); err != nil {
		return "", nil, fmt.Errorf("无法创建临时目录：%s%w", out, err)
	}
	// Safety net: done can't run once this connection is gone (timeout,
	// closed tab), so a timer removes the directory later in any case.
	_, _ = runScript(ctx, c, "systemd-run --quiet --on-active=6h /bin/rm -rf "+dir)
	return dir, func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = runScript(cctx, c, "rm -rf "+dir)
	}, nil
}

// staged puts the stage directory into a script written with @STAGE@.
func staged(script, dir string) string { return strings.ReplaceAll(script, "@STAGE@", dir) }

// upload writes data to a remote path via a single `cat > path` (no shell
// operators, so command-line mangling on locked-down hosts can't corrupt it).
func upload(ctx context.Context, c *ssh.Client, path string, data []byte) error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run("cat > " + path) }()
	select {
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("上传 %s 失败：%s%w", path, stderr.String(), err)
		}
		return nil
	}
}

// probe runs a trivial command and returns its trimmed output, retrying a few
// times: locked-down SSH shells intermittently swallow command output.
func probe(ctx context.Context, c *ssh.Client, cmd string) string {
	for i := 0; i < 6; i++ {
		out, _ := runScript(ctx, c, cmd)
		if s := strings.TrimSpace(out); s != "" {
			return s
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(400 * time.Millisecond):
		}
	}
	return ""
}

// runScript runs a single command (no operators) and returns combined output.
func runScript(ctx context.Context, c *ssh.Client, cmd string) (string, error) {
	sess, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var buf lockedBuffer // the session may still write while a cancel reads it
	sess.Stdout = &buf
	sess.Stderr = &buf
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		return buf.String(), ctx.Err()
	case err := <-done:
		return buf.String(), err
	}
}

// Check connects and runs a read-only environment probe for the given intended
// port. archAvailable(arch) says whether a node binary exists for that arch.
func Check(ctx context.Context, t Target, port int, archAvailable func(string) bool) (*Report, error) {
	client, hostKey, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if port == 0 {
		port = 443
	}
	dir, done, err := stage(ctx, client)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := upload(ctx, client, dir+"/check.sh", []byte(checkScript)); err != nil {
		return nil, err
	}
	out, _ := runScript(ctx, client, fmt.Sprintf("bash %s/check.sh %d", dir, port))
	r := parseReport(out)
	r.HostKey = hostKey
	r.Port = port
	r.Log = out
	r.ArchOK = r.Arch != "" && archAvailable(r.Arch)

	if r.Arch == "" {
		r.Fatal = append(r.Fatal, "无法识别服务器架构（SSH 可能被面板限制）")
	} else if !r.ArchOK {
		r.Fatal = append(r.Fatal, fmt.Sprintf("没有适用于 %s 架构的 vpn-node 二进制，请在 api.json 的 provision 配置对应路径", r.Arch))
	}
	if !r.Systemd {
		r.Fatal = append(r.Fatal, "未检测到 systemd，无法安装为系统服务")
	}
	if !r.PortFree {
		who := r.PortUser
		if who == "" {
			who = "其他程序"
		}
		msg := fmt.Sprintf("端口 %d 已被 %s 占用", port, who)
		if r.SuggestPort > 0 {
			msg += fmt.Sprintf("，建议改用空闲端口 %d", r.SuggestPort)
		}
		r.Fatal = append(r.Fatal, msg)
	}
	if r.DiskFreeGB > 0 && r.DiskFreeGB < 1 {
		r.Fatal = append(r.Fatal, "磁盘可用空间不足 1 GB")
	}
	if r.RAMMB > 0 && r.RAMMB < 384 {
		r.Warn = append(r.Warn, fmt.Sprintf("内存较小（%d MB），高并发时可能吃紧", r.RAMMB))
	}
	if !r.TimeSynced {
		r.Warn = append(r.Warn, "系统时间未同步，REALITY 对时间敏感，建议开启 NTP（安装时会尝试自动开启）")
	}
	if r.Virt == "openvz" || r.Virt == "lxc" {
		r.Warn = append(r.Warn, "容器化 VPS（"+r.Virt+"）：通常可用，但部分商家限制较多")
	}
	return r, nil
}

// Run installs the node: uploads the binary, config, unit and an install
// script, then runs the script. The script creates the service user, places
// files, opens the firewall, enables NTP and starts the service.
func Run(ctx context.Context, t Target, in Install) (*Result, error) {
	var buf bytes.Buffer
	var log io.Writer = &buf
	if in.Log != nil {
		log = io.MultiWriter(&buf, in.Log)
	}
	var hostKey string
	result := func() *Result { return &Result{Log: buf.String(), HostKey: hostKey} }

	client, hostKey, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	dir, done, err := stage(ctx, client)
	if err != nil {
		return nil, err
	}
	defer done()
	fmt.Fprintf(log, "已连接 %s\n", t.addr())

	arch := probe(ctx, client, "uname -m")
	if arch == "" {
		return result(), fmt.Errorf("无法检测服务器架构（SSH 输出被面板拦截，请重试）")
	}
	bin := in.Binaries[arch]
	if bin == nil {
		return result(), fmt.Errorf("没有适用于 %s 架构的 vpn-node 二进制", arch)
	}
	fmt.Fprintf(log, "架构 %s，上传程序与配置…\n", arch)

	port := in.Port
	if port == 0 {
		port = 443
	}
	files := []struct {
		path string
		data []byte
	}{
		{dir + "/vpn-node.bin", bin},
		{dir + "/vpn-node.json", in.NodeJSON},
		{dir + "/vpn-node.service", in.ServiceUnit},
		{dir + "/install.sh", []byte(staged(installScript(port), dir))},
	}
	for _, f := range files {
		if err := upload(ctx, client, f.path, f.data); err != nil {
			return result(), err
		}
	}
	fmt.Fprintf(log, "上传完成（%s），开始安装…\n", humanSize(len(bin)))

	out, runErr := runScript(ctx, client, "bash "+dir+"/install.sh")
	io.WriteString(log, out)
	// The script ran and failed (exit status): that is a failure even if an
	// older vpn-node from an earlier install is still active.
	var exit *ssh.ExitError
	if errors.As(runErr, &exit) && exit.ExitStatus() != 0 {
		jl, _ := runScript(ctx, client, "journalctl -u vpn-node -n 30 --no-pager")
		io.WriteString(log, jl)
		return result(), fmt.Errorf("安装脚本执行失败（退出码 %d）", exit.ExitStatus())
	}

	// Confirm success by polling the service state rather than trusting the
	// installer's stdout: some locked-down SSH shells swallow the output of
	// longer commands, so `::installed` may never arrive even on success.
	fmt.Fprint(log, "\n确认服务状态…\n")
	active := strings.Contains(out, "::installed")
	for i := 0; i < 10 && !active; i++ {
		select {
		case <-ctx.Done():
			return result(), ctx.Err()
		case <-time.After(time.Second):
		}
		st, _ := runScript(ctx, client, "systemctl is-active vpn-node")
		switch strings.TrimSpace(st) {
		case "active":
			active = true
		case "failed":
			i = 10 // give up early
		}
	}
	if !active {
		jl, _ := runScript(ctx, client, "journalctl -u vpn-node -n 30 --no-pager")
		io.WriteString(log, jl)
		if runErr != nil {
			return result(), fmt.Errorf("安装脚本执行失败：%w", runErr)
		}
		return result(), fmt.Errorf("vpn-node 未能保持运行，请查看日志排查")
	}
	fmt.Fprint(log, "vpn-node 已启动\n")
	return &Result{Arch: arch, Log: buf.String(), HostKey: hostKey}, nil
}

func parseReport(out string) *Report {
	r := &Report{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch k {
		case "ARCH":
			r.Arch = v
		case "OS":
			r.OS = v
		case "SYSTEMD":
			r.Systemd = v == "1"
		case "PORT_FREE":
			r.PortFree = v == "1"
		case "PORT_USER":
			r.PortUser = v
		case "SUGGEST_PORT":
			r.SuggestPort, _ = strconv.Atoi(v)
		case "RAM_MB":
			r.RAMMB, _ = strconv.Atoi(v)
		case "DISK_GB":
			r.DiskFreeGB, _ = strconv.Atoi(v)
		case "TIME_SYNCED":
			r.TimeSynced = v == "1"
		case "PKG":
			r.PkgMgr = v
		case "VIRT":
			r.Virt = v
		}
	}
	return r
}

func humanSize(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n/1024)
}

// checkScript emits KEY=VALUE lines. $1 is the intended port.
const checkScript = `#!/bin/bash
PORT="${1:-443}"
echo "ARCH=$(uname -m)"
. /etc/os-release 2>/dev/null; echo "OS=${PRETTY_NAME:-unknown}"
command -v systemctl >/dev/null 2>&1 && echo "SYSTEMD=1" || echo "SYSTEMD=0"
listening() { ss -ltn 2>/dev/null | grep -q ":$1 " || netstat -ltn 2>/dev/null | grep -q ":$1 "; }
if listening "$PORT"; then
  echo "PORT_FREE=0"
  U=$(ss -ltnp 2>/dev/null | grep ":$PORT " | grep -o '"[^"]*"' | head -1 | tr -d '"')
  echo "PORT_USER=${U:-unknown}"
  for p in 8443 2096 2053 2087 8843 9443 10443; do
    listening "$p" || { echo "SUGGEST_PORT=$p"; break; }
  done
else
  echo "PORT_FREE=1"
fi
echo "RAM_MB=$(free -m 2>/dev/null | awk '/Mem:/{print $2}')"
echo "DISK_GB=$(df -BG / 2>/dev/null | awk 'NR==2{gsub(/G/,"",$4);print $4}')"
if timedatectl 2>/dev/null | grep -qi 'synchronized: yes'; then echo "TIME_SYNCED=1"; else echo "TIME_SYNCED=0"; fi
if command -v apt-get >/dev/null 2>&1; then echo "PKG=apt"; elif command -v yum >/dev/null 2>&1; then echo "PKG=yum"; elif command -v dnf >/dev/null 2>&1; then echo "PKG=dnf"; else echo "PKG=none"; fi
echo "VIRT=$(systemd-detect-virt 2>/dev/null || echo unknown)"
echo "::done"
`

// tuneScript applies kernel settings for many concurrent proxied connections:
// BBR + fq (better throughput on lossy cross-border links), larger socket
// buffers, bigger accept / SYN queues, more file descriptors and ports,
// faster recycling of closed connections. Idempotent.
const tuneScript = `
echo "::step kernel tuning"
cat > /etc/sysctl.d/99-vpn-node.conf <<'SYSCTL'
# vpn-node: high-concurrency proxy tuning (managed by the panel)
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 65536
net.ipv4.tcp_max_syn_backlog = 65536
net.core.rmem_max = 33554432
net.core.wmem_max = 33554432
net.ipv4.tcp_rmem = 4096 87380 33554432
net.ipv4.tcp_wmem = 4096 65536 33554432
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.tcp_fin_timeout = 15
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_keepalive_time = 600
net.ipv4.tcp_keepalive_intvl = 30
net.ipv4.tcp_keepalive_probes = 5
net.ipv4.ip_local_port_range = 10240 65535
net.ipv4.tcp_max_tw_buckets = 262144
fs.file-max = 2097152
fs.nr_open = 2097152
SYSCTL
modprobe tcp_bbr 2>/dev/null || true
sysctl --system >/dev/null 2>&1 || sysctl -p /etc/sysctl.d/99-vpn-node.conf >/dev/null 2>&1 || true
echo "congestion=$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)"
`

// installScript places files and starts the service. port is baked in.
func installScript(port int) string {
	return fmt.Sprintf(`#!/bin/bash
set -e
PORT=%d
echo "::step user+dirs"
id vpn >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin vpn
mkdir -p /etc/vpn
echo "::step place files"
install -m 0755 -o root -g root @STAGE@/vpn-node.bin /usr/local/bin/vpn-node
install -m 0640 -o root -g vpn @STAGE@/vpn-node.json /etc/vpn/node.json
install -m 0644 -o root -g root @STAGE@/vpn-node.service /etc/systemd/system/vpn-node.service
`+tuneScript+`
echo "::step time sync"
timedatectl set-ntp true 2>/dev/null || systemctl enable --now systemd-timesyncd 2>/dev/null || true
echo "::step firewall"
if command -v ufw >/dev/null 2>&1; then ufw allow "$PORT"/tcp >/dev/null 2>&1 || true; fi
if command -v firewall-cmd >/dev/null 2>&1; then firewall-cmd --permanent --add-port="$PORT"/tcp >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1 || true; fi
echo "::step start"
systemctl daemon-reload
systemctl enable vpn-node >/dev/null 2>&1 || true
systemctl restart vpn-node
sleep 2
if ! systemctl is-active --quiet vpn-node; then
  echo "::fail service not active"
  journalctl -u vpn-node -n 30 --no-pager 2>/dev/null || true
  exit 1
fi
rm -rf @STAGE@
echo "::installed"
`, port)
}

// upgradeScript swaps the binary, restarts, checks and rolls back. Exit
// status: 0 upgraded, 3 rolled back, 4 not running and nothing to roll back
// to, anything else failed before the binary was replaced.
const upgradeScript = `#!/bin/bash
# Runs to the end even if the SSH connection drops: once the binary is
# replaced, the check and the rollback must happen.
trap '' HUP PIPE
set -e
test -f /etc/vpn/node.json || { echo "::fail no /etc/vpn/node.json"; exit 1; }
# Older installs: give the node a state directory for its user-list cache.
if ! grep -q StateDirectory /etc/systemd/system/vpn-node.service 2>/dev/null; then
  mkdir -p /etc/systemd/system/vpn-node.service.d
  printf '[Service]\nStateDirectory=vpn-node\n' > /etc/systemd/system/vpn-node.service.d/state.conf
  systemctl daemon-reload
fi
` + tuneScript + `
set +e
nrestarts() { systemctl show -p NRestarts vpn-node 2>/dev/null | cut -d= -f2; }
mainpid() { systemctl show -p MainPID vpn-node 2>/dev/null | cut -d= -f2; }
# The running binary becomes the fallback only if it runs well: active, and
# not restarted by systemd since it was started (or up for 10 minutes
# anyway). A crash-looping one must not replace the last good fallback.
n=$(nrestarts)
up=$(ps -o etimes= -p "$(mainpid)" 2>/dev/null | tr -d ' ')
if [ ! -f /usr/local/bin/vpn-node.prev ] || { systemctl is-active --quiet vpn-node && { [ "${n:-0}" = 0 ] || [ "${up:-0}" -ge 600 ]; }; }; then
  cp -f /usr/local/bin/vpn-node /usr/local/bin/vpn-node.prev
fi
if ! install -m 0755 -o root -g root @STAGE@/vpn-node.bin /usr/local/bin/vpn-node; then
  echo "::fail install"
  exit 1
fi
systemctl restart vpn-node
# Active right after a restart only means started. A binary that crashes on
# start or at its first sync is restarted by systemd every few seconds and
# can look active at any single moment: it has to stay up, as the same
# process, for the whole wait.
ok=1
sleep 3
pid=$(mainpid)
for i in $(seq 1 12); do
  sleep 1
  if ! systemctl is-active --quiet vpn-node || [ "${pid:-0}" = 0 ] || [ "$(mainpid)" != "$pid" ]; then
    ok=0
    break
  fi
done
n=$(nrestarts)
[ "${n:-0}" = 0 ] || ok=0
if [ "$ok" = 1 ]; then
  echo "::upgraded"
  exit 0
fi
journalctl -u vpn-node -n 20 --no-pager 2>/dev/null
if [ -f /usr/local/bin/vpn-node.prev ]; then
  cp -f /usr/local/bin/vpn-node.prev /usr/local/bin/vpn-node
  systemctl restart vpn-node
  echo "::rolledback"
  exit 3
fi
exit 4
`

// Upgrade replaces the vpn-node binary on an installed node and restarts it,
// keeping its config. Used to roll out fixes (e.g. the no-access-log change).
// The second result is the host key the server showed: set when the login
// succeeded (to pin it) or when the key did not match (to show it).
func Upgrade(ctx context.Context, t Target, binaries map[string][]byte, log io.Writer) (string, string, error) {
	client, hostKey, err := t.dial(ctx)
	if err != nil {
		return "", hostKey, err
	}
	defer client.Close()
	dir, done, err := stage(ctx, client)
	if err != nil {
		return "", hostKey, err
	}
	defer done()
	arch := probe(ctx, client, "uname -m")
	bin := binaries[arch]
	if bin == nil {
		return arch, hostKey, fmt.Errorf("没有适用于 %q 架构的 vpn-node 程序", arch)
	}
	fmt.Fprintf(log, "架构 %s，上传新程序（%s）…\n", arch, humanSize(len(bin)))
	if err := upload(ctx, client, dir+"/vpn-node.bin", bin); err != nil {
		return arch, hostKey, err
	}
	if err := upload(ctx, client, dir+"/upgrade.sh", []byte(staged(upgradeScript, dir))); err != nil {
		return arch, hostKey, err
	}
	out, runErr := runScript(ctx, client, "bash "+dir+"/upgrade.sh")
	io.WriteString(log, out)
	// The exit status decides (some panel shells swallow the output).
	var exit *ssh.ExitError
	switch {
	case errors.As(runErr, &exit):
		switch exit.ExitStatus() {
		case 3:
			return arch, hostKey, fmt.Errorf("新程序启动失败，已自动恢复旧版本，节点继续运行")
		case 4:
			return arch, hostKey, fmt.Errorf("新程序启动失败，且没有可恢复的旧程序")
		default:
			return arch, hostKey, fmt.Errorf("升级脚本执行失败（退出码 %d），节点仍在运行旧程序", exit.ExitStatus())
		}
	case runErr != nil:
		// No exit status: the connection broke. The script keeps going on
		// the node (check and rollback included); its outcome is unknown here.
		return arch, hostKey, fmt.Errorf("升级过程中与服务器的连接中断（%v），请稍后查看节点状态", runErr)
	}
	if strings.Contains(out, "::rolledback") {
		return arch, hostKey, fmt.Errorf("新程序启动失败，已自动恢复旧版本，节点继续运行")
	}
	for i := 0; i < 10; i++ {
		if probe(ctx, client, "systemctl is-active vpn-node") == "active" {
			fmt.Fprint(log, "vpn-node 已重启并运行\n")
			return arch, hostKey, nil
		}
		time.Sleep(time.Second)
	}
	jl, _ := runScript(ctx, client, "journalctl -u vpn-node -n 20 --no-pager")
	io.WriteString(log, jl)
	return arch, hostKey, fmt.Errorf("升级后 vpn-node 未能运行（已保留旧程序 /usr/local/bin/vpn-node.prev）")
}

// CloneInstall is a complete control-plane deployment for a new server.
type CloneInstall struct {
	Domain       string
	APIBinary    []byte            // linux/amd64 vpn-api (the running binary)
	NodeBinaries map[string][]byte // optional: so the clone can one-click install nodes
	APIConfig    []byte            // /etc/vpn/api.json
	APIEnv       []byte            // /etc/vpn/api.env (secrets)
	Database     []byte            // /var/lib/vpn/vpn.db
	// Migrate: the domain still points at the current server and is switched
	// to the target afterwards, so DNS and HTTPS are not checked here; the
	// certificate is issued on the first request after the switch.
	Migrate bool
	Log     io.Writer
}

type CloneResult struct {
	TLSReady bool   `json:"tls_ready"`
	TargetIP string `json:"target_ip"`
	HostKey  string `json:"host_key"`
}

const apiUnit = `[Unit]
Description=VPN control-plane API
After=network-online.target
Wants=network-online.target

[Service]
User=vpn
Group=vpn
EnvironmentFile=/etc/vpn/api.env
ExecStart=/usr/local/bin/vpn-api -config /etc/vpn/api.json
Restart=always
RestartSec=3
TimeoutStopSec=30
StateDirectory=vpn
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/vpn

[Install]
WantedBy=multi-user.target
`

// Clone checks the target (arch, systemd, free 80/443, DNS → this IP), then
// installs vpn-api with the given config, secrets and database and waits for
// HTTPS to come up with a Let's Encrypt certificate.
func Clone(ctx context.Context, t Target, in CloneInstall) (*CloneResult, error) {
	var log io.Writer = io.Discard
	if in.Log != nil {
		log = in.Log
	}
	client, hostKey, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	fmt.Fprintf(log, "已连接 %s，SSH 主机密钥 %s（可在服务商控制台核对）\n", t.addr(), hostKey)
	dir, done, err := stage(ctx, client)
	if err != nil {
		return nil, err
	}
	defer done()
	// --- checks ---
	if err := upload(ctx, client, dir+"/check.sh", []byte(checkScript)); err != nil {
		return nil, err
	}
	out, _ := runScript(ctx, client, "bash "+dir+"/check.sh 443")
	r := parseReport(out)
	out80, _ := runScript(ctx, client, "bash "+dir+"/check.sh 80")
	r80 := parseReport(out80)
	// A VPN node of ours on :443 can stay: it moves to a local port and the
	// API routes :443 by SNI (its own domain to itself, the rest to the node).
	var nodeCfg []byte // rewritten /etc/vpn/node.json, nil = no co-located node
	nodePort := 0
	if !r.PortFree && r.PortUser == "vpn-node" {
		raw, _ := runScript(ctx, client, "cat /etc/vpn/node.json")
		var nc map[string]any
		if err := json.Unmarshal([]byte(raw), &nc); err != nil {
			return nil, fmt.Errorf("443 端口被本系统节点 vpn-node 占用，但读取 /etc/vpn/node.json 失败：%v", err)
		}
		nodePort = 4443
		if o, _ := runScript(ctx, client, "bash "+dir+"/check.sh 4443"); !parseReport(o).PortFree {
			nodePort = parseReport(o).SuggestPort
		}
		if nodePort == 0 {
			return nil, fmt.Errorf("没有找到可供节点使用的本地端口")
		}
		nc["listen"] = "127.0.0.1"
		nc["vision_port"] = nodePort
		nc["accept_proxy_protocol"] = true
		nodeCfg, _ = json.MarshalIndent(nc, "", "  ")
		var ac map[string]any
		if err := json.Unmarshal(in.APIConfig, &ac); err != nil {
			return nil, fmt.Errorf("api config: %w", err)
		}
		tlsCfg, _ := ac["tls"].(map[string]any)
		if tlsCfg == nil {
			tlsCfg = map[string]any{}
		}
		tlsCfg["passthrough"] = fmt.Sprintf("127.0.0.1:%d", nodePort)
		ac["tls"] = tlsCfg
		in.APIConfig, _ = json.MarshalIndent(ac, "", "  ")
		r.PortFree = true
	}
	fmt.Fprintf(log, "系统：%s · %s · 内存 %d MB · 磁盘空闲 %d GB\n", r.OS, r.Arch, r.RAMMB, r.DiskFreeGB)
	if nodeCfg != nil {
		fmt.Fprintf(log, "这台服务器上运行着节点（vpn-node 占用 443）：节点改为监听 127.0.0.1:%d，由 vpn-api 按域名分流 443，节点继续可用（切换时中断约几秒）\n", nodePort)
	}
	switch {
	case r.Arch != "x86_64":
		return nil, fmt.Errorf("目标服务器架构为 %q，目前克隆只支持 x86_64", r.Arch)
	case !r.Systemd:
		return nil, fmt.Errorf("目标服务器没有 systemd")
	case !r.PortFree:
		return nil, fmt.Errorf("目标服务器 443 端口被 %s 占用，克隆需要独占 80/443（请换一台干净的服务器，或先停掉该程序）", r.PortUser)
	}
	if !r80.PortFree {
		// Certificates also come through TLS-ALPN-01 on :443, so :80 is optional.
		fmt.Fprintf(log, "80 端口被 %s 占用：不影响，证书改用 443 端口验证（TLS-ALPN），只是 http:// 不会自动跳转到 https://\n", r80.PortUser)
	}
	// DNS must already point at the target, or Let's Encrypt can't issue.
	ips, _ := net.DefaultResolver.LookupHost(ctx, in.Domain)
	targetIP := t.Host
	if net.ParseIP(targetIP) == nil {
		if a, err := net.DefaultResolver.LookupHost(ctx, targetIP); err == nil && len(a) > 0 {
			targetIP = a[0]
		}
	}
	pointed := false
	for _, ip := range ips {
		if ip == targetIP {
			pointed = true
		}
	}
	if in.Migrate {
		if pointed {
			fmt.Fprintf(log, "服务迁移：域名 %s 已指向 %s ✓，80/443 空闲 ✓\n", in.Domain, targetIP)
		} else {
			fmt.Fprintf(log, "服务迁移：域名 %s 当前解析到 %v，部署完成后再改到 %s；80/443 空闲 ✓\n", in.Domain, ips, targetIP)
		}
	} else if !pointed {
		return nil, fmt.Errorf("域名 %s 当前解析到 %v，没有指向目标服务器 %s。请先在 DNS 添加 A 记录（不要开 CDN 代理），生效后再克隆", in.Domain, ips, targetIP)
	}
	fmt.Fprintf(log, "域名 %s → %s ✓，80/443 空闲 ✓\n", in.Domain, targetIP)

	// --- upload ---
	files := []struct {
		path string
		data []byte
	}{
		{dir + "/vpn-api.bin", in.APIBinary},
		{dir + "/vpn-api.json", in.APIConfig},
		{dir + "/vpn-api.env", in.APIEnv},
		{dir + "/vpn-api.db", in.Database},
		{dir + "/vpn-api.service", []byte(apiUnit)},
	}
	if nodeCfg != nil {
		files = append(files, struct {
			path string
			data []byte
		}{dir + "/vpn-node.json", nodeCfg})
	}
	if b := in.NodeBinaries["x86_64"]; b != nil {
		files = append(files, struct {
			path string
			data []byte
		}{dir + "/vpn-node.bin", b})
	}
	total := 0
	for _, f := range files {
		total += len(f.data)
	}
	fmt.Fprintf(log, "上传程序和数据（%s）…\n", humanSize(total))
	for _, f := range files {
		if err := upload(ctx, client, f.path, f.data); err != nil {
			return nil, err
		}
	}
	script := `#!/bin/bash
set -e
echo "::step user+dirs"
id vpn >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin vpn
mkdir -p /etc/vpn /var/lib/vpn/dl/releases /var/lib/vpn/acme
echo "::step place files"
install -m 0755 -o root -g root @STAGE@/vpn-api.bin /usr/local/bin/vpn-api
if [ -f @STAGE@/vpn-node.bin ]; then install -m 0755 -o root -g root @STAGE@/vpn-node.bin /usr/local/bin/vpn-node; fi
install -m 0640 -o root -g vpn @STAGE@/vpn-api.json /etc/vpn/api.json
install -m 0640 -o root -g vpn @STAGE@/vpn-api.env /etc/vpn/api.env
if [ -f /var/lib/vpn/vpn.db ]; then mv /var/lib/vpn/vpn.db /var/lib/vpn/vpn.db.bak-pre-clone-$(date +%s); fi
rm -f /var/lib/vpn/vpn.db-wal /var/lib/vpn/vpn.db-shm
install -m 0600 -o vpn -g vpn @STAGE@/vpn-api.db /var/lib/vpn/vpn.db
chown -R vpn:vpn /var/lib/vpn
install -m 0644 -o root -g root @STAGE@/vpn-api.service /etc/systemd/system/vpn-api.service
rm -f @STAGE@/vpn-api.bin @STAGE@/vpn-node.bin @STAGE@/vpn-api.json @STAGE@/vpn-api.env @STAGE@/vpn-api.db
echo "::step firewall"
if command -v ufw >/dev/null 2>&1; then ufw allow 80/tcp >/dev/null 2>&1 || true; ufw allow 443/tcp >/dev/null 2>&1 || true; fi
if command -v firewall-cmd >/dev/null 2>&1; then firewall-cmd --permanent --add-service=http --add-service=https >/dev/null 2>&1 && firewall-cmd --reload >/dev/null 2>&1 || true; fi
` + tuneScript + `
echo "::step start"
systemctl daemon-reload
systemctl enable vpn-api >/dev/null 2>&1 || true
if [ -f @STAGE@/vpn-node.json ]; then
  echo "::step move node behind the API"
  cp -a /etc/vpn/node.json /etc/vpn/node.json.bak-pre-clone
  cat @STAGE@/vpn-node.json > /etc/vpn/node.json   # keeps owner and mode
  systemctl restart vpn-node
  for i in $(seq 1 20); do ss -ltnH 'sport = :443' | grep -q . || break; sleep 0.5; done
fi
systemctl restart vpn-api
echo "::cloned"
`
	if err := upload(ctx, client, dir+"/clone.sh", []byte(staged(script, dir))); err != nil {
		return nil, err
	}
	out, _ = runScript(ctx, client, "bash "+dir+"/clone.sh")
	io.WriteString(log, out)

	ok := false
	for i := 0; i < 10 && !ok; i++ {
		ok = probe(ctx, client, "systemctl is-active vpn-api") == "active"
		if !ok {
			time.Sleep(time.Second)
		}
	}
	if !ok {
		jl, _ := runScript(ctx, client, "journalctl -u vpn-api -n 30 --no-pager")
		io.WriteString(log, jl)
		if nodeCfg != nil {
			// Give :443 back to the node so its users keep working.
			runScript(ctx, client, "systemctl stop vpn-api; systemctl disable vpn-api >/dev/null 2>&1; [ -f /etc/vpn/node.json.bak-pre-clone ] && cp -a /etc/vpn/node.json.bak-pre-clone /etc/vpn/node.json; systemctl restart vpn-node")
			fmt.Fprint(log, "已恢复节点原来的配置（重新独占 443）。\n")
		}
		return nil, fmt.Errorf("新系统的 vpn-api 没有运行起来，请查看上面的日志")
	}
	if nodeCfg != nil {
		if probe(ctx, client, "systemctl is-active vpn-node") != "active" {
			jl, _ := runScript(ctx, client, "journalctl -u vpn-node -n 20 --no-pager")
			io.WriteString(log, jl)
			fmt.Fprint(log, "⚠ 节点 vpn-node 没有运行，请在新后台「节点」页检查。\n")
		} else {
			fmt.Fprintf(log, "节点已改为 127.0.0.1:%d，经 443 分流继续可用 ✓\n", nodePort)
		}
	}
	if in.Migrate && !pointed {
		// The domain still reaches the old server; check the new one locally.
		// vpn-api must be the one listening on :443 (:80 may belong to another program).
		code := ""
		for i := 0; i < 10 && code == ""; i++ {
			if strings.Contains(probe(ctx, client, "ss -ltnpH 'sport = :443' || true"), "vpn-api") {
				code = "vpn-api 已监听 443"
			} else {
				time.Sleep(time.Second)
			}
		}
		if code == "" {
			jl, _ := runScript(ctx, client, "journalctl -u vpn-api -n 30 --no-pager")
			io.WriteString(log, jl)
			return nil, fmt.Errorf("新服务器上的 vpn-api 没有响应，请查看上面的日志")
		}
		fmt.Fprintf(log, "新服务器已就绪（%s）。请把 %s 的 A 记录改为 %s，生效后首次访问会自动申请 HTTPS 证书。\n", code, in.Domain, targetIP)
		return &CloneResult{TLSReady: false, TargetIP: targetIP, HostKey: hostKey}, nil
	}
	fmt.Fprint(log, "vpn-api 已启动，正在申请 HTTPS 证书…\n")

	// --- wait for HTTPS (the first request triggers certificate issuance) ---
	hc := &http.Client{Timeout: 20 * time.Second}
	for i := 0; i < 12; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+in.Domain+"/healthz", nil)
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Fprintf(log, "HTTPS 正常：https://%s ✓\n", in.Domain)
				return &CloneResult{TLSReady: true, TargetIP: targetIP, HostKey: hostKey}, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	fmt.Fprint(log, "证书暂未就绪（可能 DNS 仍在生效），服务已运行，稍后访问即会自动申请。\n")
	return &CloneResult{TLSReady: false, TargetIP: targetIP, HostKey: hostKey}, nil
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
