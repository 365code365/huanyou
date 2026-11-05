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

### 出口指纹（避免下游识别出代理）

节点到目标网站这一段用 Xray 的 freedom 出站，`domainStrategy=AsIs`：

- 节点**原样转发**客户端的 TCP/UDP 负载到客户端本来要访问的目标，不做 DNS 重解析、不改写目标地址。
- 客户端自己的 TLS ClientHello（App/浏览器的真实指纹）**端到端**到达目标站点，中间不经过第二次 TLS。
- 不添加任何 `Via` / `X-Forwarded-For` 之类的代理头。

所以目标网站看到的是一个普通客户端连接，而不是"经过代理"的连接。剩余可被识别的点是出口 IP 属于机房段（任何服务器都如此，换 IP 可缓解）和节点 Linux 的 TCP 栈指纹，这两点无法在协议层面完全消除。

### 连接模式（App 内切换，默认"直连"）

| 模式 | 说明 |
|---|---|
| **直连** | 连单个节点，大陆 IP 直连、其余走代理。延迟最低，默认模式。 |
| **集群** | 把所有可用节点放进一个 `leastPing` 负载均衡器，自动选最快的；某节点挂了自动切换。 |
| **AI 优先** | 只有 AI 服务域名（OpenAI、Claude、Gemini 等，后台 `ai_domains` 可配）走代理，其余全部直连。服务端按可用率 → 负载 → 延迟挑出最稳定的 3 个节点，客户端用 `leastPing` 在其中自动切换；用户手动选节点会切回「直连」。 |

### 运营功能（均在管理后台配置，即改即生效）

- **套餐 / 定价**：后台「套餐」页增删改，可下架（已购用户不受影响），可设免费套餐。
- **节点管理**：后台「节点」页新增节点时自动生成 Token、REALITY 密钥和 XHTTP 路径，「部署配置」直接给出节点服务器用的 `node.json`；可启停、排序、设权重、重置 Token。
- **节点监测与预警**：API 定时 TCP 探测每个节点，并记录节点代理的同步心跳。连续失败超阈值判离线，代理 3 分钟未同步判失联，向 Webhook 推送（通用 / 飞书 / 钉钉 / 企业微信格式）。后台可看近 1 小时探测记录、可用率、预警记录，可「立即检测」「发送测试消息」。App 拉配置时会优先下发在线节点。
- **邀请送时长**：每个用户有唯一邀请码，新用户注册填码后双方各得天数；奖励套餐、天数、每人上限在后台「系统设置」配置。
- **推荐应用**：名称 / 图标 / 分类 / 跳转地址 / 排序 / 上下架都在后台「推荐应用」页配置。
- **AI 模式域名**：后台「系统设置」里维护。
- **节点到期**：新增节点必须填写到期时间（精确到分钟），可填供应商名称和续费地址。7 天内到期的节点在「节点」和「概览」页高亮并提示，监测每天推送一次到期预警。App 端不显示这些信息。
- **广告与启动弹窗**：首页轮播 / 单图 / 文字链接广告和启动弹窗（每次 / 每天 / 仅一次），都在「系统设置 → 广告与弹窗」配置，默认关闭。
- **数据统计与崩溃日志**：App（≥ 1.2.2）上报打开、连接、选节点等行为，后台「数据统计」看 UV、行为次数、版本和平台分布；崩溃按版本 + 类型 + 堆栈归并。用户可在 App「我的 → 设置」关闭崩溃回传。行为保留 180 天，崩溃保留 90 天。
- **支付网关**：后台只填一个「下单链接」，跳转时带上订单号和 HMAC-SHA256 签名；支付中心通过固定的 `/pay/gateway/notify`（通知）和 `/pay/gateway/query`（主动查询）回调，报文用 AES-256-GCM 加密 + HMAC 签名 + 防重放。对接文档在「系统设置 → 支付 → 查看接口文档」（源文件 `docs/payment-gateway.md`）。可一键关闭购买：App 显示「暂不支持购买」，下单接口返回 403。
- **接口防护**：所有接口按 IP 限流（默认每分钟 240 次），短时间内大量 4xx、扫描敏感路径（`.env`、`.php` 等）的 IP 自动封禁 30 分钟；阈值和白名单在「系统设置 → 安全防护」，已封 IP 在「风控 → 自动封禁」查看和解除。后台 `/admin` 不受限流影响。

