// Package provision installs a vpn-node on a fresh server over SSH, so an
// operator can add a node by typing its SSH address instead of running a
// sequence of shell commands by hand.
package provision

import (
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
	Host       string // IP or hostname
	Port       int    // default 22
	User       string // default root
	Password   string // password auth (optional if PrivateKey set)
	PrivateKey string // PEM private key (optional)
}

// NodeInstall is everything needed to lay down a working node.
type Install struct {
	NodeJSON    []byte // contents of /etc/vpn/node.json
	ServiceUnit []byte // contents of the systemd unit
	// Binaries by `uname -m` value, e.g. {"x86_64": <amd64 bytes>, "aarch64": <arm64 bytes>}.
	Binaries   map[string][]byte
	VisionPort int // opened in the firewall
	// Log, if set, receives the transcript as it happens (for live progress).
	Log io.Writer
}

// Result reports what happened, with a transcript for the admin UI.
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

func (t Target) clientConfig() (*ssh.ClientConfig, error) {
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
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // first-contact install; TOFU not meaningful here
		Timeout:         12 * time.Second,
	}, nil
}

// Run connects to the target and installs the node. The whole command
// transcript is captured into Result.Log, success or failure.
func Run(ctx context.Context, t Target, in Install) (*Result, error) {
	cfg, err := t.clientConfig()
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", t.addr())
	if err != nil {
		return nil, fmt.Errorf("无法连接 %s：%w", t.addr(), err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH 登录失败：%w", err)
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()

	var buf bytes.Buffer
	var log io.Writer = &buf
	if in.Log != nil {
		log = io.MultiWriter(&buf, in.Log)
	}
	fmt.Fprintf(log, "已连接 %s\n", t.addr())
	run := func(label, cmd string) (string, error) {
		fmt.Fprintf(log, "\n$ %s\n", label)
		out, err := runCmd(ctx, client, cmd)
		io.WriteString(log, out)
		if err != nil {
			fmt.Fprintf(log, "[错误] %v\n", err)
		}
		return out, err
	}

	// 1. Detect architecture and pick the matching binary.
	arch, err := run("uname -m", "uname -m")
	if err != nil {
		return &Result{Log: buf.String()}, fmt.Errorf("无法检测服务器架构：%w", err)
	}
	arch = strings.TrimSpace(arch)
	bin := in.Binaries[arch]
	if bin == nil {
		return &Result{Arch: arch, Log: buf.String()},
			fmt.Errorf("没有适用于 %s 架构的 vpn-node 二进制，请在 api.json 配置 node_binary_*", arch)
	}

	// 2. Base layout and service user.
	if _, err := run("准备目录与用户",
		"set -e; id vpn >/dev/null 2>&1 || useradd -r -s /usr/sbin/nologin vpn; "+
			"mkdir -p /etc/vpn; systemctl --version >/dev/null"); err != nil {
		return &Result{Arch: arch, Log: buf.String()}, fmt.Errorf("系统环境检查失败：%w", err)
	}

	// 3. Upload the binary, config and unit (piped through the shell to avoid SFTP).
	if err := upload(ctx, client, log, "/usr/local/bin/vpn-node", bin, "0755", "root:root"); err != nil {
		return &Result{Arch: arch, Log: buf.String()}, err
	}
	if err := upload(ctx, client, log, "/etc/vpn/node.json", in.NodeJSON, "0640", "root:vpn"); err != nil {
		return &Result{Arch: arch, Log: buf.String()}, err
	}
	if err := upload(ctx, client, log, "/etc/systemd/system/vpn-node.service", in.ServiceUnit, "0644", "root:root"); err != nil {
		return &Result{Arch: arch, Log: buf.String()}, err
	}

	// 4. Firewall (best effort) and start.
	if in.VisionPort > 0 {
		run("开放防火墙端口",
			fmt.Sprintf("command -v ufw >/dev/null && ufw allow %d/tcp || true; "+
				"command -v firewall-cmd >/dev/null && firewall-cmd --permanent --add-port=%d/tcp && firewall-cmd --reload || true",
				in.VisionPort, in.VisionPort))
	}
	if _, err := run("启动服务",
		"set -e; systemctl daemon-reload; systemctl enable --now vpn-node"); err != nil {
		return &Result{Arch: arch, Log: buf.String()}, fmt.Errorf("启动 vpn-node 失败：%w", err)
	}
	// 5. Confirm it is running.
	status, err := run("检查运行状态", "sleep 1; systemctl is-active vpn-node")
	if err != nil || strings.TrimSpace(status) != "active" {
		return &Result{Arch: arch, Log: buf.String()},
			fmt.Errorf("vpn-node 未能保持运行（状态：%s），请查看日志 journalctl -u vpn-node", strings.TrimSpace(status))
	}
	return &Result{Arch: arch, Log: buf.String()}, nil
}

// upload writes data to path atomically with the given mode and owner.
func upload(ctx context.Context, c *ssh.Client, log io.Writer, path string, data []byte, mode, owner string) error {
	fmt.Fprintf(log, "\n$ 上传 %s (%d 字节)\n", path, len(data))
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	tmp := path + ".upload"
	// `cat > tmp && chmod && chown && mv` — mv is atomic within a filesystem.
	cmd := fmt.Sprintf("cat > %q && chmod %s %q && chown %s %q && mv -f %q %q",
		tmp, mode, tmp, owner, tmp, tmp, path)
	sess.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		sess.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			io.WriteString(log, stderr.String())
			return fmt.Errorf("上传 %s 失败：%w", path, err)
		}
	}
	io.WriteString(log, "ok\n")
	return nil
}

// runCmd runs one command, returns combined stdout+stderr.
func runCmd(ctx context.Context, c *ssh.Client, cmd string) (string, error) {
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
