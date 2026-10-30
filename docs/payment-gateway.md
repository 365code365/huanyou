# 支付中心对接文档（下单链接模式）

对接只需要三件事：**一个下单链接（你提供）**，**一个通知接口**和**一个查询接口（我们提供，地址固定）**。

| 项目 | 值 |
|---|---|
| 商户号 `merchant_id` | `{{.MerchantID}}` |
| 下单链接（你的收银台） | `{{.PayLink}}` |
| 通知地址（固定） | `{{.NotifyURL}}` |
| 查询地址（固定） | `{{.QueryURL}}` |
| 签名密钥 `sign_key` | 后台「设置 → 支付」生成，64 位 hex（32 字节） |
| 加密密钥 `enc_key` | 后台「设置 → 支付」生成，64 位 hex（32 字节，AES-256） |
| 金额单位 | 分（整数），币种固定 `CNY` |

两把密钥只通过安全渠道交给支付中心，**不会出现在 App 和下单链接里**。泄露后在后台点「重新生成密钥」，旧密钥立即失效。

---

## 1. 下单：用户跳转到你的收银台

用户在 App 里选择套餐后，App 打开浏览器访问：

```
{{.PayLink}}?merchant_id={{.MerchantID}}&out_trade_no=VP20261030120000A1B2C3D4E5F6&amount=2500
  &currency=CNY&subject=月套餐&notify_url=…&query_url=…&return_url=…
  &timestamp=1793333333&expire_at=1793335133&nonce=9f1c…&sign=5ab3…
```

| 参数 | 说明 |
|---|---|
| `merchant_id` | 商户号 |
| `out_trade_no` | 系统订单号，全局唯一（你那边按它去重） |
| `amount` | 应付金额，单位分 |
| `currency` | `CNY` |
| `subject` | 商品名 |
| `notify_url` / `query_url` | 本系统的通知 / 查询地址（与上表相同，也可以写死在你那边） |
| `return_url` | 支付完成后浏览器跳回的地址 |
| `timestamp` | 链接生成时间（Unix 秒） |
| `expire_at` | 订单过期时间（Unix 秒，默认 30 分钟） |
| `nonce` | 随机串 |
| `sign` | 签名，见下 |

### 链接签名（你必须校验）

1. 取除 `sign` 外的所有参数，按参数名 ASCII 升序排序；
2. 拼成 `k1=v1&k2=v2…`，**值用原文，不做 URL 编码**（先 URL 解码再拼）；
3. `sign = hex( HMAC-SHA256(sign_key, 上一步的字符串) )`，小写；
4. 用常量时间比较；不一致、或 `now > expire_at` 时拒绝，**防止用户改金额**。

收款前建议再调一次 **查询接口** 确认订单号和金额（见第 3 节），这样即使链接被伪造也不会多收少收。

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

接收方校验顺序（任一失败即拒绝）：`merchant_id` 一致 → `|now − timestamp| ≤ 300 秒` → 常量时间验签 → `nonce` 10 分钟内未用过（防重放）→ GCM 解密成功 → 业务校验。

本系统对通知 / 查询请求严格按此校验：签名错误返回 `401 BAD_SIGN`，重放或过期返回 `401 BAD_ENVELOPE`，并计入异常请求，频繁出错的 IP 会被自动封禁。后台还可以设置「只允许这些 IP 调用」。

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

## 3. 查询订单 `POST {{.QueryURL}}`（支付中心 → 本系统）

请求体：信封，`data` 明文 `{"out_trade_no":"VP20261030…"}`
响应体：信封（用同样的密钥验签、解密），`data` 明文：

```json
{"out_trade_no":"VP20261030…","amount":2500,"currency":"CNY","subject":"月套餐",
 "status":"PENDING","created_at":1793333333,"paid_at":0}
```

`status`：`PENDING` 待支付 / `PAID` 已支付。订单不存在返回 `404 {"code":"ORDER_NOT_FOUND"}`。

---

## 4. 支付结果通知 `POST {{.NotifyURL}}`（支付中心 → 本系统）

用户付款成功后发送。请求体：信封，`data` 明文：

```json
{"out_trade_no":"VP20261030…","trade_no":"你的流水号","amount":2500,"currency":"CNY","status":"PAID"}
```

- 成功处理返回 `200 {"code":"SUCCESS"}`（明文），收到后停止重试；
- 其他任何响应都视为失败，请按 1 分钟、5 分钟、30 分钟、2 小时……重试，最长 24 小时；
- 通知是**幂等**的：同一订单重复通知只会开通一次；
- `amount` 必须与订单金额一致，否则拒绝（`400 REJECTED`），防止低价支付高价订单。

---

## 5. 对接检查清单

1. 用后台「生成测试链接」打开你的收银台，确认能验签、能展示订单号和金额；
2. 手工改链接里的 `amount`，确认你那边拒绝；
3. 对测试订单调用查询接口，能解出 `PENDING`；
4. 发一次 `PAID` 通知，后台「订单」里能看到「已支付」和你的流水号；
5. 把同一条通知原样再发一次，应返回 `401 BAD_ENVELOPE`（重放被拒）。