### 管理后台

`https://<API 域名>/admin/`（Next.js + React + Ant Design，静态导出后嵌入 `vpn-api` 二进制，生产环境不需要 Node）。页面：概览（统计卡片、14 天趋势、节点状态、最近预警）、用户（搜索 / 筛选 / 分页 / 新增 / 详情抽屉：改套餐与到期、重置密码、备注、停用、清流量、换 UUID、删除）、订单（筛选、手动入账）、套餐、节点、监控预警、推荐应用、系统设置。架构和扩展方式见 [admin/README.md](admin/README.md)。

套餐、节点、推荐应用和系统设置存在数据库里（首次启动时从 `api.json` 导入）。之后以后台为准；如果想用配置文件覆盖数据库，执行 `vpn-api -config api.json catalog import` 后重启。

### 数据库（SQLite / MySQL / PostgreSQL）

默认是本机 SQLite 文件（`api.json` 的 `database`）。后台「数据库」页可以把数据同步到 MySQL 8.0.19+ 或 PostgreSQL 12+ 并切换为主库：

1. 先建空库和有建表权限的账号，填连接串，点「测试连接」（会自动建表）。
2. 「一键同步」可以反复执行：第一次全量，之后只同步有变化的行（按 `updated_at`）。每张表都按唯一业务键对齐（用户 `uid`、订单号、`(user_id, day)` 等），不依赖自增 ID；用户 ID 原样复制，因为它就是充值 ID。
3. 「切换为主数据库」：再做一次全量同步，写入 `database.json`（与 SQLite 文件同目录，权限 600），服务自动重启，启动时补齐切换那几秒写入旧库的数据。旧库保持不动，用同样的方法可以切回。
4. 容器部署也可以直接用环境变量 `VPN_DB_DRIVER=mysql|postgres` + `VPN_DB_DSN=...` 指定（此时后台不能切换）。

备份、克隆始终导出为 SQLite 文件；用 MySQL / PostgreSQL 时恢复备份会在重启后自动导入主库。表结构见 `server/internal/store/schema.go`，跨库测试：`VPN_TEST_MYSQL=... VPN_TEST_PG=... go test ./internal/store`。

### 风控与用户分群

- **IP 记录**：App 调用接口的 IP 和节点上 VPN 连接的来源 IP，按「用户 + IP + 来源 + 节点」聚合（次数、首次 / 最近时间），默认保留 30 天。只记录来源地址，不记录访问的网站。IP 类型（国内 / 海外 / 内网）在本机判断；「归属地」只在管理员点击时才查询第三方服务。节点 IP 需要节点升级到本版本。
- **可疑行为**：24 小时内 IP 数超过阈值的账号（共享 / 倒卖）、被多个订阅共用的 IP（批量注册薅奖励）。阈值在「风控 → 设置」。
- **封禁**：封 IP / 网段（App 接口返回 403，节点丢弃该来源的流量，约 30 秒内生效，无需重启）或封用户（无法登录，节点上立即下线，多设备成员随主账号），可设有效天数。
- **用户分群**：价值分层（高价 / 中价 / 低价 / 未付费 / 共享成员，按累计消费）+ 行为标签（优质、潜力、流失风险、已流失、沉睡、新用户、推广达人、风险账号），附转化率、复购率、ARPPU、套餐销售构成和运营建议；可导出 CSV，门槛在「分群规则」里调。

## 当前线上部署

香港节点 **162.211.181.15**（Debian 12），域名 **rely-vpn-go.com**（2026-11-04 从 net-w.51aihelp.com 换过来，旧域名作为别名继续可用，给还没升级的旧版 App 用）。2026-10-30 从 64.83.22.32 用「克隆 → 服务迁移」搬过来，后台和香港节点共用这台服务器：

```
:443  vpn-api（按 TLS SNI 分流，PROXY protocol 传真实 IP）
      ├─ SNI = rely-vpn-go.com / net-w.51aihelp.com → vpn-api 自己的 HTTPS（各自一张 Let's Encrypt 证书，TLS-ALPN 验证，自动续期）
      └─ 其它 SNI（VPN 客户端 / 探测）→ 127.0.0.1:4443 vpn-node（REALITY，伪装 www.apple.com）
:80   本机原有的 nginx（与本系统无关）
```

