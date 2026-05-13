# CC Switch 匿名 Bootstrap 测试实施方案

## 1. 测试目标

本文档基于 `docs/cc-switch-anonymous-bootstrap-design.md` 和 CC Switch 客户端对接契约，定义 `new-api` 项目内的全链路测试实施方案。

测试需要证明：

- `POST /api/bootstrap/cc-switch` 默认关闭，开启后仅接受合法专有客户端请求。
- HMAC 签名、timestamp、nonce、防重放、IP/设备限流按预期工作。
- 首次启动、重复启动、同设备重装、Token 删除补发、用户禁用/删除、设备 blocked、双 hash 冲突等场景行为正确。
- 同一设备不会重复创建用户、不会重复领取 `QuotaForNewUser`。
- 新增 `bootstrap_devices` 迁移兼容 SQLite、MySQL >= 5.7.8、PostgreSQL >= 9.6。
- 响应只返回客户端所需 provider 配置和 API Token，不返回登录态、密码、控制台 access token 或用户身份信息。
- `web/default` 现有 CC Switch 入口生成的导入链接仍正确，并能与服务端返回的 provider 契约一致。

## 2. 范围与默认假设

### 覆盖范围

- 服务端模型、迁移、配置解析、签名校验、nonce 存储、限流、bootstrap 幂等流程、路由响应。
- 默认前端 `web/default` 中 API Keys 页已有的 `CC Switch` 导入入口。
- 与 CC Switch 客户端文档约定的请求签名、响应 provider 结构、action 语义和 Token 覆盖策略。

### 不覆盖范围

- 不测试 relay 计费、渠道分发、模型倍率和扣费细节；bootstrap 创建的 Token 后续仍复用现有 TokenAuth/relay 测试。
- 不在 `new-api` 仓库内实现或测试 CC Switch 桌面端 provider 锁定逻辑；这里只验证服务端契约和前端已有入口。
- 不证明客户端内置密钥不可逆向；测试只验证签名作为基础风控信号。

### 默认实施假设

- 新增专用盐值环境变量 `CC_SWITCH_BOOTSTRAP_SERVER_SALT`，测试中固定为 `test-bootstrap-salt`，避免设备哈希与 `CRYPTO_SECRET` 耦合。
- 服务端 Token 表仍保存无 `sk-` 前缀的原始 key；bootstrap 响应中的 `provider.api_key` 统一返回 `sk-` 前缀。
- 新增业务逻辑放在 `service` 层，controller 只负责读取原始 body、传入请求元信息并输出 HTTP 响应。
- Redis 集成测试使用环境变量显式开启；本地默认跑内存 fallback 测试。

## 3. 测试文件规划

建议新增或调整以下测试文件：

- `model/bootstrap_device_test.go`
  测试 `BootstrapDevice` 表迁移、字段类型、唯一索引、软/硬约束和跨数据库兼容。
- `service/cc_switch_bootstrap_test.go`
  测试配置解析、哈希、签名字符串、常量时间签名校验、nonce、防重放、限流和 bootstrap 主流程。
- `controller/cc_switch_bootstrap_test.go`
  通过 Gin 测试上下文覆盖 HTTP 状态码、响应结构、敏感字段泄露、路由无需登录态等行为。
- `router/api_router_test.go`
  验证 `/api/bootstrap/cc-switch` 已注册为公开 POST 路由，未被 `UserAuth`/`AdminAuth` 包裹。
- `web/default/src/features/keys/lib/cc-switch-url.test.ts`
  将现有 `buildCCSwitchURL` 抽为纯函数后，测试 Claude/Codex/Gemini 导入链接参数。

可选测试文件：

- `service/cc_switch_bootstrap_redis_test.go`
  仅在 `TEST_REDIS_CONN_STRING` 存在时运行，验证 Redis `SET NX EX` nonce 防重放。
- `docs/openapi` 或契约 fixture 测试
  若后续维护 `docs/openapi/api.json`，增加 bootstrap 请求/响应 schema 快照校验。

## 4. 服务端测试细案

### 4.1 配置与开关

测试项：

