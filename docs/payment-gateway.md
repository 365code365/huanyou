# 支付中心对接文档（下单链接模式）

本系统（商户）对接**外部支付中心**。支付中心需要提供两样东西，本系统提供一个固定接口：

| 方向 | 接口 | 谁提供 | 作用 |
|---|---|---|---|
| 用户浏览器 → 支付中心 | **下单链接**（收银台） | 支付中心 | 用户在这里付款 |
| 本系统 → 支付中心 | **订单查询接口** | 支付中心 | 本系统主动查询订单是否已支付 |
| 支付中心 → 本系统 | **支付结果通知** | 本系统（地址固定） | 付款成功后立即告知本系统 |

| 项目 | 值 |
|---|---|
| 商户号 `merchant_id` | `{{.MerchantID}}` |
| 下单链接（支付中心的收银台） | `{{.PayLink}}` |
| 订单查询接口（支付中心提供） | `{{.QueryURL}}` |
| 支付结果通知地址（本系统，固定） | `{{.NotifyURL}}` |
| 签名密钥 `sign_key` | 后台「系统设置 → 支付」生成，64 位 hex（32 字节） |
| 加密密钥 `enc_key` | 后台「系统设置 → 支付」生成，64 位 hex（32 字节，AES-256） |
| 金额单位 | 分（整数），币种固定 `CNY` |

两把密钥由本系统生成，只通过安全渠道交给支付中心，**不会出现在 App 和下单链接里**。泄露后在后台点「重新生成密钥」，旧密钥立即失效。

## 流程

```
 App            本系统(商户)                   支付中心
  │  选择套餐      │                              │
  │──────────────>│ 创建订单 VP2026…（待支付）      │
  │<── 下单链接 ───│ （链接带签名，金额改不了）        │
  │────────── 打开下单链接 ───────────────────────>│ ① 验签、展示订单
  │                │                              │ ② 用户付款成功
  │                │<──── POST 支付结果通知 ───────│ ③ 立即通知（失败按间隔重试）
  │                │ 验签解密 → 核对金额 → 开通套餐   │
  │                │──── 返回 SUCCESS ────────────>│
  │  轮询订单状态   │                              │
  │──────────────>│ 若仍待支付：POST 订单查询 ─────>│ ④ 返回订单状态
  │<── 已支付 ─────│<──── PAID ────────────────────│
```

- ③ 通知是主路径，支付成功后应**立即**发送。
- ④ 查询是兜底：通知丢失或延迟时，本系统会主动查询。用户停留在支付页时，App 每隔几秒查一次；即使用户关掉了 App，本系统后台也会对 2 小时内的未支付订单**每分钟**查询一次，2～24 小时内的每 15 分钟查询一次。
- 任意一条路径确认已支付都会开通套餐，并且只开通一次。

---

## 1. 下单链接（支付中心提供）

用户选择套餐后，App 用浏览器打开：

```
{{.PayLink}}?merchant_id={{.MerchantID}}&out_trade_no=VP20261030120000A1B2C3D4E5F6&amount=2500
  &currency=CNY&subject=月套餐&notify_url=…&return_url=…
  &timestamp=1793333333&expire_at=1793335133&nonce=9f1c…&sign=5ab3…
```

| 参数 | 说明 |
|---|---|
| `merchant_id` | 商户号 |
| `out_trade_no` | 本系统订单号，全局唯一。支付中心按它去重：同一订单号再次打开应显示同一笔订单，不能重复收款 |
| `amount` | 应付金额，单位分 |
| `currency` | `CNY` |
| `subject` | 商品名 |
| `notify_url` | 本系统的支付结果通知地址（与上表相同，也可以写死在支付中心） |
| `return_url` | 支付完成后浏览器跳回的地址 |
| `timestamp` | 链接生成时间（Unix 秒） |
| `expire_at` | 订单过期时间（Unix 秒，默认 30 分钟） |
| `nonce` | 随机串 |
| `sign` | 签名，见下 |

### 链接签名（支付中心必须校验）

1. 取除 `sign` 外的所有参数，按参数名 ASCII 升序排序；
2. 拼成 `k1=v1&k2=v2…`，**值用原文，不做 URL 编码**（先 URL 解码再拼）；
3. `sign = hex( HMAC-SHA256(sign_key, 上一步的字符串) )`，小写；
4. 用常量时间比较；不一致、商户号不对或 `now > expire_at` 时拒绝。这一步**防止用户在地址栏改金额**。

```python
import hmac, hashlib, time
from urllib.parse import parse_qsl

def verify_link(query: str, sign_key_hex: str) -> dict:
    p = dict(parse_qsl(query, keep_blank_values=True))
    canon = "&".join(f"{k}={p[k]}" for k in sorted(p) if k != "sign")
    want = hmac.new(bytes.fromhex(sign_key_hex), canon.encode(), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(want, p.get("sign", "")) or time.time() > int(p["expire_at"]):
        raise ValueError("bad sign or expired")
    return p
```

---

## 2. 加密信封（通知、查询的请求和响应都用它）

```json
{"merchant_id":"{{.MerchantID}}","timestamp":1793333333,"nonce":"32 位随机 hex","data":"<base64>","sign":"<hex>"}
```

- **加密**：`data = base64( iv(12 字节随机) || AES-256-GCM(enc_key, iv, 明文JSON, AAD = merchant_id) )`
  （GCM 认证标签 16 字节在密文末尾，Java / Go / Node / Python 的标准输出就是这个格式）
- **签名**：`sign = hex( HMAC-SHA256(sign_key, merchant_id + "\n" + timestamp + "\n" + nonce + "\n" + data) )`，签的是**密文**
- 每条消息都用新的 `timestamp` 和 `nonce`（重试也要重新生成信封）。