- API / 管理后台：`https://rely-vpn-go.com/`、`https://rely-vpn-go.com/admin/`（管理员密码在 `/etc/vpn/api.env`）。
- 配置：`/etc/vpn/api.json`（`public_url`、`tls.domain`、`tls.aliases`（旧域名，所有旧版 App 升级后可删）、`tls.passthrough`）；节点 `node.json` 的 `api_url` 也已改成新域名；
- 防探测（`api.json` 的 `tls`）：`cloudflare_only=["rely-vpn-go.com"]`：不是来自 Cloudflare 的连接即使带这个域名的 SNI，也当作未知域名转给节点（看到的是 REALITY 伪装的 www.apple.com），扫描器无法把新域名和本机 IP 对上；本机回环（节点同步）和 ACME 验证除外。`api_only=["net-w.51aihelp.com"]`：旧域名直连本机，只提供 App / 节点 / 支付回调用的接口，官网下载页和管理后台在旧域名上返回 404。
- 证书续期：新域名经 Cloudflare 后 TLS-ALPN 验证到不了本机，改用 http-01：nginx 的 `/www/server/panel/vhost/nginx/vpn-api-acme.conf` 把两个域名的 `/.well-known/acme-challenge/` 转到 `tls.acme_http_listen=127.0.0.1:8081`。Cloudflare 开着「始终使用 HTTPS」时，验证请求会被跳到 443，API 在 443 上也会应答验证路径。
- 模拟支付已关闭（`payments.mock=false`，2026-11-05），接入真实支付前 App 显示「暂不支持购买」。
- `rely-vpn-go.com` 在 Cloudflare 上开了代理（橙色云朵），用户经 Cloudflare 边缘访问 API，旧域名直连本机。因此 `api.json` 开了 `trust_cloudflare`：只对来自 Cloudflare 官方网段的连接读取 `CF-Connecting-IP` 作为真实 IP，否则限流、自动封禁、新设备上限都会打到共享的边缘 IP。网段列表在 `server/internal/api/cloudflare.go`，Cloudflare 公布新网段时要更新。本机 `/etc/hosts` 加了 `127.0.0.1 rely-vpn-go.com`，让同机节点直接连本机 API，不绕 Cloudflare；节点 `node.json` 里 `listen=127.0.0.1`、`vision_port=4443`、`accept_proxy_protocol=true`（原配置备份为 `node.json.bak-pre-clone`）。证书缓存在 `/var/lib/vpn/acme/`。
- 「备份与克隆」页顶部的「当前服务器」卡片显示公网 IP、域名解析是否指向本机、证书剩余天数和本机节点。
- App（≥ 1.2.7）编译时默认 `API_BASE_URLS=https://rely-vpn-go.com,https://net-w.51aihelp.com`：新域名优先，旧域名备用。1.2.6 及更早的 App 只认旧域名，所以旧域名的 DNS 和 `tls.aliases` 要保留到这些版本都升级为止。iOS 默认禁止明文 HTTP，所以真机必须走 HTTPS。

> `payments.mock` 已关闭；接入支付中心（后台「系统设置 → 支付」）或易支付 / 支付宝后才能购买。

## 目录

```
server/                Go
  cmd/api              控制面
  cmd/node             代理节点（内嵌 Xray-core）
  internal/config      配置；catalog.go 是后台可编辑的目录（套餐/节点/应用/设置）及唯一的校验
  internal/api         HTTP 接口；admin*.go 是后台接口，admin_catalog.go 是通用 CRUD
  internal/store       SQLite（迁移在 store.go 的 migrations）
  internal/monitor     节点探测、心跳、预警
  internal/payment     支付宝 / 易支付 / 模拟支付
admin/                 管理后台前端：Next.js 16 + React 19 + Ant Design 6（见 admin/README.md）
mobile/                Go：vpncore，gomobile 编译成 Android AAR / iOS xcframework；cmd/huanyou-core 是桌面版（Windows / macOS）核心
app/                   Flutter App
```

### App 架构

