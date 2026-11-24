# Sub-Pool 后端

Go 服务负责本地身份、访问权限和业务状态。它通过 PostgreSQL 保存配置、采集事实、份额契约与操作记录，并按当前连接配置访问 Sub2API。控制台通过 Next.js 的同源路由访问 Go。

## 从源码运行

源码运行使用 Go 1.27.4。在仓库根目录运行 `go -C backend version` 可核对编译器。准备专用 PostgreSQL 数据库，部署账号需能创建和更新 Sub-Pool 的表结构。生成并保存保护密钥的方法见 [部署说明](../README.md)。

以下命令在仓库根目录运行。将数据库地址和密钥换成自己的值。

```bash
export SUB_POOL_DATABASE_URL='postgres://sub_pool:YOUR_PASSWORD@127.0.0.1:5432/sub_pool?sslmode=disable'
export SUB_POOL_SECRET_KEY='YOUR_BASE64_KEY'
export SUB_POOL_PUBLIC_ORIGIN='http://127.0.0.1:3000'
export SUB_POOL_ALLOW_LOOPBACK_HTTP=true

go -C backend run ./cmd/sub-pool serve
```

在 PowerShell 中用 `$env:名称 = '值'` 设置同样的进程环境。`serve` 会应用待执行的迁移。数据库没有管理员时会创建 `admin`，通过标准输出的 `initial_admin_password` 事件一次性交付自动生成的 20 位初始密码。首次登录须改密。

要手动指定首个管理员，可在启动 `serve` 前运行 `go -C backend run ./cmd/sub-pool bootstrap <用户名>`，从标准输入提供 UTF-8 无 BOM 密码。密码至少十个字符，包含 ASCII 字母和数字，上限为 128 个 UTF-8 字节。

| 变量 | 用途 |
| --- | --- |
| `SUB_POOL_DATABASE_URL` | 必填。Sub-Pool 独立数据库的连接地址。 |
| `SUB_POOL_DB_MAX_CONNS` | `serve` 的请求连接池上限，默认 `25`，使用正整数。采集、通知、来源到期和运行期锁共用独立的 8 条后台连接预算。 |
| `SUB_POOL_SECRET_KEY` | 32 字节随机密钥的 Base64 值，用于保护上游、邮件凭据及业务备份。跨重启和恢复操作保留同一密钥。 |
| `SUB_POOL_INITIAL_ADMIN_PASSWORD` | 可选。首次自动创建管理员时使用，按同一密码策略校验，空值会导致此次初始化失败。口令不会写入日志。 |
| `SUB_POOL_PUBLIC_ORIGIN` | 浏览器实际访问的 origin，只包含协议、主机和必要的端口。 |
| `SUB_POOL_LISTEN` | Go 的监听地址，默认 `127.0.0.1:8081`。 |
| `SUB_POOL_ALLOW_LOOPBACK_HTTP` | `true` 时允许明确指定的回环 IP 使用 HTTP。公网访问走 HTTPS。 |
| `SUB_POOL_TRUSTED_PROXY_CIDRS` | 可选。逗号分隔的可信直接代理地址段，最多 32 项。通过 Next 传递真实来源还需要配置并验证入口。 |

浏览器端的本地启动和入口配置见[前端说明](../web/README.md)。

### 本机恢复管理员密码

在部署 Sub-Pool 的服务器上使用同一个 `SUB_POOL_DATABASE_URL`，运行 `sub-pool reset-password <用户名>`，并通过标准输入提供符合上述策略的 UTF-8 无 BOM 新密码。Windows 使用 `sub-pool.exe`，源码运行可用 `go -C backend run ./cmd/sub-pool reset-password <用户名>`。成功后，该管理员用新密码登录，原有会话和 API Key 同时撤销，操作写入审计。升级后固定初始密码已失效的管理员，以及原本待改密的管理员，恢复后仍需在浏览器中改密。

已有管理员可在「用户与权限」创建管理员或变更角色，由操作者输入本人当前密码确认。角色变更后，目标用户需要重新登录并签发需要的 API Key。交接时先确认另一名管理员已经启用。

## HTTP 接口

