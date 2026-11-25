# Sub-Pool 前端

Next.js 提供登录页、控制台和同源 API。当前身份、权限、业务数据及操作回执来自 Go 服务。浏览器连接前端地址，前端按固定路由访问后端。

## 本地开发

先按[后端说明](../backend/README.md#从源码运行)准备数据库并启动 Go。使用 Node.js 26 和 Bun，从仓库根目录安装工作区依赖。

```sh
bun install --frozen-lockfile
```

在启动 Next 前，将以下内容写入 `web/.env.local` 或设置到进程环境。Go 使用相同的 `SUB_POOL_PUBLIC_ORIGIN` 和回环 HTTP 设置。

```dotenv
SUB_POOL_PUBLIC_ORIGIN=http://127.0.0.1:3000
SUB_POOL_BACKEND_ORIGIN=http://127.0.0.1:8081
SUB_POOL_ALLOW_LOOPBACK_HTTP=true
```

从仓库根目录启动 Next。

```sh
bun run --cwd web dev
```

打开 <http://127.0.0.1:3000>。地址应与 origin 配置一致。

### 检查与构建

在仓库根目录执行。

```sh
bun run --cwd contracts typecheck
bun run --cwd contracts test
bun run --cwd web typecheck
bun run --cwd web test
bun run --cwd web build
```

分发用的独立 Next 构建随部署产物提供。 [部署说明](../README.md)

## 控制台页面

| 路径 | 用途 |
| --- | --- |
| `/login`、`/change-password`、`/console/profile` | 登录、首次改密与个人设置 |
| `/console` | 概览、服务健康和待关注事项 |
| `/console/connection` | 连接测试、账号纳管、采集配置与时区 |
| `/console/accounts` | 查看账号状态、额度窗口，发起采集并读取执行记录 |
| `/console/pools`、`/console/usage` | 查看参与者、份额和记录消费及历史 |
| `/console/observations`、`/console/estimates` | 查看额度观测、依据、估算与历史 |
| `/console/balances`、`/console/temporary` | 查看钱包、调整记录与临时权益状态 |
| `/console/pricing`、`/console/dispatch` | 查看上游计价、调度状态及操作记录 |
| `/console/notifications` | 邮件渠道、通知策略与发送记录 |
| `/console/users`、`/console/security`、`/console/api-keys` | 用户授权、来源规则、审计与个人 API Key |
| `/console/maintenance`、`/console/backup` | 维护计划、业务备份与恢复 |
| `/console/help` | 使用指南与公告 |

管理入口按当前会话和服务端授权开放。页面按各领域的契约显示未知数据、读取失败、冲突和结果未知。写入状态以服务端回执或原记录读回为准。条件能力的设置见[后端说明](../backend/README.md#上游连接与能力)。

余额页按参与者翻页。份额历史、钱包读数与建议历史、调整记录和临时轮次在各自页面使用「下一页」。额度池合同可包含 10,000 名成员。金融证据不足或超过容量时，页面显示原因和未知金额。

## 代码入口

- [app/console](app/console) 和 [app/login](app/login) 包含业务页面和登录页。
- [app/api/v1](app/api/v1) 与 [lib/api-proxy.ts](lib/api-proxy.ts) 处理同源 API。请求路径和方法由服务端决定。
- [lib/store.tsx](lib/store.tsx) 管理会话、主题和反馈。[components](components) 提供共享界面组件。
- [contracts](../contracts) 供前端与机器客户端使用，包含请求和响应校验。

## HTTPS 入口及来源 IP

公网访问由反向代理终止 TLS，浏览器请求转发到 Next。Go 和 Next 的 `SUB_POOL_PUBLIC_ORIGIN` 都使用浏览器的 HTTPS origin。Next 通过固定私有地址连接 Go。需要在同一个入口提供机器读取时，可用显式路径和方法的 Caddy 配置。 [机器访问示例](../README.md#通过-https-访问机器接口)

需要在控制台上传业务备份时，将反代请求体限制设为至少 16 MiB，以容纳恢复预检的 JSON 请求。

默认情况下，Go 在控制台接口中看到的是 Next 的直接连接地址。登录审计和来源阻断需要终端 IP 时，同时配置以下两段转发。

1. 在 Next 的进程环境设置 `SUB_POOL_SOURCE_PROXY_KEY`，使用 32 字节随机值的 64 位小写十六进制编码。将相同密钥配置到可信入口，放在服务端秘密配置中。
2. 在入口移除客户端发送的 `X-Forwarded-For`、`X-Sub-Pool-Proxy-Key` 和 `X-Sub-Pool-Client-IP`。重新设置 `X-Sub-Pool-Proxy-Key` 为共享密钥、`X-Sub-Pool-Client-IP` 为入口核实的单个 IPv4 或 IPv6 地址。
3. 给 Go 配置 `SUB_POOL_TRUSTED_PROXY_CIDRS`，仅信任实际连接 Go 的 Next 或私有代理地址。使用默认运行包或 Compose 的同容器回环连接时，该地址为 `127.0.0.1/32`。
4. 通过不同来源登录并检查审计。尝试从入口传入伪造头，确认入口会覆盖这些头。将 Next 和 Go 保持在仅可信入口可达的网络范围内。

Next 验证密钥和单个客户端地址，并向 Go 写入 `X-Forwarded-For`。代理身份头不向 Go 转发。启用来源验证后，缺失或错误的头会使同源 API 返回 `403`。轮换密钥时同步更新入口代理和 Next。

使用 Compose 时，需把这两个变量显式传入 `app` 的环境。可以在部署目录的 `compose.override.yaml` 加入以下配置，再在部署环境提供变量的值。

```yaml
services:
  app:
    environment:
      SUB_POOL_SOURCE_PROXY_KEY: ${SUB_POOL_SOURCE_PROXY_KEY:?Set SUB_POOL_SOURCE_PROXY_KEY}
      SUB_POOL_TRUSTED_PROXY_CIDRS: ${SUB_POOL_TRUSTED_PROXY_CIDRS:?Set SUB_POOL_TRUSTED_PROXY_CIDRS}
```

## 合成演示

在独立的开发环境中可以启动演示服务。它返回合成场景，运行时独立于业务数据库和 Sub2API。

```sh
go -C backend run ./cmd/sub-pool-demo
```

本地 Next 设置 `SUB_POOL_DEMO_ORIGIN=http://127.0.0.1:8082` 后访问 `/demo`。保留开发环境的回环 HTTP 设置。演示访问单独的固定接口，场景操作不会写入业务记录。