```
lib/
  core/       api.dart（接口 + 模型）、session.dart（本地存储）、vpn.dart（原生 VPN 通道）、format.dart
  state/      app_controller.dart：唯一的状态源（登录、用户、节点、模式、隧道状态及所有操作）
              app_scope.dart：InheritedNotifier，页面用 AppScope.of(context) 读、调用方法
  theme/      app_theme.dart：颜色 / 圆角 / ThemeData，改视觉只改这里
  widgets/    复用组件：PrimaryButton、AppTextField、ConnectButton、RegionFlag、SectionCard…
  features/   每个功能一个目录：auth、shell（底部导航）、home、nodes、plans、me、apps、referral
```

页面不直接调 `ApiClient` 管理全局状态，而是调 `AppController` 的方法（`connect`、`selectNode`、`setMode`…），这样连接逻辑只有一份。新增底部 Tab 在 `features/shell/main_shell.dart` 的 `_tabs` 里加一项。

交互：首页点「当前节点」进入节点页，选中节点后自动返回并连接（已连接则切换重连）；选「智能选择」即切换到集群模式。底部三个模式块切换直连 / 集群 / AI，已连接时会自动重连。

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

`target` / `server_name` 要选一个**境外、大陆能直连、支持 TLS 1.3 + HTTP/2、不跳转**的网站，最好和你的 VPS 在同一地区或同一机房，并且 `server_name` 要在节点 `server_names` 列表里。

一个实测坑：客户端 uTLS 用 `chrome` 指纹时会带上抗量子的 X25519MLKEM768 密钥交换，如果目标站支持它（Cloudflare / Google / 微软这类大站现在默认开），REALITY 偷证书会失败，客户端报 `received real certificate`。要选**仍走经典 X25519** 的目标。线上这台美国节点实测 `www.apple.com` 可用；`www.microsoft.com` / `dl.google.com` 会失败。换目标后记得同时改节点的 `target`+`server_names` 和 API 里该节点的 `reality.server_name`，客户端配置每次连接都会重新下发。

> Xray 会对 apple/icloud 这类目标给出"可能被 GFW 封 IP"的告警。生产环境最好挑一个经典 X25519、低调、与节点同区的站点，逐个用真实客户端连通测试。

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

### 1.6 套餐、流量与限速

`api.json` 里的 `plans` 定义套餐：价格（分）、有效天数、每周期流量（`traffic_gb`，0 = 不限）、限速（`speed_mbps`，0 = 不限），以及超额后的速度（`over_quota_kbps`，0 = 超额后断开）。`free_plan` 指定没有付费套餐的用户用哪个套餐，留空表示不提供免费版。流量每 `traffic_period_days` 天（默认 30）重置一次。

- 节点按用户统计上下行流量，每次同步时上报给 API（`POST /internal/v1/traffic`），并保存按天汇总的记录。
- 节点按 API 下发的 `speed_kbps` 对每个用户限速，对已建立的连接也生效。为此节点关闭了 Linux 的 splice 零拷贝（启动时自动设置 `XRAY_BUF_SPLICE=disable`），会多用一些 CPU。
- 购买同一个套餐会顺延到期时间；购买其他套餐会立即替换当前套餐，并开始新的流量周期。

### 1.7 支付

App 下单后用系统浏览器打开支付页面，然后每 2 秒查询一次订单。支付平台会把结果异步通知到 `{public_url}/pay/notify/{alipay|epay|mock}`，服务端验证签名、核对金额后开通套餐。重复通知不会重复开通。

| 配置 | 说明 |
|---|---|
| `payments.alipay` | 支付宝开放平台「手机网站支付」，RSA2 签名。需要企业资质。`gateway` 改成沙箱地址就可以用沙箱测试。 |
| `payments.epay` | 「易支付」协议（`submit.php` + MD5），常见的聚合支付平台都兼容，可以接支付宝 / 微信。密钥建议用环境变量 `VPN_EPAY_KEY` 传入。 |
| `payments.mock` | 模拟支付，**只能用于开发**。开启后任何人都能免费开通套餐。 |

`public_url` 必须能从公网访问，否则收不到支付结果通知。

> App Store（iOS）和 Google Play（Android play 渠道）版本里，购买数字服务必须走各自的应用内购买，商店会拒绝外部支付链接。iOS 走 StoreKit（见「iOS 额外步骤」），Android play 渠道走 Google Play 结算（见「Google Play 上架」）；官网 APK / 桌面版仍用这里的外部支付。