`GET /health` 可用于探测 Go 进程。`/api/v1` 各领域的请求字段、响应、版本和客户端实现以 TypeScript 契约为准。Go 的路由入口在 [cmd/sub-pool](cmd/sub-pool/main.go)。

| 领域 | 契约 |
| --- | --- |
| 登录、会话、用户权限 | [认证](../contracts/auth.ts)、[用户与访问范围](../contracts/users.ts)、[来源规则与审计](../contracts/security.ts) |
| 上游连接与账号 | [连接和时区](../contracts/connection.ts)、[账号](../contracts/accounts.ts)、[采集配置与记录](../contracts/monitoring.ts) |
| 参与关系与用量 | [参与者](../contracts/participants.ts)、[额度池](../contracts/pools.ts)、[已记录消费](../contracts/usage.ts) |
| 观测、估算与钱包 | [观测](../contracts/observations.ts)、[估算](../contracts/estimates.ts)、[钱包](../contracts/wallets.ts)、[应用记录](../contracts/wallet-applications.ts) |
| 上游控制与通知 | [临时放宽](../contracts/temporary.ts)、[计价](../contracts/pricing.ts)、[调度](../contracts/dispatch.ts)、[通知](../contracts/notifications.ts) |
| 运维与帮助 | [API Key](../contracts/api-keys.ts)、[历史维护](../contracts/maintenance.ts)、[业务备份](../contracts/backups.ts)、[帮助与公告](../contracts/help.ts) |

浏览器通过 HttpOnly 会话 Cookie 鉴权。POST 请求使用 `application/json` 和精确匹配的 `Origin`。普通用户同时受功能权限和相应的账号或参与者范围约束。服务端在读取事实和执行写入时检查当前授权。

`must_change_password: true` 的会话只能查询本次会话、改密或退出。续期和受保护接口返回 `403 PASSWORD_CHANGE_REQUIRED`。成功改密后，客户端需要重新登录取得授权。

### 来源 IP 阻断

管理员在「登录与审计 → 来源阻断」添加精确 IPv4 或 IPv6。`POST /api/v1/security/sources/update` 接收 `ip`、`revision`、`blocked`。添加阻断时可附带最多 256 个字符的 `reason`，以及精确到微秒的 UTC `expires_at`。省略期限表示永久。查询和更新回执中的空原因或空期限以 `null` 表示。

限时规则到期后，服务端按 PostgreSQL 时间放行。后台在当前状态上提交解除并记录 `security.source.expire` 系统审计。停机期间到期的规则会在启动后补记。

### API Key

在控制台的 **API Key** 页面签发和轮换个人 Key。明文只在创建或轮换时显示，轮换立即使旧 Key 失效。停用账号、变更密码等凭据事件会撤销关联 Key。