- 未设置 `CC_SWITCH_BOOTSTRAP_ENABLED` 或值为 `false` 时，接口返回 `403 Forbidden`，并包含 `bootstrap disabled`，不访问数据库。
- `CC_SWITCH_BOOTSTRAP_CLIENTS` 为空或 JSON 非法时，开启状态也应拒绝请求。
- `CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL` 为空时拒绝成功响应，避免返回不可用 provider。
- `CC_SWITCH_BOOTSTRAP_TOKEN_NAME` 为空时使用默认值 `CC Switch`。
- `CC_SWITCH_BOOTSTRAP_SIGNATURE_WINDOW_SECONDS` 非法、为 0 或负数时使用默认 300 秒。
- IP/设备限流配置非法时使用默认值，不 panic。
- `RegisterEnabled=false` 时 bootstrap 仍可创建普通用户，证明独立于公开注册开关。

建议断言：

- 配置解析使用纯函数，测试不依赖真实进程环境；每个用例传入 map，输出结构体。
- client secret 不写入日志、不出现在 API 响应。

### 4.2 签名与 nonce

测试项：

- 正确签名通过：签名字符串严格为 `POST\n/api/bootstrap/cc-switch\n{timestamp}\n{nonce}\nhex(sha256(raw_body))`。
- JSON 字段顺序变化时，只要签名基于实际发送 raw body，就能通过。
- 使用同一 body 但签名基于重排 JSON 时拒绝，证明不做服务端 canonicalization。
- method/path/timestamp/nonce/body hash 任一字段变化，签名均拒绝。
- `X-CCS-Client-Id` 不存在、未知或空值，返回 401。
- timestamp 超出窗口返回 401；边界 `now +/- window` 应允许，超过 1 秒拒绝。
- nonce 首次使用通过，窗口内重放返回 401 或 429，窗口过期后可重新使用。
- 签名比较使用常量时间比较；测试可通过代码审查点确认使用 `hmac.Equal`，不做时长断言。

内存 fallback 测试：

- `common.RedisEnabled=false` 时，使用进程内 TTL store。
- 测试 helper 注入 fake clock，避免真实等待 300 秒。
- 多 goroutine 同时使用同一 nonce，最多 1 个通过。

Redis 测试：

- 设置 `TEST_REDIS_CONN_STRING` 时启用。
- 同 nonce 第二次 `SET NX EX` 失败。
- key 格式为 `bootstrap_nonce:{client_id}:{nonce}`。
- TTL 与签名窗口一致。

### 4.3 请求字段校验

测试项：

- 缺失 `install_id`、`device_fingerprint`、`client_version`、`platform`、`arch` 分别返回 400。
- 空字符串、全空白字符串返回 400。
- 超长 `install_id`、`device_fingerprint`、`client_version`、`platform`、`arch` 返回 400。
- `platform` 只允许 `macos`、`windows`、`linux`。
- `build_channel` 为空允许；非空时建议只接受可审计字符串长度，不影响主流程。
- 请求 body 非 JSON 或 JSON 类型不为对象返回 400。
- 超大 body 不导致 panic；若走 `common.GetRequestBody`，按项目现有 body 限制返回错误。

隐私断言：

- `bootstrap_devices` 中只存 hash，不存原始 `install_id` 和 `device_fingerprint`。
- hash 为 64 位小写 hex。
- `risk_flags` 不写入原始 fingerprint，可写风险枚举如 `fingerprint_changed`、`hash_conflict`。

### 4.4 数据库迁移与唯一索引

SQLite 必跑测试：

- `AutoMigrate(&model.BootstrapDevice{})` 创建 `bootstrap_devices`。
- 字段包含 `install_id_hash`、`device_fingerprint_hash`、`user_id`、`token_id`、`status`、`risk_flags`、`first_ip`、`last_ip`、`user_agent`、`client_version`、`platform`、`arch`、`created_at`、`updated_at`、`last_seen_at`。
- `risk_flags` 类型为 text 兼容存储，不使用 JSONB。
- `install_id_hash` 唯一索引生效。
- `device_fingerprint_hash` 唯一索引生效。
- `user_id` 和 `token_id` 索引存在；若实现选择唯一索引，也要有显式测试说明。
- `migrateDB` 和 `migrateDBFast` 均包含 `BootstrapDevice`。

外部数据库测试：