### 1.8 用户管理

```bash
vpn-api -config /etc/vpn/api.json user disable a@b.com          # 停用（节点约 30 秒内踢下线）
vpn-api -config /etc/vpn/api.json user enable  a@b.com
vpn-api -config /etc/vpn/api.json user plan    a@b.com month 30  # 手动开通 / 顺延套餐
vpn-api -config /etc/vpn/api.json user expire  a@b.com 2026-12-31
vpn-api -config /etc/vpn/api.json user reset-traffic a@b.com    # 清零本周期流量
vpn-api -config /etc/vpn/api.json user rotate  a@b.com          # 更换该用户的 VLESS UUID
```

数据库结构升级在 API 启动时自动执行。

### 1.9 API 接口

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/auth/register` | `{email,password}` → `{token,user}` |
| POST | `/api/v1/auth/login` | 同上，按 IP 限流 |
| GET | `/api/v1/me` | 当前用户，含 `entitlement`（套餐、已用流量、限速、能否连接） |
| GET | `/api/v1/nodes` | 节点列表 |
| GET | `/api/v1/profile?node=hk1&mode=smart\|global` | Xray 客户端配置；没有可用套餐时返回 402 |
| GET | `/api/v1/usage` | 近 30 天每日流量 |
| GET | `/api/v1/plans` | 套餐和支付方式 |
| POST | `/api/v1/orders` | `{plan_id,method}` → `{order,pay_url}` |
| GET | `/api/v1/orders`、`/api/v1/orders/{id}` | 已支付订单 / 查询单个订单状态 |
| GET/POST | `/pay/notify/{provider}` | 支付平台异步通知 |
| GET | `/internal/v1/users` | 节点专用：可连接的用户及限速 |
| POST | `/internal/v1/traffic` | 节点专用：上报流量 |

---

## 2. 编译 App

需要：Go（脚本会自动下载 Go 1.26 工具链）、Flutter 3.41+、Android SDK + NDK、Xcode（iOS）。

```bash
# 1) 编译 Go 核心（精简版 Xray-core，只含 VLESS + REALITY / XHTTP 等用到的功能）
cd mobile && ./build.sh            # 或 ./build.sh android / ./build.sh ios

# 2) 运行 / 打包（API 地址在编译时注入，多个用逗号分隔）
cd ../app
flutter run --dart-define=API_BASE_URLS=https://api.example.com,https://api2.example.com
flutter build apk --release --split-per-abi --target-platform android-arm,android-arm64 \
  --obfuscate --split-debug-info=build/symbols --dart-define=API_BASE_URLS=...   # 官网 APK（site 渠道）