机器客户端通过可信 HTTPS 入口直接访问 Go，发送一个 `Authorization: Bearer <Key>` 请求头。凭据随请求单独传递，省略浏览器 Cookie。Next 的同源接口供浏览器会话使用。原生部署可通过同一个 HTTPS 入口分别转发机器读取和浏览器会话。 [Caddy 部署示例](../README.md#通过-https-访问机器接口)

1. 调用 `GET /api/v1/api-keys/access` 查看当前身份、授权和范围。
2. 调用 `GET /api/v1/api-keys/catalog` 查看该 Key 可用的路径、方法、版本与能力限制。
3. 按接口契约发送请求。只有管理员可以签发 `management` Key。每次调用仍受目录和当前授权约束。

遇到超时、断链或版本冲突，先读取原请求对应的记录并核对持久状态。等操作结果和当前依据明确后再决定下一步。

## 上游连接与能力

在控制台提交 Sub2API 的 HTTPS 地址和管理凭据。地址可以有基础路径。凭据可填管理 API Key，也可以填以 `Bearer ` 开头的管理员 JWT。连接检查分别记录健康、鉴权及各项能力。保存新地址会建立新的来源，历史仍按原来源保留。

连接检查从上游读取分组计价详情和已纳管账号的被动额度快照。证据显示对应对象和时间。原版 Sub2API 0.2.23 支持用管理 API Key 验证运行模式，受限 Key 需要 `system.read` 和 `models.defaults.read`。管理员 JWT 可通过个人资料接口读取运行模式。余额、调度和计价的写入状态取自当前连接与版本下的写入和独立读回回执。测试连接本身只读取上游。

使用受限 Key 调整余额还需要 `users.read`、`users.balance.read` 和 `users.balance.write`。原版 0.2.23 的受限 Key 会裁剪账号身份字段，分组更新仅开放模型白名单。使用管理 Key 时，调度和计价写入需要全权兼容模式。

- **采集与份额。** 被动采集保存来源、采集时间和真实读数。用量和份额历史按当时的参与者绑定与契约归属。数据缺失时保留未知状态。
- **钱包与临时放宽。** 在「余额建议与应用」读取已绑定用户的钱包后，管理员可通过 `standard` 模式下的原生余额接口提交单笔增减额。Sub-Pool 保存请求 ID 并独立核对上游流水和余额，未决记录可在页面只读核对。增加余额遵循 Sub2API 的管理员充值返利设置。金额建议、容量和临时权益依赖完整且可验证的业务依据。
- **生产写入授权。** 默认关闭计价、整账号调度和钱包写入。管理员在「连接与采集 → 能力检查」用本人密码逐项授权或撤销，生效无需重启。同一地址轮换凭据保留授权，改用新地址需要重新授权。升级后请在页面为当前来源重新授权，原 `SUB_POOL_PRICING_CONTROL_BINDING` 和 `SUB_POOL_SCHEDULING_CONTROL_BINDING` 已停用。
- **紧急禁写。** 以 `SUB_POOL_DISABLE_UPSTREAM_WRITES=true` 启动 `serve`，暂停实例的生产写入派发。已发送请求仍可只读核对，已保存授权仍可管理。
- **模拟协议。** `SUB_POOL_SIMULATION_BINDING=<source_id>:<connection_revision>:<simulator_epoch>` 供隔离验收使用。它要求启用回环 HTTP、连接专用回环模拟器并使用匹配的来源、版本和 epoch。模拟凭据和回执只用于该协议。
- **邮件。** 可配置 SMTP 或 Resend。通知种类、发送资格与去重分别检查。通过通知记录区分已发送、失败和结果未知。

## 业务数据运维

### 备份与恢复

在 **备份与恢复** 导出加密的 `.spbak` 文件。它保存 Sub-Pool 自身的业务表，恢复要求匹配的数据库结构、迁移历史及 `SUB_POOL_SECRET_KEY`。密钥和备份应分别存放在数据库之外。需要迁移整套服务时也要保留数据库备份、部署配置和 TLS 配置。

迁移 038 的完整及轻量档都保存来源阻断原因和期限。可恢复结构与迁移摘要匹配的 035、036 旧档和 037 流式档，其中的旧阻断保留原状态，期限与原因为空。

1. 上传要恢复的文件并执行预检。预检会校验内容、结构、管理员及未完成的业务操作。
2. 下载并保存预检生成的恢复前备份。在预检有效期间确认恢复。恢复前的数据发生变化时重新预检。
3. 使用备份中的管理员重新登录，读取恢复记录并检查当前 Sub2API 连接。旧会话会被清除，导入的 API Key 和写入授权会被撤销，采集与邮件暂停，需要管理员重新确认相应设置。

### 历史维护

在 **历史维护** 选择账号和时间范围，查看预检差异并重新读取有效计划后应用。维护结果由本地已保留的观测和估算生成，原采集记录继续保留。使用计划 ID 查看排队、执行与最终结果。

## 测试

准备可丢弃的回环 PostgreSQL 数据库，名称以 `subpool_test_` 开头。设置 `SUB_POOL_TEST_DATABASE_URL`，确保测试用户有权创建和清理独立 schema。未设置时集成测试会明确跳过。数据库级锁要求串行运行各包的测试。

```sh
go -C backend test -p 1 ./...
go -C backend vet ./...
```

契约测试、构建和容器检查的命令见 [Build and publish 工作流](../.github/workflows/build.yml)。涉及上游、钱包或会话写入的测试使用单独的隔离环境。
