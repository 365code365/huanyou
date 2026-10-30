// Package provision installs a vpn-node on a fresh server over SSH, so an
// operator can add a node by typing its SSH address instead of running a
// sequence of shell commands by hand.
//
// Robustness note: some hosts (e.g. with a BT/宝塔 panel) wrap the SSH exec
// shell and mangle compound command lines (anything with && / $() may silently
// produce no output). So we never send compound commands: payload files and a
// self-contained install script are uploaded with single `cat > file`
// redirects, and the script is run by path (`bash /tmp/x.sh`). All the shell
// logic lives inside the uploaded file, where it runs normally.
package provision

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
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
	Arch string `json:"arch"`
	Log  string `json:"log"`
}

func (t Target) addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

func (t Target) dial(ctx context.Context) (*ssh.Client, error) {
	user := t.User
	if user == "" {
		user = "root"
	}
	var auth []ssh.AuthMethod
	if strings.TrimSpace(t.PrivateKey) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(t.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("私钥解析失败：%w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		auth = append(auth, ssh.Password(t.Password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("必须提供密码或私钥")
	}
	cfg := &ssh.ClientConfig{
		User: user, Auth: auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // first-contact install
		Timeout:         12 * time.Second,
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", t.addr())
	if err != nil {
		return nil, fmt.Errorf("无法连接 %s：%w", t.addr(), err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH 登录失败（请检查账号密码 / 端口）：%w", err)
	}
	return ssh.NewClient(c, chans, reqs), nil
}

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
	var buf bytes.Buffer
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
	client, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if port == 0 {
		port = 443
	}
	if err := upload(ctx, client, "/tmp/vpn-check.sh", []byte(checkScript)); err != nil {
		return nil, err
	}
	out, _ := runScript(ctx, client, fmt.Sprintf("bash /tmp/vpn-check.sh %d", port))
	runScript(ctx, client, "rm -f /tmp/vpn-check.sh")
	r := parseReport(out)
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
	result := func() *Result { return &Result{Log: buf.String()} }

	client, err := t.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Close()
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
		{"/tmp/vpn-node.bin", bin},
		{"/tmp/vpn-node.json", in.NodeJSON},
		{"/tmp/vpn-node.service", in.ServiceUnit},
		{"/tmp/vpn-install.sh", []byte(installScript(port))},
	}
	for _, f := range files {
		if err := upload(ctx, client, f.path, f.data); err != nil {
			return result(), err
		}
	}
	fmt.Fprintf(log, "上传完成（%s），开始安装…\n", humanSize(len(bin)))

	out, runErr := runScript(ctx, client, "bash /tmp/vpn-install.sh")
	io.WriteString(log, out)

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
	return &Result{Arch: arch, Log: buf.String()}, nil
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

// installScript places files and starts the service. port is baked in.
func installScript(port int) string {
	return fmt.Sprintf(`#!/bin/bash
set -e
PORT=%d
echo "::step user+dirs"
id vpn >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin vpn
mkdir -p /etc/vpn
echo "::step place files"
install -m 0755 -o root -g root /tmp/vpn-node.bin /usr/local/bin/vpn-node
install -m 0640 -o root -g vpn /tmp/vpn-node.json /etc/vpn/node.json
install -m 0644 -o root -g root /tmp/vpn-node.service /etc/systemd/system/vpn-node.service
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
rm -f /tmp/vpn-node.bin /tmp/vpn-node.json /tmp/vpn-node.service /tmp/vpn-install.sh
echo "::installed"
`, port)
}
