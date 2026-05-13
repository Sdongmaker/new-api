# CC Switch 匿名启动开户注册设计方案

## 1. 背景与目标

本文档描述 `new-api` 如何为专有版 CC Switch 提供“安装/启动即匿名开户注册”的服务端能力。该能力的目标是让用户安装 CC Switch 后无需手动注册，即可获得一个普通 `new-api` 用户和一个普通 API Token，并直接使用首次注册额度。

### 目标

- 新增独立的匿名 bootstrap 接口：`POST /api/bootstrap/cc-switch`。
- 首次请求创建普通 `User`、普通 `Token` 和一条设备绑定记录。
- 同一安装、同一设备的重复请求必须幂等返回同一账号和 Token。
- 同一设备重装后，即使本地 `install_id` 丢失，也必须恢复原账号，不能重复领取首次注册额度。
- bootstrap 只返回 API Token 和客户端所需的固定 provider 配置，不返回登录态、密码或控制台凭证。
- 新能力默认关闭，仅在专有部署显式开启后生效。

### 非目标

- 不改现有 OAuth/OIDC 登录、绑定、注册和回调流程。
- 不改变普通用户名密码注册、登录、Passkey、用户控制台行为。
- 不改变 relay、计费、渠道、模型倍率和额度扣费逻辑。
- 不为匿名用户自动建立控制台登录态。
- 不承诺客户端内置密钥无法被逆向；签名只作为基础风控信号。

## 2. 总体设计

服务端新增一张设备 bootstrap 表和一个公开但受签名保护的接口。客户端启动时提交 `install_id` 和 `device_fingerprint`，服务端按哈希后的安装标识和设备指纹查找绑定关系。

核心原则：

- 账号仍然是现有 `users` 表中的普通用户。
- API Key 仍然是现有 `tokens` 表中的普通 Token。
- 首次额度仍然通过 `User.InsertWithTx` 复用 `QuotaForNewUser`。
- 设备绑定关系只记录在新增表 `bootstrap_devices` 中，不往 `users` 表加字段。
- 并发安全依赖数据库唯一索引和事务，兼容 SQLite、MySQL、PostgreSQL。

建议服务端配置使用环境变量或现有配置系统读取：

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `CC_SWITCH_BOOTSTRAP_ENABLED` | `false` | 是否启用匿名 bootstrap 接口 |
| `CC_SWITCH_BOOTSTRAP_CLIENTS` | 空 | JSON 对象，`client_id -> client_secret` |
| `CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL` | 空 | 返回给客户端的 NewAPI 网关地址 |
| `CC_SWITCH_BOOTSTRAP_TOKEN_NAME` | `CC Switch` | 自动创建 Token 的名称 |
| `CC_SWITCH_BOOTSTRAP_SIGNATURE_WINDOW_SECONDS` | `300` | 签名时间窗口 |
| `CC_SWITCH_BOOTSTRAP_IP_LIMIT_PER_HOUR` | `20` | 单 IP 每小时允许的创建/恢复请求数 |
| `CC_SWITCH_BOOTSTRAP_DEVICE_LIMIT_PER_HOUR` | `10` | 单设备每小时允许的 bootstrap 请求数 |

`CC_SWITCH_BOOTSTRAP_ENABLED` 是独立开关，不受普通 `RegisterEnabled` 影响。这样平台可以关闭公开注册，同时允许专有客户端开户注册。

## 3. 数据表设计

新增模型建议命名为 `BootstrapDevice`，表名为 `bootstrap_devices`，通过 GORM `AutoMigrate` 创建，并加入 `migrateDB` 与 `migrateDBFast`。

| 字段 | 类型建议 | 索引 | 说明 |
| --- | --- | --- | --- |
| `id` | `int` | 主键 | 自增主键，交给 GORM 处理 |
| `install_id_hash` | `varchar(64)` | 唯一 | `sha256(server_salt + install_id)` |
| `device_fingerprint_hash` | `varchar(64)` | 唯一 | `sha256(server_salt + device_fingerprint)` |
| `user_id` | `int` | 唯一/普通索引 | 绑定的 `users.id`，建议唯一 |
| `token_id` | `int` | 唯一/普通索引 | 绑定的 `tokens.id`，Token 丢失时可更新 |
| `status` | `varchar(32)` | 索引 | `active`、`blocked` |
| `risk_flags` | `text` | 无 | JSON 字符串或逗号分隔，避免 JSONB |
| `first_ip` | `varchar(64)` | 无 | 首次请求 IP |
| `last_ip` | `varchar(64)` | 无 | 最近请求 IP |
| `user_agent` | `varchar(255)` | 无 | 最近 User-Agent |
| `client_version` | `varchar(64)` | 无 | 最近客户端版本 |
| `platform` | `varchar(32)` | 无 | `macos`、`windows`、`linux` |
| `arch` | `varchar(32)` | 无 | `aarch64`、`x86_64` 等 |
| `created_at` | `bigint` | 无 | Unix 秒或毫秒，保持项目现有风格 |
| `updated_at` | `bigint` | 无 | 更新时间 |
| `last_seen_at` | `bigint` | 索引 | 最近 bootstrap 时间 |

