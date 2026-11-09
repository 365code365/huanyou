# 支付中心 paycenter

VPN 系统和信联pay 之间的支付中心。对 VPN 系统实现「下单链接」协议（`server/internal/api/payment_doc.md`），对上游调用信联pay 开放 API（<https://c02.z-cn.top/doc>），自带管理后台。

```
VPN App ──下单链接──> GET /checkout（验签）──> 收银台选支付方式 ──> 信联pay /api/pay/submit ──> 用户付款
信联pay ──异步回调──> POST /notify/xinlian/{通道ID}（验签、核金额）──> 订单已支付 ──> 加密通知 VPN（失败按 1m/5m/30m/2h… 重试 24h）
VPN ──加密查询──> POST /api/order/query（待支付时先向信联pay /api/pay/query 核实）
```

## 对 VPN 提供的三个接口

| 接口 | 地址 | 说明 |
|---|---|---|
| 下单链接（收银台） | `GET {对外地址}/checkout` | 校验 HMAC-SHA256 链接签名、商户号和 `expire_at`；同一 `out_trade_no` 只对应一笔订单，金额不同直接拒绝 |
| 订单查询接口 | `POST {对外地址}/api/order/query` | AES-256-GCM + HMAC 信封，校验时间窗和 nonce 防重放；没有订单返回 `404 ORDER_NOT_FOUND` |
| 支付结果通知 | 由本系统 POST 到 VPN 的 `/pay/gateway/notify` | 每次重试都用新的 timestamp/nonce；VPN 返回 `{"code":"SUCCESS"}` 才算成功 |

## 运行

```bash
make build
PAYCENTER_PUBLIC_URL=https://pay.example.com ./bin/paycenter        # 默认 SQLite：data/paycenter.db，监听 :8090
```

首次启动会创建管理员 `admin`，密码取 `PAYCENTER_ADMIN_PASSWORD`；没设置时随机生成并打印在日志里。忘记密码：`./bin/paycenter reset-password <新密码>`。

| 参数 | 环境变量 | 默认值 |
|---|---|---|
| `-listen` | `PAYCENTER_LISTEN` | `:8090` |
| `-db-driver` | `PAYCENTER_DB_DRIVER` | `sqlite`（可选 `mysql`） |
| `-db-dsn` | `PAYCENTER_DB_DSN` | `data/paycenter.db` |
| `-public-url` | `PAYCENTER_PUBLIC_URL` | 空；首次启动写入，之后在后台修改 |
| `-trust-proxy` | `PAYCENTER_TRUST_PROXY=1` | 关；放在 Nginx 后面时打开，从 `X-Real-IP` / `X-Forwarded-For` 取用户 IP |

### 切换到 MySQL

```bash
PAYCENTER_DB_DRIVER=mysql \
PAYCENTER_DB_DSN='user:pass@tcp(127.0.0.1:3306)/paycenter?charset=utf8mb4&parseTime=true&loc=Local' \
./bin/paycenter
```

表结构启动时自动迁移（GORM AutoMigrate），SQLite 和 MySQL 用同一套模型。DSN 必须带 `parseTime=true`。切换数据库不会自动迁移旧数据。

## 对接步骤

1. **系统设置**：填写对外地址（公网 https，信联pay 只接受公网回调地址）。
2. **支付通道**：添加信联pay 商户：接口地址、`pid`、MD5 密钥（RSA 下单时也要填，回调固定用 MD5 验签）、支付方式（每行 `type=显示名`，如 `alipay=支付宝`）。保存后点「测试连接」，会调用 `/api/merchant/info` 验证密钥。
3. **VPN 商户**：把 VPN 后台「系统设置 → 支付」里的商户号、签名密钥、加密密钥填进来（也可以在这里生成后填回 VPN），选择支付通道。
4. **VPN 后台**：下单链接填 `{对外地址}/checkout`，订单查询接口填 `{对外地址}/api/order/query`，保存。
5. 回到「VPN 商户」点「发送测试通知」。VPN 返回 `ORDER_NOT_FOUND` 说明地址和密钥都对；再到 VPN 后台点「生成测试链接」「测试查询」。

## 测试下单（不需要 VPN 参与）

后台「测试下单」里支付中心扮演 VPN：用商户密钥按 VPN 规则签名下单链接，支付结果通知发给内置的「模拟 VPN」接收端（`/test/vpn-notify`，同样验签、解密、防重放、核对金额），查询也按 VPN 的加密信封发起。测试订单带「测试」标记，不计入统计，也**不会通知真实 VPN**（即使商户配置了固定通知地址）。

- **一键自检**：自动跑 12 步（勾选上游时含信联pay 下单 / 查单 / 关单），不扣款。
- **手动测试订单**：生成下单链接和二维码，可以用手机走真实支付，也可以点「模拟支付成功」（只允许测试订单）。详情页可以「模拟 VPN 查询」，展示请求明文、加密信封、响应和解密结果。

## 说明

- 用户每选一次支付方式会在信联pay 建一笔上游订单（10 分钟内重复点同一方式会复用）。如果用户用两种方式都付了款，订单日志会记一条「重复支付」，需要人工退款。
- 信联pay 回调丢失时，后台会对 2 小时内的待支付上游订单每分钟查一次，2～24 小时内每 15 分钟查一次；VPN 来查询时，如果订单还没支付，也会先向信联pay 核实。订单过期 5 分钟后会关闭上游订单。
- 信联pay 文档里关于 `sign_type` 是否参与签名有矛盾（规则说剔除，示例里保留）。下单默认剔除；如果「测试连接」返回 4015，可以在通道里勾选「待签名串包含 sign_type」。回调验签两种写法都接受。
- 防重放的 nonce 缓存在进程内存里，请只跑一个实例；要多实例部署，需要把 nonce 换成共享存储（如 Redis）。
- 密钥以明文保存在数据库里，请限制数据库文件 / 账号的访问权限。

## 当前部署（162.211.181.15，域名 shoping.rely-vpn-go.com）

| 项目 | 位置 |
|---|---|
| 程序 | `/usr/local/bin/paycenter`，systemd 服务 `paycenter`，用户 `paycenter`，监听 `127.0.0.1:8090` |
| 配置 | `/etc/paycenter.env`（0600） |
| 数据库 | `/var/lib/paycenter/paycenter.db`（SQLite） |
| 初始管理员密码 | `/root/paycenter-admin.txt` |
| nginx 站点 | `/www/server/panel/vhost/nginx/shoping.rely-vpn-go.com.conf` |
| 443 分流 | `vpn-api` 占用 :443，非自身域名转给 nginx stream `127.0.0.1:8443`；`tcp/xy-tls-mux.conf` 的 map 里加了 `shoping.rely-vpn-go.com → 127.0.0.1:4444` |
| 证书 | DNS 生效后运行 `/opt/paycenter/issue-cert.sh` 申请 Let's Encrypt 并切换，之后由 certbot.timer 自动续期 |

更新程序：`make linux`，上传到服务器后 `install -m 0755 paycenter /usr/local/bin/paycenter && systemctl restart paycenter`。