接收方校验顺序（任一失败即拒绝）：`merchant_id` 一致 → `|now − timestamp| ≤ 300 秒` → 常量时间验签 → `nonce` 10 分钟内未用过（防重放）→ GCM 解密成功 → 业务校验。

本系统收到的通知严格按此校验：签名错误返回 `401 BAD_SIGN`，重放或过期返回 `401 BAD_ENVELOPE`，并计入异常请求，频繁出错的 IP 会被自动封禁。后台还可以设置「只允许这些 IP 发通知」。本系统收到的查询响应同样按此校验，校验不通过的响应一律当作查询失败。

```python
import base64, hashlib, hmac, json, os, time
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

def seal(obj, mid, sign_key_hex, enc_key_hex):
    iv = os.urandom(12)
    ct = AESGCM(bytes.fromhex(enc_key_hex)).encrypt(iv, json.dumps(obj).encode(), mid.encode())
    env = {"merchant_id": mid, "timestamp": int(time.time()), "nonce": os.urandom(16).hex(),
           "data": base64.b64encode(iv + ct).decode()}
    msg = f'{mid}\n{env["timestamp"]}\n{env["nonce"]}\n{env["data"]}'.encode()
    env["sign"] = hmac.new(bytes.fromhex(sign_key_hex), msg, hashlib.sha256).hexdigest()
    return env

def open_(env, mid, sign_key_hex, enc_key_hex):
    assert env["merchant_id"] == mid and abs(time.time() - env["timestamp"]) <= 300
    msg = f'{mid}\n{env["timestamp"]}\n{env["nonce"]}\n{env["data"]}'.encode()
    want = hmac.new(bytes.fromhex(sign_key_hex), msg, hashlib.sha256).hexdigest()
    assert hmac.compare_digest(want, env["sign"])        # 再检查 nonce 未重复
    raw = base64.b64decode(env["data"])
    return json.loads(AESGCM(bytes.fromhex(enc_key_hex)).decrypt(raw[:12], raw[12:], mid.encode()))
```

---

## 3. 支付结果通知 `POST {{.NotifyURL}}`（支付中心 → 本系统）

用户付款成功后**立即**发送。请求体：信封，`data` 明文：

```json
{"out_trade_no":"VP20261030…","trade_no":"支付中心流水号","amount":2500,"currency":"CNY","status":"PAID"}
```

- 成功处理返回 `200 {"code":"SUCCESS"}`（明文），收到后停止重试；
- 其他任何响应都视为失败，请按 1 分钟、5 分钟、30 分钟、2 小时……重试，最长 24 小时；
- 通知是**幂等**的：同一订单重复通知只会开通一次，后续通知同样返回 `SUCCESS`；
- `amount` 必须与订单金额一致，否则拒绝（`400 REJECTED`），防止低价支付高价订单；
- 订单不存在返回 `404 ORDER_NOT_FOUND`。

---

## 4. 订单查询接口 `POST {{.QueryURL}}`（本系统 → 支付中心，由支付中心实现）

本系统主动调用，请求体：信封，`data` 明文：

```json
{"out_trade_no":"VP20261030…"}
```

支付中心返回 `200`，响应体：信封（用同样的密钥签名、加密），`data` 明文：

```json
{"out_trade_no":"VP20261030…","trade_no":"支付中心流水号","amount":2500,"currency":"CNY",
 "status":"PAID","paid_at":1793333444}
```

| `status` | 含义 | 本系统的处理 |
|---|---|---|
| `PENDING` | 待支付 | 继续等待 |
| `PAID` | 已支付 | 核对 `out_trade_no` 和 `amount` 后开通套餐 |
| `CLOSED` | 已关闭 / 已过期 | 继续显示待支付，不开通 |

- 支付中心没有这个订单（用户还没打开收银台，或已清理）时，返回 `404 {"code":"ORDER_NOT_FOUND"}`，这是正常情况；
- 信封校验失败返回 `401 {"code":"BAD_SIGN"}` / `{"code":"BAD_ENVELOPE"}`；
- 响应的 `out_trade_no` 必须与请求一致，`amount` 必须与订单金额一致，否则本系统不会开通；
- 本系统单次查询超时 6 秒，同一订单最多每 6 秒查询一次，不会给支付中心造成压力。

---

## 5. 对接检查清单

1. 在后台「系统设置 → 支付」填写下单链接、订单查询接口，生成商户号和密钥并保存；
2. 点「生成测试链接」打开收银台，确认能验签、能展示订单号和金额；
3. 手工改链接里的 `amount`，确认收银台拒绝；
4. 点「测试查询」输入任意订单号，应提示「接口和密钥正常，只是没有这个订单」；输入刚才的测试订单号应能查到 `PENDING`；
5. 在 App 里下一单并完成支付，后台「订单」里能看到「已支付」和支付中心流水号；
6. 把同一条通知信封原样再发一次，应返回 `401 BAD_ENVELOPE`（重放被拒）；
7. 让支付中心暂时不发通知、再下一单并支付，订单应在几秒（App 在支付页时）到 1 分钟内（后台兜底）自动变为已支付——这一步验证查询接口。

> 本系统源码自带一个**模拟支付中心** `server/cmd/paycenter-mock`，完整实现了上面的收银台、通知和查询接口，可作为支付中心的参考实现，也可以用来在本地把整个流程跑一遍：
> `go run ./cmd/paycenter-mock -merchant <商户号> -sign-key <签名密钥> -enc-key <加密密钥> [-drop-notify]`
