# CC Switch 客户端对接文档

## 1. 概述

CC Switch 启动时调用 `POST /api/bootstrap/cc-switch` 即可自动获得一个 NewAPI 账号和 API Token，无需用户手动注册。该接口通过 HMAC 签名认证客户端身份，同一设备多次请求幂等返回同一账号。

### 核心流程

```
┌─────────────┐     POST /api/bootstrap/cc-switch      ┌──────────┐
│  CC Switch  │ ──────────────────────────────────────> │  NewAPI  │
│   桌面端    │ <────────────────────────────────────── │  服务端  │
└─────────────┘     返回 provider 配置 + API Key        └──────────┘
       │                                                       │
       │  用拿到的 api_key + base_url 正常调用                   │
       └───────────────────────────────────────────────────────┘
```

## 2. 环境变量配置（服务端）

CC Switch 客户端无需关心这些配置，但对接联调时需要服务端开启以下环境变量：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CC_SWITCH_BOOTSTRAP_ENABLED` | `false` | 必须设为 `true` |
| `CC_SWITCH_BOOTSTRAP_CLIENTS` | 空 | JSON 对象，`{"client_id":"client_secret"}` |
| `CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL` | 空 | 返回给客户端的 API 网关地址 |
| `CC_SWITCH_BOOTSTRAP_SERVER_SALT` | 空 | 设备标识哈希盐值，必须设置 |
| `CC_SWITCH_BOOTSTRAP_TOKEN_NAME` | `CC Switch` | 自动创建 Token 的名称 |
| `CC_SWITCH_BOOTSTRAP_SIGNATURE_WINDOW_SECONDS` | `300` | 签名时间窗口（秒） |
| `CC_SWITCH_BOOTSTRAP_IP_LIMIT_PER_HOUR` | `20` | 单 IP 每小时请求上限 |
| `CC_SWITCH_BOOTSTRAP_DEVICE_LIMIT_PER_HOUR` | `10` | 单设备每小时请求上限 |

## 3. 接口定义

### 3.1 请求

**端点**: `POST /api/bootstrap/cc-switch`

**Headers**:

```http
Content-Type: application/json
X-CCS-Client-Id: <client_id>
X-CCS-Timestamp: <unix_seconds>
X-CCS-Nonce: <uuid_v4>
X-CCS-Signature: <hex_hmac_sha256>
```

**Body** (JSON):

```json
{
  "install_id": "8e8b6a40-4214-44cb-b82e-4eecf09f42e8",
  "device_fingerprint": "v1:macos:stable-device-fingerprint",
  "client_version": "3.14.1-proprietary.1",
  "platform": "macos",
  "arch": "aarch64",
  "build_channel": "proprietary"
}
```

**字段说明**:

| 字段 | 类型 | 必填 | 最大长度 | 说明 |
| --- | --- | --- | --- | --- |
| `install_id` | string | 是 | 128 | UUID v4，客户端安装时生成，本地持久化 |
| `device_fingerprint` | string | 是 | 512 | 设备稳定指纹，建议格式 `v1:{platform}:{hash}` |
| `client_version` | string | 是 | 64 | 客户端版本号，用于灰度和风控 |
| `platform` | string | 是 | 32 | 仅允许 `macos`、`windows`、`linux` |
| `arch` | string | 是 | 32 | CPU 架构，如 `aarch64`、`x86_64` |
| `build_channel` | string | 否 | 64 | 构建渠道，建议固定为 `proprietary` |

### 3.2 签名算法

签名输入为以下 5 行，用 `\n` 拼接：

```text
POST
/api/bootstrap/cc-switch
<X-CCS-Timestamp>
<X-CCS-Nonce>
<hex(sha256(raw_request_body))>
```

然后用 `HMAC-SHA256(client_secret, signing_string)` 计算签名，输出为 hex 小写字符串。

**伪代码**:

```
method     = "POST"
path       = "/api/bootstrap/cc-switch"
timestamp  = X-CCS-Timestamp 的字符串值
nonce      = X-CCS-Nonce 的字符串值
body_hash  = hex(sha256(raw_body_bytes))
signing    = method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + body_hash
signature  = hex(hmac_sha256(client_secret, signing))
```

**注意事项**:

- 使用原始请求体字节计算 `body_hash`，不要对 JSON 做任何格式化或重排序
- timestamp 使用 Unix 秒（10 位数字），必须在服务端时间窗口 `±SIGNATURE_WINDOW_SECONDS` 内
- nonce 使用 UUID v4，在签名窗口内不可重复使用
- 签名比较使用常量时间比较，防止时序攻击

### 3.3 成功响应

**HTTP 200**:

```json
{
  "success": true,
  "message": "",
  "data": {
    "action": "created",
    "provider": {
      "id": "managed-newapi",
      "name": "NewAPI",
      "base_url": "https://api.example.com",
      "api_key": "sk-xxxxxxxxxxxxxxxx",
      "models": {
        "claude": {
          "model": "claude-sonnet-4-6",
          "haiku_model": "claude-haiku-4-5-20251001",
          "sonnet_model": "claude-sonnet-4-6",
          "opus_model": "claude-opus-4-7"
        },
        "codex": {
          "model": "gpt-5.4",
          "reasoning_effort": "high"
        },
        "gemini": {
          "model": "gemini-3.1-pro"
        }
      }
    },
    "expires_at": 0
  }
}
```

**`action` 枚举**:

| 值 | 含义 | 客户端行为 |
| --- | --- | --- |
| `created` | 首次开户注册 | 写入新 provider 配置 |
| `resumed` | 同一 install_id 重复启动 | 幂等，返回同一 Token |
| `restored` | install_id 变化但设备指纹命中，重装恢复 | 更新本地 provider 的 api_key |
| `token_rotated` | Token 被删除/禁用，已补发新 Token | 用新 api_key 替换旧 key |

**`provider` 字段**:

| 字段 | 说明 |
| --- | --- |
| `id` | provider 标识，固定为 `managed-newapi` |
| `name` | 显示名称，固定为 `NewAPI` |
| `base_url` | API 网关地址，所有模型请求的基础 URL |
| `api_key` | API Token，固定带 `sk-` 前缀 |
| `models` | 各 app 的默认模型配置，详见第 4 节 |

**`expires_at`**: `0` 表示 Token 永不过期。

### 3.4 错误响应

**HTTP 4xx/5xx**:

```json
{
  "success": false,
  "message": "<错误描述>"
}
```

**状态码速查**:

| 状态码 | 含义 | 客户端处理建议 |
| --- | --- | --- |
| `400` | 请求字段缺失/格式错误 | 检查 client_version、platform、arch 是否为空 |
| `401` | 签名错误 / timestamp 超窗 / client_id 无效 | 检查本地时间同步、client_secret 配置、签名算法 |
| `403` | bootstrap 功能关闭 / 设备被封禁 / 用户被禁用 | 保留已有本地 provider 配置，不再重试 |
| `409` | 设备指纹冲突（疑似篡改） | 上报异常，不清除本地已有配置 |
| `429` | 触发限流 | 等待 60 秒后重试，指数退避 |
| `500` | 服务端内部错误 | 等待 1 分钟后重试 |

## 4. Provider Models 配置

服务端返回的 `provider.models` 固定包含 Claude、Codex、Gemini 三组默认模型配置。客户端按 app 类型写入对应 provider。

### Claude

```
endpoint = {base_url}
```

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `model` | `claude-sonnet-4-6` | 默认模型（必填） |
| `haiku_model` | `claude-haiku-4-5-20251001` | Haiku 模型 |
| `sonnet_model` | `claude-sonnet-4-6` | Sonnet 模型 |
| `opus_model` | `claude-opus-4-7` | Opus 模型 |

### Codex

```
endpoint = {base_url}/v1
```

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `model` | `gpt-5.4` | 默认模型（必填） |
| `reasoning_effort` | `high` | 推理强度 |

### Gemini

```
endpoint = {base_url}
```

| 字段 | 值 | 说明 |
| --- | --- | --- |
| `model` | `gemini-3.1-pro` | 默认模型（必填） |

## 5. 客户端实现指南

### 5.1 install_id 的管理

`install_id` 用于区分不同安装实例，必须满足：

- 首次启动时生成 UUID v4（或等价随机标识），本地持久化存储
- 卸载重装后 `install_id` 会丢失（生成新的），此时依赖 `device_fingerprint` 恢复账号
- 同一安装多次启动时保持 `install_id` 不变

### 5.2 device_fingerprint 的生成

设备指纹用于跨重装的账号恢复，必须满足：

- 同一设备卸载重装后指纹不变
- 格式建议：`v1:{platform}:{stable_hash}`
- 指纹原文仅存在于客户端，服务端只存储 `sha256(salt + fingerprint)`，不落盘原始指纹
- 绝对不要包含可变信息（如安装时间戳、随机数）

### 5.3 调用时机

```
启动
  │
  ├── 本地已有 api_key → 直接使用，后台异步调用 bootstrap 检查更新
  │
  └── 本地无 api_key（首次安装 / 清除数据后）→ 同步调用 bootstrap 获取