scripts/build_play.sh https://api.example.com                                     # Google Play（play 渠道，AAB）
```

> Android 有两个分发渠道（编译时用 `--dart-define=CHANNEL=` 区分，默认 `site`）：
> **site**（官网 APK / `scripts/build_android.sh`）保持现状——支付宝 / 易支付 / 网关支付、应用内更新、下载页、推荐应用。
> **play**（Google Play / `scripts/build_play.sh`，输出 AAB）固定走 Google Play 结算，没有网页支付、应用内更新、站外下载链接和推荐应用，连接前显示 VpnService 用途说明。两个渠道对各自用户行为一致，play 渠道不依赖服务端开关，也不在审核后切换。详见下方「Google Play 上架」。

> 每个 CPU 架构的 APK 约 20 MB：大陆 IP 库和配置解析放在服务端（API 下发编译好的 protobuf 配置），Go 核心只编入用到的协议。
> 发布用 `build/app/outputs/flutter-apk/app-arm64-v8a-release.apk`。`--split-per-abi` 会把 versionCode 设为「架构 × 1000 + 构建号」（arm64 为 2004），App 检查更新时会自动去掉前缀，所以**构建号要小于 1000，并且以后一直用分架构 APK 发布**（换成通用 APK 的 versionCode 会变小，无法覆盖安装）。
> 大陆 IP 库：服务端发版前运行 `app/scripts/fetch_geo.sh` 更新 `server/internal/profile/geoip-only-cn-private.dat`。

### 多语言

App 内置简体中文，界面文字以中文原文为 key（`tr('剩余 {0} 天', [days])`），其他语言是后台「多语言」页管理的语言包，App 按需下载并缓存：

- 默认跟随手机系统语言；没有对应语言包时用英语，再没有就用中文。用户可在「我的 → 语言」切换。阿拉伯语、波斯语、希伯来语等勾选「从右到左」后界面自动镜像。
- 新增 / 修改 App 文字后运行 `python3 scripts/i18n-extract.py`，更新 `server/internal/i18n/base.json`（翻译模板），再重新编译 API。
- 翻译方式：后台填 OpenAI 兼容接口后点「AI 自动翻译」；或点「复制 AI 提示词」贴到任意 AI 对话，把返回的 JSON 贴回来；也可以上传 JSON 文件或逐条校对。占位符 `{0}` 不一致会提示。
- 英语包的源文件在 `docs/i18n/en.json`。
- 官网（`/`、`/download`、`/i/{邀请码}`，模板 `server/internal/api/site_landing.html`）同样以中文原文为 key。右上角可切换语言（`?lang=xx`，记在 cookie 里）；没选过时按浏览器语言匹配，匹配不到用简体中文。译文优先取后台已启用的同名语言包，其次取内置的 `site_i18n.json`（英语、繁体中文、西班牙语、日语、韩语）。官网文字也会进 `base.json`，所以在后台新增语言并 AI 翻译后，官网会自动多出这门语言。

外观：「我的 → 外观」可选跟随系统 / 浅色 / 深色，颜色都在 `app/lib/theme/app_theme.dart` 的 `AppPalette`。

### iOS 额外步骤

PacketTunnel 扩展 target 已经通过 `app/ios/scripts/setup_packet_tunnel.rb` 加进了 Xcode 工程（这个脚本可以重复运行）。上架或真机运行前还需要：

1. 一个**付费** Apple Developer 账号。Network Extension 不能用免费账号签名，也不能在模拟器里运行。
2. 在 Apple Developer 后台注册两个 App ID：`com.vpnplatform.vpnApp` 和 `com.vpnplatform.vpnApp.PacketTunnel`，都勾选 **Network Extensions** 能力。
3. 在 Xcode 里打开 `ios/Runner.xcworkspace`，给 Runner 和 PacketTunnel 两个 target 都选上你的 Team。如果要改 Bundle ID，两个都要改，扩展的 ID 必须是 `<App ID>.PacketTunnel`。
4. 注意：国区 App Store 不允许上架 VPN 类 App，需要上架到其他地区。

### Google Play 上架

VPN 类 App 可以上 Google Play（Psiphon、Lantern 等抗审查工具都在架）。但 Play 要求数字服务走 Google Play 结算，且不能有站外下载和应用内自更新。为此 Android 有一个单独的 **play** 构建渠道（`scripts/build_play.sh`，`--dart-define=CHANNEL=play`），与官网 APK 相互独立，行为对所有用户一致、不受服务端开关影响：

| | play（Google Play） | site（官网 APK） |
|---|---|---|
| 支付 | Google Play 结算（Play Billing） | 支付宝 / 易支付 / 网关 |
| 应用内更新 | 关闭，由 Play 更新 | 保留 |
| 站外下载 / 推荐应用 / 「发客服充值」 | 隐藏 | 保留 |
| VpnService 用途说明 | 连接前展示并需同意 | 不展示 |
| 上报平台 | `android-play` | `android` |

服务端对接（Google Play Developer API 校验购买凭证）：

1. 建一个 Google Cloud 服务账号，在 Play Console「用户和权限」授予它「查看财务数据」和「管理订单和订阅」权限，下载 JSON 密钥。
2. 在 `api.json` 填 `payments.google_play`：
   ```json
   "google_play": {
     "package_name": "com.vpnplatform.vpn_app",
     "service_account_file": "/etc/vpn/google-sa.json"
   }
   ```
   密钥也可以用环境变量 `VPN_GOOGLE_SA_JSON`（整段 JSON）传入，优先级更高。
3. 在 Play Console 为每个付费套餐建一个**一次性商品**，产品 ID 填到后台「套餐」页的「Google Play 产品 ID」（留空则用套餐 ID，需与 Play Console 完全一致）。App 内显示的价格以 Google Play 返回为准。
4. App 买到后把购买凭证 POST 到 `/api/v1/iap/google/verify`，服务端用 Play Developer API 校验 `purchaseState`，用 Google 订单号做幂等开通套餐。套餐按「一次性商品」消耗，可重复购买续期（时长顺延）。

Play Console 提交时还要：填写 **VpnService 声明表**（选「VPN 是核心功能」）、提交 90 秒内演示连接的视频和一段演示 VpnService 用途说明（prominent disclosure）同意/拒绝流程的视频、在商店描述里写明使用了 VpnService、如实填写**数据安全**表单（设备标识 ANDROID_ID、行为统计、崩溃日志、连接来源 IP）并附隐私政策链接。

签名：启用 Play App Signing 时，**导入现有的 `android/keystore` 密钥作为 Play 应用签名密钥**，这样从 Play 安装的应用签名证书（以及绑定它的 ANDROID_ID 免登录身份）和官网 APK 一致。

> Google Play 在中国大陆不可用，主要面向海外用户；大陆用户仍走官网 APK。部分地区（美国 / 欧洲经济区 / 英国）如需在 Play 内提供外部支付，可报名 Google 的 billing choice 计划（按规范显示支付方式选择页、向 Google 上报交易并缴纳服务费），本仓库暂未实现该流程。
>
> 不实现「只给审核员看合规界面、普通用户切回外部支付」这类按审核状态翻转的做法：那属于规避平台审核，一旦被判定为欺骗，通常会终止开发者账号及其关联账号。

### 桌面版（Windows / macOS）

桌面版和手机版是同一套 Flutter 代码（`app/windows/`、`app/macos/`）。连接用的是 `huanyou-core`（`mobile/cmd/huanyou-core`，同样基于 Xray-core），以**系统代理模式**工作：

- App 启动核心进程，通过标准输入把 API 下发的配置交给它。核心在 `127.0.0.1` 的随机端口开一个 HTTP + SOCKS5 混合代理，并把系统代理指向这个端口。路由规则（大陆直连 / AI / 全局 / 集群）和手机版相同。
  - Windows：修改「设置 → 网络 → 代理」，不需要管理员权限，也不用装驱动。
  - macOS：用 `networksetup` 修改每个已启用网络服务的 HTTP / HTTPS / SOCKS 代理。需要管理员账户（Mac 的第一个账户默认就是），标准账户会提示失败。
- 浏览器和大多数遵循系统代理的软件会自动走代理。不读系统代理的程序（部分游戏、命令行工具）不会经过代理。
- 断开或退出 App 时会恢复用户原来的代理设置。核心被强制结束时，下次打开 App 会自动恢复（备份在 `%LocalAppData%\Huanyou` / `~/Library/Caches/Huanyou`）。
- 设备标识：Windows 用 MachineGuid，macOS 用硬件 IOPlatformUUID，重装 App 不会变成新账号。
- 桌面版没有扫码绑定，可以用「输入令牌」。
- 应用内更新：启动时和「我的 → 设置 → 检查更新」会检查新版本。桌面版点「立即更新」后在 App 里下载（断线自动续传）、校验 SHA-256，然后退出 App，用一个小脚本把新版本替换到原来的安装位置（macOS 替换 `.app`，Windows 覆盖 Huanyou.exe 所在文件夹）并重新打开（`app/lib/core/desktop_updater.dart`）。macOS 上从 DMG 里直接运行、或安装目录没有写入权限时，会提示改用浏览器下载。
- 标题栏和应用融为一体（`app/lib/widgets/desktop_frame.dart`，基于 `window_manager`）：系统标题栏隐藏，页面背景一直画到窗口顶部，标题栏高度像手机状态栏一样加在顶部安全区里。macOS 保留左上角的红黄绿按钮；Windows 在右上角画最小化 / 最大化 / 关闭按钮，并跟随深浅色。顶部空白处可以拖动窗口。窗口固定为 420×820，不能缩放、最大化或全屏（屏幕太矮时自动降低高度）；Windows 只允许运行一个实例。

```bash
# 1) 编译核心
cd mobile
./build.sh windows   # -> app/windows/core/huanyou-core.exe（纯 Go，任何系统都能编译）
./build.sh macos     # -> app/macos/core/huanyou-core（arm64 + x86_64 通用，需要 Mac）

