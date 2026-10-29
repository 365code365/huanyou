# VPN Platform

用户在 App 里登录后点一下即可连接。服务端用 Go，App 用 Flutter（iOS + Android）。传输层使用 **Xray-core 的 VLESS + XTLS-Vision + REALITY**，并配一条 **XHTTP + REALITY** 备用通道，两条通道共用 443 端口。这是目前在中国大陆抗封锁效果最好的一类方案。

```
┌────────── App (Flutter) ──────────┐        HTTPS         ┌──────── API (Go) ────────┐
│ 登录 / 选节点 / 一键连接            │ ───────────────────▶ │ 账号、JWT、节点列表          │
│ Android: VpnService ─┐            │  GET /api/v1/profile  │ 生成 Xray 客户端配置         │
│ iOS: PacketTunnel ───┤ TUN fd     │ ◀─────────────────── │ SQLite                   │
│        vpncore (Go, Xray-core)    │                       └────────────▲─────────────┘
└──────────────┬────────────────────┘                                    │ 每 30s 拉取用户
               │ VLESS+Vision+REALITY / VLESS+XHTTP+REALITY (TCP 443)    │
               ▼                                                         │
        ┌──────── Node (Go, 内嵌 Xray-core) ────────┐ ────────────────────┘
        │ 443: REALITY → Vision；其它流量回落到 XHTTP  │
        │ 未认证的探测 → 转发到真实网站（如 yahoo.com）   │
        └─────────────────────────────────────────────┘
```

## 抗封锁设计

| 手段 | 作用 |
|---|---|
| **REALITY** | 握手时冒充一个真实的境外网站（SNI、证书链都是对方的）。GFW 主动探测时，服务器把连接转发给那个真实网站，探测方拿到的是真证书、真页面。没有自签证书，也不需要你自己的域名，所以不会出现可识别的 TLS 指纹。 |
| **XTLS-Vision** | 消除"TLS 套 TLS"的长度和时序特征。这是 GFW 识别 trojan、普通 VLESS-TLS 的主要依据。 |
| **uTLS 指纹 `chrome`** | 客户端 ClientHello 与 Chrome 浏览器一致。 |
| **XHTTP 备用通道** | 走 HTTP/2 形态，流量特征和 Vision 不同。客户端用 observatory 每 60 秒探测两条通道，`leastPing` 负载均衡器会自动切到可用的那条。 |
| **只用 443 端口** | Xray 官方提示 REALITY 跑在非 443 端口更容易被封 IP，所以 XHTTP 通过 VLESS fallback 和 Vision 共用 443。 |
| **远程 DNS** | 系统 DNS 被导到 TUN 内的 172.19.0.2，由 Xray 通过代理向 8.8.8.8 / 1.1.1.1 查询，避免 DNS 污染。 |
| **智能分流** | 大陆 IP 直连，其余走代理（App 内可切换"全局代理"）。 |
| **多个 API 地址** | `API_BASE_URLS` 可填多个地址，某个域名被封时 App 自动换下一个。API 不可达时，App 用上次缓存的配置照样能连上。 |

本地端到端验证过的项目：Vision 通道、XHTTP 回落通道、负载均衡、未认证探测返回真实网站证书、停用用户约 5 秒内断开、智能分流的大陆 IP 直连、Android 模拟器上浏览器流量经 TUN 走节点。

## 目录

```
server/            Go：cmd/api（控制面）、cmd/node（代理节点）
mobile/            Go：vpncore，gomobile 编译成 Android AAR / iOS xcframework
app/               Flutter App
  lib/             Dart：登录、主页、API 客户端、VPN 通道
  android/…/kotlin MainActivity（MethodChannel）+ XrayVpnService（VpnService）
  ios/Runner       VpnPlugin.swift（NETunnelProviderManager）
  ios/PacketTunnel PacketTunnelProvider.swift（Network Extension）
```

---

## 1. 部署服务端

需要：一台跑 API 的服务器（可以和节点同机），一台或多台**境外** VPS 作为节点（香港、日本、新加坡、美西等）。

### 1.1 编译

```bash
cd server
make linux              # 输出 bin/linux-amd64/vpn-api 与 vpn-node（静态二进制）
# 或者：docker build -t vpn-server .
```

### 1.2 为每个节点生成密钥

```bash
./bin/node keygen
```

它会输出 `token`、REALITY `private_key` / `public_key` 和 `short_id`。私钥写进节点的 `node.json`，公钥写进 API 的 `api.json`。

### 1.3 选 REALITY 目标网站（重要）

`target` / `server_name` 要选一个**境外、大陆能直连、支持 TLS 1.3 + HTTP/2、不跳转**的网站，最好和你的 VPS 在同一地区或同一机房。示例里用的是 `www.yahoo.com`（本地实测可用）。`www.microsoft.com` 在测试网络下握手失败，所以没有采用。可以用官方 xray 二进制的 `xray tls ping <域名>` 检查候选网站。

### 1.4 API 服务器