兼容性要求：

- 不使用数据库专属 JSON 类型，`risk_flags` 用 `text`。
- 不写数据库专属 DDL，迁移走 GORM `AutoMigrate`。
- 唯一约束必须在三类数据库中同时有效。
- 业务代码中 JSON marshal/unmarshal 使用 `common.Marshal`、`common.Unmarshal` 或 `common.DecodeJson`。

## 4. 接口协议

### 4.1 请求

`POST /api/bootstrap/cc-switch`

Header：

```http
Content-Type: application/json
X-CCS-Client-Id: cc-switch-proprietary
X-CCS-Timestamp: 1760000000
X-CCS-Nonce: 4f6b6c5c-9d87-4db7-bb5a-2c9bdebd81b4
X-CCS-Signature: hex(hmac-sha256(...))
```

Body：

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

字段规则：

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `install_id` | 是 | UUID v4 或等价随机字符串，本地持久化 |
| `device_fingerprint` | 是 | 客户端稳定指纹，服务端只存哈希 |
| `client_version` | 是 | 用于灰度和风控 |
| `platform` | 是 | `macos`、`windows`、`linux` |
| `arch` | 是 | CPU 架构 |
| `build_channel` | 否 | 建议固定为 `proprietary` |

### 4.2 签名规则

签名输入使用原始请求体 hash，避免 JSON 字段顺序带来的 canonicalization 问题：

```text
METHOD + "\n" +
PATH + "\n" +
X-CCS-Timestamp + "\n" +
X-CCS-Nonce + "\n" +
hex(sha256(raw_body))
```

其中：

- `METHOD` 固定为 `POST`。
- `PATH` 固定为 `/api/bootstrap/cc-switch`，不包含 scheme、host、query。
- `X-CCS-Timestamp` 使用 Unix 秒。
- `X-CCS-Nonce` 在签名窗口内不可重复。
- `X-CCS-Signature` 为 `hex(hmac-sha256(client_secret, signing_string))`。
- 服务端必须使用常量时间比较校验签名。

nonce 防重放：

- Redis 启用时，使用 `SET NX EX` 记录 `bootstrap_nonce:{client_id}:{nonce}`。
- Redis 未启用时，使用进程内 TTL cache；多实例部署必须启用 Redis，否则只能提供单节点防重放。

### 4.3 成功响应

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

`action` 枚举：

| 值 | 含义 |
| --- | --- |
| `created` | 首次开户注册并创建 Token |
| `resumed` | 同一 `install_id` 重复启动，返回原 Token |
| `restored` | 同一设备重装，`install_id` 变化但 fingerprint 命中原账号 |
| `token_rotated` | 设备记录存在但 Token 被删除，补发新 Token |

`expires_at=0` 表示 Token 不设置过期时间。v1 不返回 `user_id`、用户名、密码、session cookie、控制台 access token。

### 4.4 错误响应

```json
{
  "success": false,
  "message": "bootstrap disabled"
}
```

建议状态码：

| 状态码 | 场景 |
| --- | --- |
| `400` | 请求字段缺失或格式错误 |
| `401` | client id 不存在、签名错误、timestamp 超窗 |
| `403` | 设备记录为 `blocked` |
| `409` | `install_id_hash` 和 `device_fingerprint_hash` 分别命中不同账号，疑似篡改 |
| `429` | IP、设备或全局限流 |
| `500` | 数据库、Token 生成等服务端错误 |

## 5. 服务端处理流程

1. 检查 `CC_SWITCH_BOOTSTRAP_ENABLED`，未开启直接拒绝。
2. 读取原始请求体，校验 `client_id`、timestamp、nonce、HMAC 签名。
3. 解析 JSON，校验必填字段和长度。
4. 计算 `install_id_hash`、`device_fingerprint_hash`。
5. 查询 `bootstrap_devices`：
   - 两个 hash 命中同一行：幂等返回。
   - 仅 `install_id_hash` 命中：幂等返回，并记录 fingerprint 变化风险。
   - 仅 `device_fingerprint_hash` 命中：重装恢复，更新 `install_id_hash` 或记录为新安装，返回原账号。
   - 两个 hash 命中不同行：拒绝并记录冲突风险。
   - 均未命中：进入开户注册事务。
