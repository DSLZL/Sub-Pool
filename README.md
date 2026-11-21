# Sub-Pool

Sub-Pool 是面向 Sub2API 的独立管理控制台。管理员可以纳管账号、查看额度和已记录的消费、维护本地额度池与份额，并为普通用户指定可读功能、账号范围和参与者范围。

## Docker Compose 部署

准备 Docker Engine 或 Docker Desktop，以及 Docker Compose v2。将下面两个文件放在同一个目录。镜像包含前后端，支持 Linux amd64 和 arm64，Compose 会一并启动 PostgreSQL 18。

- [下载 compose.yaml](https://raw.githubusercontent.com/DSLZL/Sub-Pool/main/compose.yaml)
- [下载 .env.example](https://raw.githubusercontent.com/DSLZL/Sub-Pool/main/.env.example)

1. 复制 `.env.example` 为 `.env`，把带用户名和密码的完整 PostgreSQL URI 填入 `SUB_POOL_DATABASE_URL`。使用内置 PostgreSQL 时，替换示例中的 `YOUR_PASSWORD`。

   ```dotenv
   SUB_POOL_DATABASE_URL='postgres://sub_pool:YOUR_PASSWORD@postgres:5432/sub_pool?sslmode=disable'
   ```

   密码包含 URI 分隔符时，按 URI 规则编码。例如 `@` 可写为 `%40`。
2. 为 `SUB_POOL_SECRET_KEY` 生成一个 32 字节随机密钥并填写其 Base64 值。可以使用 `openssl rand -base64 32`，或运行下面的 Node.js 命令。生成后保留同一密钥。

   ```sh
   node -e "console.log(require('node:crypto').randomBytes(32).toString('base64'))"
   ```

3. 在该目录启动服务。

   ```sh
   docker compose pull
   docker compose up -d --no-build --wait
   ```

4. 打开 <http://127.0.0.1:3000>，[登录控制台](#首次登录)。

使用已有的 PostgreSQL 时，填入从容器内可访问的完整 URI，只启动 `app`。

```sh
docker compose pull app
docker compose up -d --no-deps --no-build --wait app
```

## 原生运行包

在 [Build and publish](https://github.com/DSLZL/Sub-Pool/actions/workflows/build.yml) 的成功运行中下载对应的构建产物。文件名格式为 `sub-pool-<系统>-<架构>.tar.gz`，系统取 `linux`、`darwin` 或 `windows`，架构取 `amd64` 或 `arm64`。用同包的 `.sha256` 文件核对归档哈希后解压。Linux 包面向 glibc 环境。

1. 安装 Node.js 26，准备专供 Sub-Pool 使用的 PostgreSQL 18 数据库。
2. 在解压目录复制 `.env.example` 为 `.env`。填写完整的 `SUB_POOL_DATABASE_URL`，例如 `postgres://sub_pool:YOUR_PASSWORD@127.0.0.1:5432/sub_pool?sslmode=disable`。按上面的方式生成并填写 `SUB_POOL_SECRET_KEY`。
3. 执行 `node --env-file=.env start.mjs` 启动前后端，打开 <http://127.0.0.1:3000> 并[登录](#首次登录)。在前台运行时可以用 Ctrl+C 停止。

## 首次登录

数据库没有管理员时，服务会创建 `admin`。默认生成 20 位随机初始密码，在创建成功时通过一条 `initial_admin_password` 启动记录交付。Compose 部署可在服务器上运行 `docker compose logs app` 查看该记录，原生包可在首次启动的终端查看。该行含明文密码，请勿转发。

需要预设初始密码时，可在 `.env` 中填写 `SUB_POOL_INITIAL_ADMIN_PASSWORD`。密码须至少 10 个字符，包含 ASCII 字母和数字，上限为 128 个 UTF-8 字节。预设密码不会写入启动记录。首次登录后须修改密码，再用新密码进入控制台。

升级时，仍使用旧固定初始密码的账户会失去该密码及关联会话和 API Key。原生部署用 `sub-pool reset-password <用户名>` 恢复仍启用的管理员，Windows 使用 `sub-pool.exe`。Compose 部署用 `docker compose exec -T app ./sub-pool reset-password <用户名>`。将新密码以 UTF-8 无 BOM 格式通过标准输入传入，然后登录并完成改密。初始启动记录遗失时也可用此方式恢复。

1. 在 **连接与采集** 保存 Sub2API 地址和管理凭据，运行已保存连接的检查。健康、鉴权与各项能力分别显示检查结果。
2. 纳管需要观察的账号。在 **账号与额度** 查看最近成功采集、上游时间和后续失败记录。
3. 在 **参与者与额度池** 绑定参与者并维护份额。在 **用户与权限** 为普通用户授予需要的功能和数据范围。

额度池和份额保存在 Sub-Pool。用量页面展示已采集的记录，缺失数据会保留为未知。钱包及上游操作依各自的能力检查开放。

## 七天额度估算

为 OpenAI OAuth 账号开启被动监控后，**容量估算**会从该账号的原生额度重置起点累计 Sub2API 使用记录。连接凭据需要读取原生账号和使用记录。

截至每次有效额度观测的累计标准费用，除以同一次观测的已用比例，得到七天总量估值。新周期从新的重置起点重新累计。页面显示累计费用、已用比例、估算依据和采集进度。

已用比例达到 20%，且最近三次不同已用比例对应的总量估值最大差距在最新估值的 10% 以内时，系统自动按当前池合同的百分比分别计算总量份额和剩余份额。金额为 Sub2API 已记录用量的标准美元等值。

## 周额度发放

在 **余额建议与应用 → 当前建议 → 周额度建议** 中查看份额、正式应得、扣除项、建议余额和本次增减额，确认后可手动应用。在 **自动应用 → 周额度设置** 中设置所属池的自动应用，也可以从额度池打开同一套发放设置。

额度池须只纳管一个被动监控的 OpenAI OAuth 账号，每位参与者使用本池专用的 Sub2API 钱包。连接须能读取和写入原版标准模式的钱包，发放须使用不会触发管理员充值返佣的配置。两个选项独立设置，默认关闭。

- **开启半额预发。** 识别到原生周限的新重置周期后，按该成员上次已确认正式额度的一半预发，并先抵扣期初欠额。
- **估算达标后自动应用正式分配。** 钱包单位的累计估算达标后，按当前份额结算。原版的余额和订阅计费记录均使用实际费用。余额为本周应得额度减去预发期间已用量和期初欠额，差额不足时将余额调至零并结转欠额。首次正式分配为之后的预发建立记录。

弹窗展示最近的预发、正式结算、欠额和调账记录。若原生调账结果待确认，在 **余额建议与应用** 核对对应调账记录。

## 维护部署

| 配置 | 用途 |
| --- | --- |
| `SUB_POOL_PUBLIC_ORIGIN` | 浏览器实际访问的 origin，默认 `http://127.0.0.1:3000`。填写协议、主机和必要的端口即可。 |
| `SUB_POOL_BIND` | Compose 在宿主机的监听地址，默认 `127.0.0.1`。 |
| `SUB_POOL_HOST` | 原生包的前端监听地址，默认 `127.0.0.1`。 |
| `SUB_POOL_PORT` | 两种部署的前端访问端口，默认 `3000`。 |
| `SUB_POOL_LOG_LEVEL` | Go JSON 日志级别，默认 `info`。可用值为 `debug`、`info`、`warn`、`error`。 |
| `IMAGE_TAG` | Compose 镜像版本，默认 `main`。可使用发布标签或 `sha-<完整提交 SHA>` 固定版本。 |

- **公网访问。** 在前端前配置 HTTPS 反向代理，并将 `SUB_POOL_PUBLIC_ORIGIN` 改为实际访问的 HTTPS origin。仅更换本地访问端口时也要同步修改 origin。前后端使用内部回环 HTTP，保留这一连接的 `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`。
- **升级。** 先备份 Sub-Pool 数据库和 `SUB_POOL_SECRET_KEY`，再更新镜像标签并重新执行拉取、启动命令。应用启动时检查并应用迁移。
- **数据持久化。** `docker compose down` 停止服务并保留数据库卷。已有卷继续使用原数据库密码，密码轮换需要在 PostgreSQL 中同步更新。
- **健康检查。** `/health` 检查 Go 进程存活。`/health/ready` 核对数据库迁移和后台循环最近一分钟的心跳，未就绪时返回 HTTP 503。Sub2API 状态可在 **连接与采集** 检查。
- **日志。** Go 常规 JSON 日志写到标准错误，生成初始密码的专用启动记录写到标准输出。页面报错时提供的请求编号对应日志的 `request_id`。数据库和上游错误记录分类、状态和安全诊断字段。需要查看后台循环的开始、完成与跳过详情时，可临时使用 `debug`。

## 项目文档

- [后端开发、HTTP 接口与业务数据](https://github.com/DSLZL/Sub-Pool/blob/main/backend/README.md)
- [前端开发、控制台路由与 HTTPS 入口](https://github.com/DSLZL/Sub-Pool/blob/main/web/README.md)