```bash
sudo useradd -r -s /usr/sbin/nologin vpn
sudo install -m 755 bin/linux-amd64/vpn-api /usr/local/bin/
sudo mkdir -p /etc/vpn && sudo cp deploy/api.example.json /etc/vpn/api.json   # 然后编辑
echo "VPN_JWT_SECRET=$(openssl rand -base64 48)" | sudo tee /etc/vpn/api.env
sudo chmod 600 /etc/vpn/api.env /etc/vpn/api.json && sudo chown vpn /etc/vpn/api.*
sudo cp deploy/vpn-api.service /etc/systemd/system/ && sudo systemctl enable --now vpn-api
```

API 只监听 `127.0.0.1:8080`，前面必须加 HTTPS 反向代理，例如 Caddy：

```
api.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

> API 域名本身也要能从大陆访问，否则用户没法登录。建议准备 2～3 个域名，放进 `API_BASE_URLS`。

### 1.5 节点

```bash
sudo install -m 755 bin/linux-amd64/vpn-node /usr/local/bin/
sudo cp deploy/node.example.json /etc/vpn/node.json   # 填入 keygen 结果、api_url、xhttp_path
sudo chmod 600 /etc/vpn/node.json && sudo chown vpn /etc/vpn/node.json
sudo cp deploy/vpn-node.service /etc/systemd/system/ && sudo systemctl enable --now vpn-node
# 防火墙只开放 TCP 443
```

建议开启 BBR：`echo -e "net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr" | sudo tee /etc/sysctl.d/99-bbr.conf && sudo sysctl --system`

### 1.6 用户管理

```bash
vpn-api -config /etc/vpn/api.json user disable a@b.com        # 停用（节点约 30 秒内踢下线）
vpn-api -config /etc/vpn/api.json user enable  a@b.com
vpn-api -config /etc/vpn/api.json user expire  a@b.com 2026-12-31   # 或 never
vpn-api -config /etc/vpn/api.json user rotate  a@b.com        # 更换该用户的 VLESS UUID
```

注册默认开放（`allow_register`），新用户默认永不过期。

### 1.7 API 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/auth/register` | `{email,password}` → `{token,user}` |
| POST | `/api/v1/auth/login` | 同上，按 IP 限流 |
| GET | `/api/v1/me` | 当前用户 |
| GET | `/api/v1/nodes` | 节点列表 |
| GET | `/api/v1/profile?node=hk1&mode=smart\|global` | Xray 客户端配置（字符串） |
| GET | `/internal/v1/users` | 节点专用（Bearer 节点 token） |

---

## 2. 编译 App

需要：Go（脚本会自动下载 Go 1.26 工具链）、Flutter 3.41+、Android SDK + NDK、Xcode（iOS）。

```bash
# 1) 编译 Go 核心（Xray-core）
cd mobile && ./build.sh            # 或 ./build.sh android / ./build.sh ios

# 2) 下载大陆 IP 库（每次发版前更新一次）
cd ../app && ./scripts/fetch_geo.sh

# 3) 运行 / 打包（API 地址在编译时注入，多个用逗号分隔）
flutter run --dart-define=API_BASE_URLS=https://api.example.com,https://api2.example.com
flutter build appbundle --release --dart-define=API_BASE_URLS=...      # Google Play
flutter build apk --release --split-per-abi --dart-define=API_BASE_URLS=...  # 直接分发
```

> APK 里每个 CPU 架构各带一份约 35 MB 的 Go 库。直接分发时请用 `--split-per-abi`，否则 APK 有 100 MB 以上。

### iOS 额外步骤

PacketTunnel 扩展 target 已经通过 `app/ios/scripts/setup_packet_tunnel.rb` 加进了 Xcode 工程（这个脚本可以重复运行）。上架或真机运行前还需要：

1. 一个**付费** Apple Developer 账号。Network Extension 不能用免费账号签名，也不能在模拟器里运行。
2. 在 Apple Developer 后台注册两个 App ID：`com.vpnplatform.vpnApp` 和 `com.vpnplatform.vpnApp.PacketTunnel`，都勾选 **Network Extensions** 能力。
3. 在 Xcode 里打开 `ios/Runner.xcworkspace`，给 Runner 和 PacketTunnel 两个 target 都选上你的 Team。如果要改 Bundle ID，两个都要改，扩展的 ID 必须是 `<App ID>.PacketTunnel`。
4. 注意：国区 App Store 不允许上架 VPN 类 App，需要上架到其他地区。

---

## 已知限制 / 后续可做

- 没有支付和套餐系统，账号有效期用 `user expire` 手动设置。
- 没有流量统计和限速。Xray 本身支持 stats API，需要时可以加上。
- 节点 IP 仍可能因为流量过大被封。要有多个节点，被封时更换 IP，再改 `api.json` 里的 `address`（App 每次连接都会拉取最新配置）。
- 在大陆面向公众运营未经许可的 VPN 服务有法律风险，请自行评估。