- `TEST_MYSQL_DSN` 存在时跑 MySQL 迁移兼容测试；测试前若发现目标库已有 `bootstrap_devices`，直接 skip，避免破坏真实数据。
- `TEST_POSTGRES_DSN` 存在时跑 PostgreSQL 迁移兼容测试；同样拒绝在已有表的库上运行。
- MySQL/PostgreSQL 均验证重复 `install_id_hash`、重复 `device_fingerprint_hash` 插入失败。

### 4.5 Bootstrap 主流程

测试数据基础设置：

- 测试 DB 使用独立内存 SQLite：`file:{test_name}?mode=memory&cache=shared`。
- `common.QuotaForNewUser=12345`。
- `common.RegisterEnabled=false`。
- `common.RedisEnabled=false`。
- `CC_SWITCH_BOOTSTRAP_ENABLED=true`。
- `CC_SWITCH_BOOTSTRAP_CLIENTS={"cc-switch-proprietary":"test-secret"}`。
- `CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL=https://api.example.com`。
- `CC_SWITCH_BOOTSTRAP_SERVER_SALT=test-bootstrap-salt`。

核心用例：

| 场景 | 准备 | 期望 |
| --- | --- | --- |
| 首次启动 | 两个 hash 均不存在 | 返回 `created`，创建 1 个普通用户、1 个 enabled Token、1 条 active 设备记录，用户额度为 `QuotaForNewUser` |
| 同安装重复启动 | 同 `install_id` + 同 fingerprint | 返回 `resumed`，Token key 不变，用户数/Token 数/设备数不变，额度不增加 |
| 同设备重装 | 新 `install_id` + 同 fingerprint | 返回 `restored`，原用户和原 Token 不变，设备记录更新新 install hash 或按实现记录风险，额度不增加 |
| install 命中但 fingerprint 变化 | 同 `install_id` + 新 fingerprint | 返回 `resumed` 或按实现记录风险后返回原 Token，不创建新用户，`risk_flags` 包含 fingerprint 变化 |
| Token 被软删除 | 删除设备记录关联 Token | 返回 `token_rotated`，创建新 Token，更新 `token_id`，用户数不变，额度不增加 |
| Token disabled/expired/exhausted | 修改关联 Token 状态 | 若用户 active，补发新 Token 并返回 `token_rotated` |
| 用户 disabled | 修改用户 status | 返回 403，不返回 Token，不创建新 Token |
| 用户 soft deleted | 软删除用户 | 返回 403，不返回 Token，设备可标记 blocked |
| 设备 blocked | 设备 status=blocked | 返回 403，不返回 Token |
| hash 冲突 | install hash 命中 A，fingerprint hash 命中 B | 返回 409，不合并，不返回 Token，记录风险 |

创建用户断言：

- `Role=common.RoleCommonUser`。
- `Status=common.UserStatusEnabled`。
- 用户名以 `ccs_` 开头且不超过 `model.UserNameMaxLength`。
- `Password` 为空或不可用于登录；不返回任何密码字段。
- `Group` 使用项目默认值，不能写入空导致 relay 组异常。
- 调用 `User.InsertWithTx`，因此 `QuotaForNewUser` 生效。
- 事务提交后调用 `FinalizeOAuthUserCreation(0)`，新用户赠送日志和侧栏设置行为与 OAuth 注册一致。

创建 Token 断言：

- `Name` 等于配置值或默认 `CC Switch`。
- `Status=common.TokenStatusEnabled`。
- `ExpiredTime=-1`。
- `UnlimitedQuota=true`。
- `RemainQuota=0`。
- `Group` 使用默认组或项目既有默认策略。
- DB 中 key 不带 `sk-`，响应中 `provider.api_key` 带 `sk-`。

响应断言：

- `success=true`，`message=""`。
- `data.action` 为预期枚举。
- `data.provider.id="managed-newapi"`。
- `data.provider.name="NewAPI"`。
- `data.provider.base_url` 等于 `CC_SWITCH_BOOTSTRAP_PROVIDER_BASE_URL` 去尾斜杠后的值。
- `data.provider.models.claude/codex/gemini` 包含设计文档中的默认模型字段。
- `data.expires_at=0`。
- 响应 body 不包含 `password`、`access_token`、`session`、`username`、`user_id`、`quota`。
- 响应 header 不设置登录 session cookie。

### 4.6 并发幂等

测试项：

