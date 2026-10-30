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
| **AI 应用模式** | 只有 AI 服务域名（OpenAI、Claude、Gemini 等，后台 `ai_domains` 可配）走代理，其余全部直连。适合只想加速 AI、其它走本地网络的场景。 |

### 运营功能（均在管理后台配置，即改即生效）

- **套餐 / 定价**：后台「套餐」页增删改，可下架（已购用户不受影响），可设免费套餐。
- **节点管理**：后台「节点」页新增节点时自动生成 Token、REALITY 密钥和 XHTTP 路径，「部署配置」直接给出节点服务器用的 `node.json`；可启停、排序、设权重、重置 Token。
- **节点监测与预警**：API 定时 TCP 探测每个节点，并记录节点代理的同步心跳。连续失败超阈值判离线，代理 3 分钟未同步判失联，向 Webhook 推送（通用 / 飞书 / 钉钉 / 企业微信格式）。后台可看近 1 小时探测记录、可用率、预警记录，可「立即检测」「发送测试消息」。App 拉配置时会优先下发在线节点。
- **邀请送时长**：每个用户有唯一邀请码，新用户注册填码后双方各得天数；奖励套餐、天数、每人上限在后台「系统设置」配置。
- **推荐应用**：名称 / 图标 / 分类 / 跳转地址 / 排序 / 上下架都在后台「推荐应用」页配置。
- **AI 模式域名**：后台「系统设置」里维护。

### 管理后台

`https://<API 域名>/admin/`（Next.js + React + Ant Design，静态导出后嵌入 `vpn-api` 二进制，生产环境不需要 Node）。页面：概览（统计卡片、14 天趋势、节点状态、最近预警）、用户（搜索 / 筛选 / 分页 / 新增 / 详情抽屉：改套餐与到期、重置密码、备注、停用、清流量、换 UUID、删除）、订单（筛选、手动入账）、套餐、节点、监控预警、推荐应用、系统设置。架构和扩展方式见 [admin/README.md](admin/README.md)。

套餐、节点、推荐应用和系统设置存在数据库里（首次启动时从 `api.json` 导入）。之后以后台为准；如果想用配置文件覆盖数据库，执行 `vpn-api -config api.json catalog import` 后重启。

## 当前线上部署

美国节点 **64.83.22.32**（Debian 12，宝塔 nginx），域名 **net-w.51aihelp.com**：

```
:443  nginx stream（ssl_preread 按 SNI 分流，PROXY protocol 传真实 IP）
      ├─ SNI = net-w.51aihelp.com → 127.0.0.1:8443 nginx HTTPS（Let's Encrypt）→ 127.0.0.1:8080 vpn-api
      └─ 其它 SNI（VPN 客户端 / 探测）→ 127.0.0.1:4443 vpn-node（REALITY，伪装 www.apple.com）
:80   ACME 验证 + 其余 301 跳转 https
```

- API / 管理后台：`https://net-w.51aihelp.com/`、`https://net-w.51aihelp.com/admin/`（管理员密码在 `/etc/vpn/api.env`）。
- 证书由 acme.sh 签发并自动续期（cron），续期后自动 reload nginx。证书在 `/etc/vpn/tls/`。
- 相关配置：`/www/server/panel/vhost/nginx/tcp/vpn-sni.conf`（443 分流）、`/www/server/panel/vhost/nginx/vpn-api.conf`（80 / 8443）；节点 `node.json` 里 `listen=127.0.0.1`、`vision_port=4443`、`accept_proxy_protocol=true`。
- App 编译时用 `--dart-define=API_BASE_URLS=https://net-w.51aihelp.com`。iOS 默认禁止明文 HTTP，所以真机必须走 HTTPS。

> 仍是演示配置：`payments.mock` 为开启状态，上线前关闭。

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
mobile/                Go：vpncore，gomobile 编译成 Android AAR / iOS xcframework
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

> iOS 的 App Store 版本里，App 内购买数字服务必须走 Apple 内购，审核会拒绝外部支付链接。目前只实现了外部支付，上架 App Store 需要另外接入 StoreKit。

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
  --obfuscate --split-debug-info=build/symbols --dart-define=API_BASE_URLS=...   # 直接分发
flutter build appbundle --release --dart-define=API_BASE_URLS=...                 # Google Play
```

> 每个 CPU 架构的 APK 约 20 MB：大陆 IP 库和配置解析放在服务端（API 下发编译好的 protobuf 配置），Go 核心只编入用到的协议。
> 发布用 `build/app/outputs/flutter-apk/app-arm64-v8a-release.apk`。`--split-per-abi` 会把 versionCode 设为「架构 × 1000 + 构建号」（arm64 为 2004），App 检查更新时会自动去掉前缀，所以**构建号要小于 1000，并且以后一直用分架构 APK 发布**（换成通用 APK 的 versionCode 会变小，无法覆盖安装）。
> 大陆 IP 库：服务端发版前运行 `app/scripts/fetch_geo.sh` 更新 `server/internal/profile/geoip-only-cn-private.dat`。

### 多语言

App 内置简体中文，界面文字以中文原文为 key（`tr('剩余 {0} 天', [days])`），其他语言是后台「多语言」页管理的语言包，App 按需下载并缓存：

- 默认跟随手机系统语言；没有对应语言包时用英语，再没有就用中文。用户可在「我的 → 语言」切换。阿拉伯语、波斯语、希伯来语等勾选「从右到左」后界面自动镜像。
- 新增 / 修改 App 文字后运行 `python3 scripts/i18n-extract.py`，更新 `server/internal/i18n/base.json`（翻译模板），再重新编译 API。
- 翻译方式：后台填 OpenAI 兼容接口后点「AI 自动翻译」；或点「复制 AI 提示词」贴到任意 AI 对话，把返回的 JSON 贴回来；也可以上传 JSON 文件或逐条校对。占位符 `{0}` 不一致会提示。
- 英语包的源文件在 `docs/i18n/en.json`。

外观：「我的 → 外观」可选跟随系统 / 浅色 / 深色，颜色都在 `app/lib/theme/app_theme.dart` 的 `AppPalette`。

### iOS 额外步骤

PacketTunnel 扩展 target 已经通过 `app/ios/scripts/setup_packet_tunnel.rb` 加进了 Xcode 工程（这个脚本可以重复运行）。上架或真机运行前还需要：

1. 一个**付费** Apple Developer 账号。Network Extension 不能用免费账号签名，也不能在模拟器里运行。
2. 在 Apple Developer 后台注册两个 App ID：`com.vpnplatform.vpnApp` 和 `com.vpnplatform.vpnApp.PacketTunnel`，都勾选 **Network Extensions** 能力。
3. 在 Xcode 里打开 `ios/Runner.xcworkspace`，给 Runner 和 PacketTunnel 两个 target 都选上你的 Team。如果要改 Bundle ID，两个都要改，扩展的 ID 必须是 `<App ID>.PacketTunnel`。
4. 注意：国区 App Store 不允许上架 VPN 类 App，需要上架到其他地区。

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

- iOS 上架 App Store 需要接入 Apple 内购（见 1.7）。
- 管理员只有一个共享密码，没有多管理员和操作日志。
- 预警记录保存在内存里，API 重启后清空（Webhook 推送不受影响）。
- 节点 IP 仍可能因为流量过大被封。要有多个节点，被封时更换 IP，再改 `api.json` 里的 `address`（App 每次连接都会拉取最新配置）。
- 在大陆面向公众运营未经许可的 VPN 服务有法律风险，请自行评估。