```

### 5.4 幂等处理

对客户端而言，所有成功的 `action`（`created` / `resumed` / `restored` / `token_rotated`）处理方式一致：

```
1. 将 provider.api_key 写入本地 provider 配置
2. 将 provider.base_url 写入 endpoint
3. 将对应的 provider.models[app] 写入模型配置
4. 忽略 action 语义差异，将其视为幂等 upsert
```

### 5.5 Go 签名参考实现

```go
package main

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
)

func sign(
    method string,
    path string,
    timestamp string,
    nonce string,
    body []byte,
    secret string,
) string {
    bodyHash := sha256.Sum256(body)

    signingString := fmt.Sprintf(
        "%s\n%s\n%s\n%s\n%s",
        method,
        path,
        timestamp,
        nonce,
        hex.EncodeToString(bodyHash[:]),
    )

    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(signingString))
    return hex.EncodeToString(mac.Sum(nil))
}
```

### 5.6 JavaScript/TypeScript 签名参考实现

```typescript
async function sign(
  method: string,
  path: string,
  timestamp: string,
  nonce: string,
  body: string,
  secret: string
): Promise<string> {
  const encoder = new TextEncoder()
  const bodyBytes = encoder.encode(body)
  const bodyHashBuffer = await crypto.subtle.digest('SHA-256', bodyBytes)
  const bodyHash = Array.from(new Uint8Array(bodyHashBuffer))
    .map(b => b.toString(16).padStart(2, '0'))
    .join('')

  const signingString = `${method}\n${path}\n${timestamp}\n${nonce}\n${bodyHash}`

  const key = await crypto.subtle.importKey(
    'raw',
    encoder.encode(secret),
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign']
  )
  const sig = await crypto.subtle.sign('HMAC', key, encoder.encode(signingString))
  return Array.from(new Uint8Array(sig))
    .map(b => b.toString(16).padStart(2, '0'))
    .join('')
}
```

### 5.7 Rust 签名参考实现

```rust
use hmac::{Hmac, Mac};
use sha2::{Digest, Sha256};