6. 注册事务：
   - 创建 `User`，`Role=RoleCommonUser`，`Status=UserStatusEnabled`，用户名建议 `ccs_` 加短随机串。
   - 调用 `InsertWithTx`，复用 `QuotaForNewUser`。
   - 创建 `Token`，`Status=TokenStatusEnabled`，`ExpiredTime=-1`，`UnlimitedQuota=true`，`RemainQuota=0`。
   - 创建 `BootstrapDevice` 绑定两个 hash、`user_id`、`token_id`。
   - 提交事务后调用 `FinalizeOAuthUserCreation(0)` 生成侧栏设置和新用户赠送日志。
7. 如果事务因唯一索引冲突失败，回滚后重新查询绑定记录并返回，保证并发幂等。
8. 返回 provider 配置。

## 6. 幂等决策表

| 场景 | install_id_hash | fingerprint_hash | 处理 | 是否赠送首次额度 |
| --- | --- | --- | --- | --- |
| 首次启动 | 不存在 | 不存在 | 创建用户、Token、设备记录，返回 `created` | 是 |
| 重复启动 | 命中 A | 命中 A | 更新 `last_seen_at`，返回 A 的 Token，`resumed` | 否 |
| 同设备重装 | 不存在 | 命中 A | 更新/记录新安装信息，返回 A 的 Token，`restored` | 否 |
| Token 被删除 | 命中 A | 命中 A | 给 A 的用户补发 Token，更新 `token_id`，`token_rotated` | 否 |
| 设备 blocked | 命中 A | 命中 A | 返回 `403`，不返回 Token | 否 |
| 疑似篡改 | 命中 A | 命中 B | 返回 `409`，记录风险，不返回 Token | 否 |

用户被禁用或删除时：

- `User.Status != enabled`：返回 `403`，不返回 Token。
- 用户已软删除：返回 `403`，设备记录可标记 `blocked`。
- Token 状态非 enabled：补发新 Token 前必须确认用户仍 active。

## 7. 风控边界

必须明确：内置客户端密钥会进入桌面客户端二进制，存在被逆向提取的可能，不能作为强安全边界。服务端风控应把签名视为“专有客户端来源信号”，而不是“不可伪造证明”。

建议 v1 风控组合：

- HMAC 签名、timestamp、nonce 防重放。
- IP 级限流、设备级限流、全局限流。
- `install_id_hash` 与 `device_fingerprint_hash` 双维度幂等。
- 同 fingerprint 新 install_id 只恢复原账号，不重复开户注册。
- 冲突命中不自动合并，直接拒绝。
- 管理员可手动把设备记录设为 `blocked`。
- 记录 `first_ip`、`last_ip`、`client_version`、`platform`、`user_agent` 便于审计。

不建议 v1 做的事情：

- 不把 device fingerprint 原文落库。
- 不把客户端签名当成付费级防盗刷。
- 不为了匿名开户注册修改现有 OAuth/OIDC 表结构。
- 不自动登录控制台。

## 8. 与现有功能的关系

- OAuth/OIDC：保持现状，未来可新增“匿名账号升级/绑定 OAuth”能力，但不属于 v1。
- 普通注册：不受影响，bootstrap 有独立开关。
- 首次注册额度：由 `User.InsertWithTx` 设置 `QuotaForNewUser`，只在创建用户时触发。
- Token：使用现有 `tokens` 表，`UnlimitedQuota=true` 只表示 Token 本身不限额，用户余额仍参与消费校验。
- Relay：客户端拿到 Token 后仍走现有 OpenAI/Claude/Gemini 兼容接口。
- 数据库迁移：只新增 `bootstrap_devices`，不改旧表字段。

## 9. 测试清单

服务端单元/集成测试建议覆盖：

- `bootstrap disabled` 时接口拒绝。
- 签名正确时通过，签名错误、timestamp 超窗、nonce 重放时拒绝。
- 首次启动创建 1 个用户、1 个 Token、1 条设备记录。
- 同一 `install_id` 重复请求返回同一 Token，不新增用户，不重复赠送额度。
- 新 `install_id` + 同 fingerprint 返回原 Token，不新增用户。
- Token 被软删除后补发新 Token，但不新增用户、不增加用户额度。
- 用户 disabled 或 soft deleted 时返回 `403`。
- `install_id_hash` 命中 A、`fingerprint_hash` 命中 B 时返回 `409`。
- 并发 20 个相同请求最终只创建 1 个用户和 1 条设备记录。
- Redis 开启和未开启时 nonce 行为符合预期。
- SQLite、MySQL、PostgreSQL 下 `AutoMigrate` 成功，唯一索引生效。

验收标准：

- v1 响应中没有登录态、密码、控制台 access token。
- 同设备重装不会重复领取首次注册额度。
- 关闭该功能后，现有登录、注册、OAuth/OIDC、relay 测试不受影响。