# 2a) Windows：在装了 Flutter 和 Visual Studio 2022（「使用 C++ 的桌面开发」）的 Windows 上
cd app
powershell -ExecutionPolicy Bypass -File scripts\build_windows.ps1 -ApiBaseUrls https://api.example.com
#    -> app/build/dist/huanyou-windows-<版本>-<构建号>.zip

# 2b) macOS：在 Mac 上（Xcode + CocoaPods）
cd app
SIGN_IDENTITY="Developer ID Application: 名称 (TEAMID)" NOTARY_PROFILE=<公证配置> \
  scripts/build_macos.sh https://api.example.com
#    -> app/build/dist/huanyou-macos-<版本>-<构建号>.dmg（安装包：打开后把「环游」拖进「应用程序」，通用二进制）
```

- `-ApiBaseUrls` / 第一个参数对应手机版的 `--dart-define=API_BASE_URLS=...`。
- macOS 不设 `SIGN_IDENTITY` 时用临时签名（ad-hoc），可以在本机和测试机运行，但别人第一次打开要右键点 App 选「打开」。对外分发请用 Developer ID 签名并公证（`xcrun notarytool store-credentials` 先保存一次公证配置）。App 不使用沙盒，所以只能在 App Store 之外分发。
- macOS 的 Xcode 工程里有一个「Embed VPN core」构建步骤（`app/macos/scripts/add_core_phase.rb`，可以重复运行），负责把核心复制进 `.app` 并签名。
- `build_macos.sh` 会把 arm64 和 x86_64 分别编译再用 lipo 合并，因为 Xcode 27 的 lipo 不接受 Flutter 通用编译时的 `-verify_arch arm64 x86_64` 写法。

然后在后台「版本发布」选「发布 Windows 版本」上传 zip，或「发布 macOS 版本」上传 dmg。不用填版本号：服务端（`server/internal/pkginfo`）会从安装包里读出版本号、构建号和支持的芯片（Windows 读 Huanyou.exe 的版本信息和 CPU 类型；macOS 直接解析 DMG 里的 Info.plist 和程序头），并检查 VPN 核心是否打包进去。macOS 通用包（Apple 芯片 + Intel）记为「通用」，也可以另外发布只含 Apple 芯片或 Intel 的包，App 会按电脑的芯片拿到对应的包；ARM 电脑没有专用包时使用 x64 / Intel 包。下载页会多出对应的下载入口，App 内检查更新会在浏览器里打开下载链接。

---

### 测试

```bash
# 需要先启动开启了 payments.mock 的 API 和一个节点
cd app && flutter test integration_test/app_test.dart -d <模拟器或设备 ID> \
  --dart-define=API_BASE_URLS=http://127.0.0.1:18090 \
  --dart-define=E2E_NODE_TOKEN=<节点 token> --dart-define=E2E_NODE_NAME=<节点名称>