type HmacSha256 = Hmac<Sha256>;

fn sign(
    method: &str,
    path: &str,
    timestamp: &str,
    nonce: &str,
    body: &[u8],
    secret: &str,
) -> String {
    let body_hash = hex::encode(sha2::Sha256::digest(body));

    let signing_string = format!(
        "{}\n{}\n{}\n{}\n{}",
        method, path, timestamp, nonce, body_hash
    );

    let mut mac = HmacSha256::new_from_slice(secret.as_bytes())
        .expect("HMAC key derivation");
    mac.update(signing_string.as_bytes());
    hex::encode(mac.finalize().into_bytes())
}
```

## 6. 完整请求示例

### cURL

```bash
#!/bin/bash

CLIENT_ID="cc-switch-proprietary"
CLIENT_SECRET="your-client-secret"
NONCE=$(uuidgen)
TIMESTAMP=$(date +%s)
BODY='{
  "install_id": "8e8b6a40-4214-44cb-b82e-4eecf09f42e8",
  "device_fingerprint": "v1:macos:stable-device-fingerprint",
  "client_version": "3.14.1-proprietary.1",
  "platform": "macos",
  "arch": "aarch64",
  "build_channel": "proprietary"
}'

# 计算签名
if command -v sha256sum >/dev/null 2>&1; then
  BODY_HASH=$(printf '%s' "$BODY" | sha256sum | awk '{print $1}')
else
  BODY_HASH=$(printf '%s' "$BODY" | shasum -a 256 | awk '{print $1}')