- 20 个 goroutine 同时发送完全相同请求；最终只有 1 个用户、1 个设备记录。
- 如果实现允许并发下 Token unique 冲突后重查，最终可接受 1 个或极短暂多 Token 回滚后的 1 个有效关联 Token；测试以 DB 最终状态为准。
- 每个成功响应的 `provider.api_key` 相同。
- 若某些请求因 nonce 重放被拒绝，则单独设计并发测试使用不同 nonce 但相同 body，避免把 nonce 语义和幂等语义混在一起。
- 并发唯一索引冲突路径必须回滚后重新查询绑定记录，不能返回 500。

### 4.7 限流

测试项：

- IP 限流：将 `CC_SWITCH_BOOTSTRAP_IP_LIMIT_PER_HOUR=2`，同 IP 第 3 次请求返回 429。
- 设备限流：将 `CC_SWITCH_BOOTSTRAP_DEVICE_LIMIT_PER_HOUR=2`，同 fingerprint 第 3 次请求返回 429。
- 不同 IP、同设备：触发设备限流。
- 同 IP、不同设备：触发 IP 限流。
- 限流命中时不创建用户、不创建 Token、不更新设备记录。
- Redis 关闭时使用内存限流；Redis 开启时使用 Redis key，key 中不包含原始 fingerprint，只包含 fingerprint hash。

## 5. Controller 与 Router 测试

Controller 测试使用 `httptest` 和 `gin.CreateTestContext`：

- `POST /api/bootstrap/cc-switch` 在关闭状态返回业务错误。
- 未携带任何认证 cookie 或用户 header 也能访问；鉴权只依赖 bootstrap 签名。
- 缺失签名 header 返回 401。
- 请求成功时 HTTP status 为 200，业务 `success=true`。
- 设备 blocked、用户 disabled 返回 HTTP 403。
- hash 冲突返回 HTTP 409。
- 限流返回 HTTP 429。
- 服务端内部错误返回 HTTP 500，响应不泄露内部 SQL 或 secret。

Router 测试：

- 使用 `router.SetApiRouter(gin.New())` 发起真实路由请求。
- 验证路径为 `/api/bootstrap/cc-switch`，不是 `/api/user/bootstrap` 或需要登录的子路由。
- 验证 gzip/global API middleware 不影响 body hash 校验；签名应基于 handler 读取到的原始 body。

## 6. 前端测试细案

当前 `web/default` 已有 API Keys 页 `CC Switch` 菜单和 `CCSwitchDialog`。测试目标不是重写 UI，而是保证导入链接和服务端 provider 契约一致。

建议先把 `cc-switch-dialog.tsx` 中的纯函数抽出：

- 新增 `web/default/src/features/keys/lib/cc-switch-url.ts`
- 导出 `buildCCSwitchURL`、`getCCSwitchEndpoint`
- 组件继续调用这些函数

单元测试：

| 场景 | 输入 | 期望 |
| --- | --- | --- |
| Claude 导入 | app=claude，base=https://api.example.com，apiKey=sk-abc | url scheme 为 `ccswitch://v1/import`，endpoint 为 `https://api.example.com` |
| Codex 导入 | app=codex | endpoint 自动追加 `/v1`，且不产生 `//v1` |
| Gemini 导入 | app=gemini | 包含 `model`，不包含空模型字段 |
| key 无前缀 | apiKey=abc | 组件或 helper 输出 `sk-abc` |
| key 已有前缀 | apiKey=sk-abc | 不重复变成 `sk-sk-abc` |
| server_address 缺失 | localStorage 无 status | fallback 到 `window.location.origin` |
| status JSON 损坏 | localStorage status 非 JSON | 不抛异常，fallback 到当前 origin |

前端命令：

```bash
cd web/default
bun test src/features/keys/lib/cc-switch-url.test.ts
bun run typecheck
bun run build
```

若后续引入 React Testing Library，再补组件交互测试：

- 打开 CC Switch 弹窗会请求 `/api/user/models`。
- 未选择主模型点击 `Open CC Switch` 会 toast warning，不调用 `window.open`。
- 选择模型后调用 `window.open(ccswitch://...)` 并关闭弹窗。
- 切换 app 会重置名称和模型字段。

## 7. 客户端契约与联调测试

虽然 CC Switch 桌面端不在本仓库内实现，但 `new-api` 需要提供可复现的契约联调方式。

建议新增测试 fixture：

- 固定 body：

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

