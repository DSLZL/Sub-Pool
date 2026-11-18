# Sub-Pool 后端

Go 服务负责本地身份、访问权限和业务状态。它通过 PostgreSQL 保存配置、采集事实、份额契约与操作记录，并按当前连接配置访问 Sub2API。控制台通过 Next.js 的同源路由访问 Go。

## 从源码运行

准备 Go 1.27 和专用 PostgreSQL 数据库。部署使用的数据库账号需要能够创建和更新 Sub-Pool 的表结构。生成并保存保护密钥的方法见 [部署说明](../README.md)。

以下命令在仓库根目录运行。将数据库地址和密钥换成自己的值。

```bash
export SUB_POOL_DATABASE_URL='postgres://sub_pool:YOUR_PASSWORD@127.0.0.1:5432/sub_pool?sslmode=disable'
export SUB_POOL_SECRET_KEY='YOUR_BASE64_KEY'
export SUB_POOL_PUBLIC_ORIGIN='http://127.0.0.1:3000'
export SUB_POOL_ALLOW_LOOPBACK_HTTP=true

go -C backend run ./cmd/sub-pool migrate
go -C backend run ./cmd/sub-pool bootstrap admin < admin-password.txt
go -C backend run ./cmd/sub-pool serve
```

在 PowerShell 中用 `$env:名称 = '值'` 设置同样的进程环境。首次运行 `bootstrap` 时，按[创建管理员](../README.md#创建管理员)的要求从标准输入传入密码，将示例中的目标命令替换为 `go -C backend run ./cmd/sub-pool bootstrap admin`。之后直接运行 `serve`。启动时会应用待执行的迁移。

| 变量 | 用途 |
| --- | --- |
| `SUB_POOL_DATABASE_URL` | 必填。Sub-Pool 独立数据库的连接地址。 |
| `SUB_POOL_SECRET_KEY` | 32 字节随机密钥的 Base64 值，用于保护上游、邮件凭据及业务备份。跨重启和恢复操作保留同一密钥。 |
| `SUB_POOL_PUBLIC_ORIGIN` | 浏览器实际访问的 origin，只包含协议、主机和必要的端口。 |
| `SUB_POOL_LISTEN` | Go 的监听地址，默认 `127.0.0.1:8081`。 |
| `SUB_POOL_ALLOW_LOOPBACK_HTTP` | `true` 时允许明确指定的回环 IP 使用 HTTP。公网访问走 HTTPS。 |
| `SUB_POOL_TRUSTED_PROXY_CIDRS` | 可选。逗号分隔的可信直接代理地址段，最多 32 项。通过 Next 传递真实来源还需要配置并验证入口。 |

浏览器端的本地启动和入口配置见[前端说明](../web/README.md)。

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

### API Key

在控制台的 **API Key** 页面签发和轮换个人 Key。明文只在创建或轮换时显示，轮换立即使旧 Key 失效。停用账号、变更密码等凭据事件会撤销关联 Key。

机器客户端通过可信 HTTPS 入口直接访问 Go，发送一个 `Authorization: Bearer <Key>` 请求头。凭据随请求单独传递，省略浏览器 Cookie。Next 的同源接口供浏览器会话使用。

1. 调用 `GET /api/v1/api-keys/access` 查看当前身份、授权和范围。
2. 调用 `GET /api/v1/api-keys/catalog` 查看该 Key 可用的路径、方法、版本与能力限制。
3. 按接口契约发送请求。只有管理员可以签发 `management` Key。每次调用仍受目录和当前授权约束。

遇到超时、断链或版本冲突，先读取原请求对应的记录并核对持久状态。等操作结果和当前依据明确后再决定下一步。

## 上游连接与能力

在控制台提交 Sub2API 的 HTTPS 地址和管理凭据。地址可以有基础路径。凭据可填管理 API Key，也可以填以 `Bearer ` 开头的管理员 JWT。连接检查分别记录健康、鉴权及各项能力。保存新地址会建立新的来源，历史仍按原来源保留。

- **采集与份额。** 被动采集保存来源、采集时间和真实读数。用量和份额历史按当时的参与者绑定与契约归属。数据缺失时保留未知状态。
- **钱包与临时放宽。** 原生钱包和冻结余额分别保留上游单位、精度及确认状态。原生钱包写入请求会保存 `rejected/not_dispatched` 本地回执。金额建议、容量和临时权益依赖完整且可验证的业务依据。
- **计价与调度。** 默认关闭上游写入。受控环境可在启动 `serve` 前设置 `SUB_POOL_PRICING_CONTROL_BINDING` 或 `SUB_POOL_SCHEDULING_CONTROL_BINDING`，值为 `<source_id>:<connection_revision>`。只对已确认单一写入者的对应来源启用。上游缺少原子版本比较，其他写入者可能在读写之间改变状态。
- **模拟协议。** `SUB_POOL_SIMULATION_BINDING=<source_id>:<connection_revision>:<simulator_epoch>` 供隔离验收使用。它要求启用回环 HTTP、连接专用回环模拟器并使用匹配的来源、版本和 epoch。模拟凭据和回执只用于该协议。
- **邮件。** 可配置 SMTP 或 Resend。通知种类、发送资格与去重分别检查。通过通知记录区分已发送、失败和结果未知。

## 业务数据运维

### 备份与恢复

在 **备份与恢复** 导出加密的 `.spbak` 文件。它保存 Sub-Pool 自身的业务表，恢复要求匹配的数据库结构、迁移历史及 `SUB_POOL_SECRET_KEY`。密钥和备份应分别存放在数据库之外。需要迁移整套服务时也要保留数据库备份、部署配置和 TLS 配置。

1. 上传要恢复的文件并执行预检。预检会校验内容、结构、管理员及未完成的业务操作。
2. 下载并保存预检生成的恢复前备份。在预检有效期间确认恢复。恢复前的数据发生变化时重新预检。
3. 使用备份中的管理员重新登录，读取恢复记录并检查当前 Sub2API 连接。旧会话会被清除，导入的 API Key 会被撤销，采集与邮件暂停，需要管理员重新确认相应设置。

### 历史维护

在 **历史维护** 选择账号和时间范围，查看预检差异并重新读取有效计划后应用。维护结果由本地已保留的观测和估算生成，原采集记录继续保留。使用计划 ID 查看排队、执行与最终结果。

## 测试

准备可丢弃的回环 PostgreSQL 数据库，名称以 `subpool_test_` 开头。设置 `SUB_POOL_TEST_DATABASE_URL`，确保测试用户有权创建和清理独立 schema。未设置时集成测试会明确跳过。数据库级锁要求串行运行各包的测试。

```sh
go -C backend test -p 1 ./...
go -C backend vet ./...
```

契约测试、构建和容器检查的命令见 [Build and publish 工作流](../.github/workflows/build.yml)。涉及上游、钱包或会话写入的测试使用单独的隔离环境。