fi
SIGNING_STRING="POST
/api/bootstrap/cc-switch
$TIMESTAMP
$NONCE
$BODY_HASH"
SIGNATURE=$(printf '%s' "$SIGNING_STRING" | openssl dgst -sha256 -hmac "$CLIENT_SECRET" | awk '{print $NF}')

curl -X POST "https://api.example.com/api/bootstrap/cc-switch" \
  -H "Content-Type: application/json" \
  -H "X-CCS-Client-Id: $CLIENT_ID" \
  -H "X-CCS-Timestamp: $TIMESTAMP" \
  -H "X-CCS-Nonce: $NONCE" \
  -H "X-CCS-Signature: $SIGNATURE" \
  -d "$BODY"
```

## 7. 完整调用流程伪代码

```
function bootstrap():
    // 1. 准备请求
    install_id    = getOrCreateInstallId()     // 本地持久化的 UUID
    fingerprint   = getDeviceFingerprint()     // 设备稳定指纹
    client_id     = CLIENT_ID                  // 编译进二进制
    client_secret = CLIENT_SECRET              // 编译进二进制
    nonce         = uuidv4()
    timestamp     = unixSeconds()

    body = jsonEncode({
        install_id:         install_id,
        device_fingerprint: fingerprint,
        client_version:     APP_VERSION,
        platform:           osPlatform(),
        arch:               cpuArch(),
        build_channel:      "proprietary",
    })

    bodyHash    = hex(sha256(body))
    signingStr  = "POST\n/api/bootstrap/cc-switch\n" +
                  timestamp + "\n" + nonce + "\n" +
                  bodyHash
    signature   = hex(hmacSha256(client_secret, signingStr))

    // 2. 发送请求
    response = httpPost("https://<newapi-host>/api/bootstrap/cc-switch", {
        headers: {
            "Content-Type":     "application/json",
            "X-CCS-Client-Id":  client_id,
            "X-CCS-Timestamp":  timestamp,
            "X-CCS-Nonce":      nonce,
            "X-CCS-Signature":  signature,
        },
        body: body,
    })

    // 3. 处理响应
    if response.success:
        provider = response.data.provider
        // 写入本地 provider 配置
        writeProvider(provider.id, {
            name:     provider.name,
            base_url: provider.base_url,
            api_key:  provider.api_key,
            models:   provider.models,
        })
        return provider

    if response.status == 401:
        // 检查本地时间、client_secret、签名算法
        logError("bootstrap auth failed, check time sync and client secret")

    if response.status == 403:
        // 功能关闭或设备被封，使用已有配置
        logError("bootstrap forbidden, using cached provider if any")

    if response.status == 429:
        // 限流，退避后重试
        scheduleRetry(exponentialBackoff(60, 300))

    if response.status >= 500:
        scheduleRetry(fixedDelay(60))
```

## 8. 注意事项

1. **时间同步**: 客户端必须保证系统时间与 NTP 同步。签名时间窗口默认 ±5 分钟，偏差过大会导致 401。

2. **nonce 唯一性**: 每次请求必须使用不同的 nonce（推荐 UUID v4）。nonce 在签名窗口内不可重复。

3. **client_secret 保护**: client_secret 硬编码在客户端二进制中，存在被逆向的风险。服务端将其视为来源信号而非强安全凭证，额外有 IP/设备限流、双 hash 冲突检测等风控措施。

4. **body 原始字节**: 签名的 body_hash 必须基于**原始请求体字节**计算。不要对 JSON 做任何格式化、重排序、压缩或美化。

5. **api_key 前缀**: 响应的 `api_key` 始终带 `sk-` 前缀。写入 provider 配置时应直接使用此值。

6. **重装恢复**: 卸载重装后 `install_id` 变为新值，服务端通过 `device_fingerprint` 恢复原账号并返回 `action=restored`。客户端只需按新 `api_key` 更新本地配置。

7. **proxy 环境**: 如果客户端和 NewAPI 之间存在代理（如 CDN、反向代理），确保代理不会修改请求体或重排 JSON 字段顺序。签名失败时应检查代理是否篡改了 body。

8. **多实例部署**: 服务端多实例部署时，nonce 防重放依赖 Redis。如果 Redis 不可用，多实例间的 nonce 去重可能不完全。
