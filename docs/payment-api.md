# 收银台对接协议 v1（Cashier API）

环游后台（下称「商户」）与收银台（下称「收银台」）之间的接口约定。下单、查询、回调三个接口使用**同一种加密信封**，双向都加密、双向都签名。参考实现：`server/internal/payment/cashier.go`，测试：`cashier_test.go`。

## 1. 密钥

双方离线共享三项配置，任何一项都不能出现在 App 里：

| 名称 | 说明 |
|---|---|
| `merchant_id` | 商户号，如 `M1001` |
| `sign_key` | HMAC-SHA256 签名密钥，≥16 字节，hex 或 base64 |
| `enc_key` | AES-256-GCM 加密密钥，**正好 32 字节**，hex 或 base64 |

生成示例：`openssl rand -hex 32`（分别生成两次，`sign_key` 和 `enc_key` 不要相同）。

商户侧配置（`/etc/vpn/api.json`），密钥建议放环境变量 `VPN_CASHIER_SIGN_KEY` / `VPN_CASHIER_ENC_KEY`：

```json
"payments": {
  "cashier": {
    "api_url": "https://pay.example.com",
    "merchant_id": "M1001",
    "channels": [{"id": "alipay", "name": "支付宝"}, {"id": "wxpay", "name": "微信支付"}]
  }
}
```

## 2. 信封（所有请求、响应、回调的 Body）

```json
{
  "merchant_id": "M1001",
  "timestamp": 1793333333,
  "nonce": "9f1c2a7e5b3d4c6f8a0b1c2d3e4f5a6b",
  "data": "<base64>",
  "sign": "<hex>"
}
```

- `Content-Type: application/json`，UTF-8。
- `timestamp`：Unix 秒。
- `nonce`：16–64 位随机字符串，每条消息都要新生成。
- **加密**：`data = base64( iv || AES-256-GCM(enc_key, iv, 明文JSON, AAD = merchant_id) )`
  - `iv` 为 12 字节随机数，放在密文最前面；GCM 认证标签 16 字节，接在密文最后（Go/Java/Node 的标准 GCM 输出即为此格式）。
  - AAD 绑定 `merchant_id`，把密文挪给其他商户会解密失败。
- **签名**：`sign = hex( HMAC-SHA256(sign_key, merchant_id + "\n" + timestamp + "\n" + nonce + "\n" + data) )`
  - 签名的对象是**密文** `data`，接收方先验签、再解密。

### 接收方校验顺序（任一失败即拒绝）

1. `merchant_id` 一致；
2. `|now − timestamp| ≤ 300 秒`；
3. 按上式重算签名，用**常量时间**比较；
4. `nonce` 在 10 分钟内未出现过（防重放，验签通过后才记录）；
5. AES-GCM 解密成功（完整性已由 GCM 保证）；
6. 业务字段校验（订单号、金额、币种）。

## 3. 订单字段（`data` 解密后的 JSON）

| 字段 | 类型 | 说明 |
|---|---|---|
| `out_trade_no` | string | 商户订单号，如 `VP20261030133000A1B2C3D4E5F6`，全局唯一 |
| `trade_no` | string | 收银台流水号（收银台生成） |
| `amount` | int | 金额，**单位：分**，整数，避免浮点误差 |
| `currency` | string | 固定 `CNY` |
| `subject` | string | 商品名，如「月套餐」 |
| `channel` | string | 支付渠道：`alipay` / `wxpay` / … |
| `notify_url` | string | 支付结果回调地址 |
| `return_url` | string | 支付完成后浏览器跳回地址 |
| `expire_at` | int | 订单过期时间（Unix 秒），商户默认 30 分钟 |
| `status` | string | `PENDING` / `PAID` / `CLOSED` / `REFUNDED` |
| `pay_url` | string | 收银台页面地址，必须是 https |
| `paid_at` | int | 支付时间（Unix 秒） |

## 4. 接口

### 4.1 下单 `POST {api_url}/v1/orders/create`（商户 → 收银台）

请求 `data`：

```json
{"out_trade_no":"VP…","amount":2500,"currency":"CNY","subject":"月套餐","channel":"alipay",
 "notify_url":"https://rely-vpn-go.com/pay/notify/cashier",
 "return_url":"https://rely-vpn-go.com/pay/return?order=VP…","expire_at":1793335133}
```

响应：HTTP 200 + 信封，`data`：

```json
{"out_trade_no":"VP…","amount":2500,"status":"PENDING","pay_url":"https://pay.example.com/c/xxxx"}
```

- 同一 `out_trade_no` 重复下单必须**幂等**，返回同一个 `pay_url`。
- 商户会核对响应里的 `out_trade_no` 和 `amount` 与请求一致。
- App 打开 `pay_url` 进入收银台。

### 4.2 查询 `POST {api_url}/v1/orders/query`（商户 → 收银台）

请求 `data`：`{"out_trade_no":"VP…"}`

响应 `data`：

```json
{"out_trade_no":"VP…","trade_no":"T…","amount":2500,"status":"PAID","paid_at":1793333400}
```

商户在 App 轮询订单、但回调还没到时主动调用（同一订单 6 秒最多 1 次，下单 2 小时内有效）。

### 4.3 支付回调 `POST {notify_url}`（收银台 → 商户）

即 `https://rely-vpn-go.com/pay/notify/cashier`。Body 为信封，`data`：

```json
{"out_trade_no":"VP…","trade_no":"T…","amount":2500,"currency":"CNY","status":"PAID","paid_at":1793333400}
```

商户处理规则：

- 校验通过且 `amount` 与订单一致 → 开通套餐 → 返回 HTTP 200 `{"code":"SUCCESS"}`。
- 已处理过的订单再次回调 → 同样返回 `SUCCESS`，**不会重复开通**（幂等）。
- 验签 / 解密 / 金额不符等失败 → 返回 HTTP 400，收银台应重试。
- `status` 不是 `PAID`（如 `CLOSED`）→ 返回 `SUCCESS`，不做处理。

收银台重试建议：未收到 `SUCCESS` 时按 15s、1m、5m、15m、1h、2h、6h 重试，**每次重试重新生成 `timestamp`、`nonce` 和签名**（旧信封会被当作重放拒绝）。

### 4.4 错误响应

非 2xx 时 Body 为明文（不含敏感数据）：`{"code":"BAD_SIGN","message":"signature mismatch"}`。

| code | 含义 |
|---|---|
| `BAD_SIGN` | 签名错误 |
| `EXPIRED` | 时间戳超出 ±300 秒 |
| `REPLAY` | nonce 重复 |
| `DECRYPT_FAILED` | 解密失败 |
| `ORDER_NOT_FOUND` | 查询的订单不存在 |
| `AMOUNT_MISMATCH` | 同一订单号金额不一致 |

## 5. 安全要求

- 全程 HTTPS（TLS 1.2+）。
- 金额以服务端订单为准，绝不信任前端传来的金额。
- 密钥只放服务端配置 / 环境变量，泄露后立刻双方同时更换。
- 收银台建议对 `notify_url` 做白名单校验。