```

测试流程：注册（含两次密码不一致校验）→ 显示免费版 → 进节点页选节点 → 自动返回并连接 → 选「智能选择」切到集群模式 → 切换 AI / 直连模式 → 通过套餐标签进入购买页，模拟支付买「月付」→ 节点上报流量 →「我的」显示流量和购买记录 →「应用」Tab → 退出登录。iOS 模拟器不能运行 Network Extension，所以自动连接在模拟器上预期得到「模拟器不支持 VPN」的提示；实际连接要在 iPhone 真机上测。

`integration_test/screens_test.dart` 是截图巡览（每个页面停留几秒），配合 `xcrun simctl io booted screenshot` 使用。

## 已知限制 / 后续可做

- iOS 上架 App Store 需要接入 Apple 内购（见 1.7）；Android 上架 Google Play 用 play 渠道走 Google Play 结算（见「Google Play 上架」）。
- 管理员只有一个共享密码，没有多管理员和操作日志。
- 预警记录保存在内存里，API 重启后清空（Webhook 推送不受影响）。
- 节点 IP 仍可能因为流量过大被封。要有多个节点，被封时更换 IP，再改 `api.json` 里的 `address`（App 每次连接都会拉取最新配置）。
- 在大陆面向公众运营未经许可的 VPN 服务有法律风险，请自行评估。