- 固定 headers：
  - `X-CCS-Client-Id=cc-switch-proprietary`
  - `X-CCS-Timestamp=1760000000`
  - `X-CCS-Nonce=4f6b6c5c-9d87-4db7-bb5a-2c9bdebd81b4`
  - `client_secret=test-secret`

契约测试断言：

- Go 测试 helper 和客户端文档中的签名算法产出一致。
- 成功响应可直接映射到 CC Switch 的 Claude/Codex/Gemini provider 写入规则。
- `action=created/resumed/restored/token_rotated` 均对客户端是幂等 upsert provider。
- `403/409/429` 不返回 provider，客户端应保留本地已有 provider 或显示错误。

联调 smoke 流程：

1. 使用 SQLite 临时库启动 `new-api`。
2. 设置 bootstrap 测试环境变量。
3. 用脚本生成 HMAC 并 `curl POST /api/bootstrap/cc-switch`。
4. 校验响应 `provider.api_key` 可用于 `/v1/models` 或只读 token usage 接口。
5. 重复请求校验 action 从 `created` 变为 `resumed` 且 key 不变。
6. 改 install_id 保持 fingerprint 不变，校验 action 为 `restored`。

## 8. 推荐执行顺序

第一阶段：服务端纯逻辑

```bash
go test ./service -run 'TestCCSwitchBootstrap'
go test ./model -run 'TestBootstrapDevice'
```

第二阶段：HTTP 与路由集成

```bash
go test ./controller -run 'TestCCSwitchBootstrap'
go test ./router -run 'TestCCSwitchBootstrap'
```

第三阶段：全量回归

```bash
go test ./...
```

第四阶段：跨数据库兼容，按需运行

```bash
TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/newapi_test?charset=utf8mb4&parseTime=true' \
  go test ./model ./service ./controller -run 'Bootstrap.*MySQL|CCSwitch.*MySQL'

TEST_POSTGRES_DSN='host=127.0.0.1 user=newapi password=pass dbname=newapi_test sslmode=disable' \
  go test ./model ./service ./controller -run 'Bootstrap.*Postgres|CCSwitch.*Postgres'
```

第五阶段：Redis 防重放，按需运行

```bash
TEST_REDIS_CONN_STRING='redis://127.0.0.1:6379/15' \
  go test ./service -run 'TestCCSwitchBootstrapRedis'
```

第六阶段：前端入口回归

```bash
cd web/default
bun test src/features/keys/lib/cc-switch-url.test.ts
bun run typecheck
bun run build
```

## 9. 验收清单

- [ ] bootstrap 默认关闭，关闭时不会创建任何数据。
- [ ] 正确签名可通过，错误签名、超窗 timestamp、nonce 重放均拒绝。
- [ ] 首次启动只创建 1 个普通用户、1 个普通 Token、1 条设备绑定。
- [ ] 重复启动、重装恢复、Token 补发都不重复增加用户额度。
- [ ] 用户 disabled、用户 soft deleted、设备 blocked 均不返回 Token。
- [ ] 双 hash 冲突返回 409，不自动合并账号。
- [ ] 20 并发相同设备最终只有 1 个账号和 1 条设备绑定。
- [ ] SQLite/MySQL/PostgreSQL AutoMigrate 成功，唯一索引生效。
- [ ] Redis 开启和关闭两种 nonce 行为均符合预期。
- [ ] 响应没有登录态、密码、控制台 access token、用户名、用户 ID。
- [ ] `web/default` 生成的 CC Switch 导入链接与服务端 provider 响应结构一致。
- [ ] `go test ./...`、`bun run typecheck`、`bun run build` 通过。

## 10. 风险与关注点

- 并发幂等必须依赖数据库唯一索引和事务，不能只依赖应用层先查后插。
- nonce 防重放在多实例部署必须使用 Redis；内存 fallback 只适合单实例测试或单节点部署。
- 设备 hash 盐值一旦轮换，会影响重装恢复；专有部署应长期稳定保存 `CC_SWITCH_BOOTSTRAP_SERVER_SALT`。
- Token 补发必须先确认用户仍 active，避免为 disabled/deleted 用户恢复访问能力。
- 响应中的固定模型名属于 provider 配置契约；未来改默认模型时要同步更新服务端测试、前端链接测试和 CC Switch 客户端契约测试。
