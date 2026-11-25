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

在 **余额建议与调整 → 当前建议 → 周额度建议** 点击计算份额、正式应得、扣除项、建议余额和本次增减额。具备钱包写入资格时，可在结果弹窗确认并点击 **应用建议余额** 手动结算。在 **自动调整 → 周额度设置** 中设置所属池的自动发放，也可以从额度池打开发放设置。

等待正式估算时，弹窗可用同周期已核对的实际费用展示参考周额度、依据时间和样本进度。参考周额度表示按合同份额预计应得的金额。跨账号结果在所需贡献齐备时展示合计。

额度池可纳管多个被动监控的 OpenAI OAuth 账号。每项贡献按该账号的原生周周期、当前合同份额及已核对的用量计算。参与者跨池时，各项贡献会汇总到同一个绑定钱包。缺少所需账号的依据时，请核对建议中列出的原因。计算建议只需读取依据和钱包余额。发放还需要有效的钱包写入授权、可读可写的原版标准模式钱包，以及不会触发管理员充值返佣的配置。两个自动选项独立设置，默认关闭。

- **开启半额预发。** 识别到原生周限的新重置周期后，按该成员上次已确认正式额度的一半预发，并先抵扣期初欠额。
- **估算达标后自动应用正式分配。** 钱包单位的累计估算达标后，按当前份额结算。原版的余额和订阅计费记录均使用实际费用。余额为本周应得额度减去预发期间已用量和期初欠额，差额不足时将余额调至零并结转欠额。首次正式分配为之后的预发建立记录。

弹窗展示最近的预发、正式结算、欠额和调整记录。若原生调整结果待确认，在 **余额建议与调整** 核对对应调整记录。

## Sub2API 兼容矩阵

| 上游版本 | 管理 Key | 管理员 JWT |
| --- | --- | --- |
| 原版 `0.2.23` | 使用 `system.read` 读取版本，使用 `models.defaults.read` 核对标准或简易模式。 | 通过 `GET /api/v1/auth/me` 读取管理员的实际运行模式。 |
| 其他版本 | 连接诊断显示版本未经验证，并提示使用管理员 JWT 读取模式。 | 运行模式按管理员资料的实际响应显示，同时保留版本未经验证提示。 |

本期生产兼容范围为原版 `0.2.23`。原版隔离验收使用官方 `sub2api_0.2.23_windows_amd64.zip`。写入能力依赖当前来源权限、操作门禁和独立读回证据。[Sub2API 官方发布](https://github.com/Wei-Shaw/sub2api/releases/tag/v0.2.23)

## 维护部署

| 配置 | 用途 |
| --- | --- |
| `SUB_POOL_PUBLIC_ORIGIN` | 浏览器实际访问的 origin，默认 `http://127.0.0.1:3000`。填写协议、主机和必要的端口即可。 |
| `SUB_POOL_BIND` | Compose 在宿主机的监听地址，默认 `127.0.0.1`。 |
| `SUB_POOL_HOST` | 原生包的前端监听地址，默认 `127.0.0.1`。 |
| `SUB_POOL_PORT` | 两种部署的前端访问端口，默认 `3000`。 |
| `SUB_POOL_LOG_LEVEL` | Go JSON 日志级别，默认 `info`。可用值为 `debug`、`info`、`warn`、`error`。 |
| `SUB_POOL_DB_MAX_CONNS` | Go 请求连接池上限，默认 `25`，使用正整数。后台池上限为 `8`，包含运行期锁连接。部署按两池每实例合计 `33` 的默认预算及其他数据库客户端预留连接。 |
| `SUB_POOL_UPSTREAM_READ_TIMEOUT` | Sub2API 一次 GET 的总预算，默认 `15s`。包含响应体、重试和退避，受调用方截止时间约束。 |
| `SUB_POOL_UPSTREAM_CONNECT_TIMEOUT` | Sub2API 拨号及 TLS 握手的阶段上限，默认 `5s`。使用带单位的正时长，例如 `5s`。 |
| `SUB_POOL_DISABLE_UPSTREAM_WRITES` | 启动时开启紧急禁写。设为 `true` 后，暂停向上游派发计价、整账号调度及钱包写入。 |
| `SUB_POOL_BACKUP_MAX_BYTES` | 单份新保护档案的实际字节上限，默认 `2000000000`（2 GB）。 |
| `SUB_POOL_BACKUP_TIMEOUT` | 单次导出、下载、上传预检或恢复的时限，默认 `10m`。使用带单位的正时长。 |
| `IMAGE_TAG` | Compose 镜像版本，默认 `main`。可使用发布标签或 `sha-<完整提交 SHA>` 固定版本。 |

- **公网访问。** 在前端前配置 HTTPS 反向代理，并将 `SUB_POOL_PUBLIC_ORIGIN` 设为实际访问的 HTTPS origin。登录使用带 `Secure` 的主机限定 Cookie `__Host-sub_pool_session`。Next 到 Go 使用回环 HTTP 时保留 `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`。非回环 HTTP 公共 origin 会在 Go 启动时被拒绝。
- **本地开发。** 使用 `SUB_POOL_PUBLIC_ORIGIN=http://127.0.0.1:3000` 并开启 `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`。登录使用开发 Cookie `sub_pool_dev_session`，其 `Secure` 属性关闭。更换访问端口时同步修改 origin。
- **升级。** 先备份 Sub-Pool 数据库和 `SUB_POOL_SECRET_KEY`，再更新镜像标签并重新执行拉取、启动命令。应用启动时检查并应用迁移。使用旧环境变量放行写入的部署，升级后请到「连接与采集 → 能力检查」为当前来源重新授权。
- **数据持久化。** `docker compose down` 停止服务并保留数据库卷。已有卷继续使用原数据库密码，密码轮换需要在 PostgreSQL 中同步更新。
- **健康检查。** `/health` 检查 Go 进程存活。`/health/ready` 核对数据库迁移和后台循环最近一分钟的心跳，未就绪时返回 HTTP 503。Sub2API 状态可在 **连接与采集** 检查。
- **日志。** Go 常规 JSON 日志写到标准错误，生成初始密码的专用启动记录写到标准输出。页面报错时提供的请求编号对应日志的 `request_id`。数据库和上游错误记录分类、状态和安全诊断字段。需要查看后台循环的开始、完成与跳过详情时，可临时使用 `debug`。

### 通过 HTTPS 访问机器接口

同一主机上的原生部署可使用 [Caddyfile.example](Caddyfile.example) 提供一个 HTTPS 入口。Caddy 把浏览器请求转发到回环的 Next。带 `Authorization` 的请求只在匹配列出的只读方法和路径时转发到 Go。其他带凭据的路径由 Caddy 返回 404。Go 每次核对 Key、账号状态和当前授权范围。

1. 将示例中的 `subpool.example.com` 改为已指向本机的域名，保存为 `Caddyfile`。为 Caddy 的自动 HTTPS 配置公网 80 和 443 端口。 [Caddy 自动 HTTPS](https://caddyserver.com/docs/automatic-https)
2. 在原生包的 `.env` 中设置 `SUB_POOL_PUBLIC_ORIGIN=https://subpool.example.com`，使用实际域名。保留 `SUB_POOL_HOST=127.0.0.1`、`SUB_POOL_PORT=3000` 和 `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`。原生启动器将 Go 固定在 `127.0.0.1:8081`。通过公网的客户端使用 Caddy 地址。
3. 执行 `caddy validate --config Caddyfile --adapter caddyfile`，通过后用 `caddy run --config Caddyfile --adapter caddyfile` 启动 Caddy。模板使用回环来源。需要向浏览器传递经信任的客户端 IP 时，按项目的来源代理约定配置。 [来源代理说明](web/README.md)

登录控制台，在 **API Key** 签发 `read` Key。将 Key 保存在客户端的凭据配置中。机器请求发送 `Authorization: Bearer <Key>`，并省略会话 Cookie。先查询 `catalog` 确认当前授权，再读取账号。

```sh
curl --fail-with-body -H "Authorization: Bearer ${SUB_POOL_READ_API_KEY}" \
  https://subpool.example.com/api/v1/api-keys/catalog

curl --fail-with-body -H "Authorization: Bearer ${SUB_POOL_READ_API_KEY}" \
  -H "Content-Type: application/json" \
  --data '{"after":"","limit":100,"search":""}' \
  https://subpool.example.com/api/v1/accounts/list
```

此示例使用原生包的两个回环监听。Compose 的默认端口只提供 Next。

## 备份与恢复

管理员在 **备份与恢复** 导出加密的 `.spbak` 文件。完整档保存业务数据和历史。轻量档保存配置、身份、授权、绑定与池关系。轻量恢复会整体替换业务数据，预检和确认均列出将清空的表及行数。若候选档会丢失或改变已有周额度账目或对应调整记录，服务会阻止恢复。

预检通过后，先下载完整的恢复前副本并保存到本机，再确认恢复。恢复会清除会话、撤销导入的 API Key 和有效写入授权，并暂停自动任务。使用档案中的管理员重新登录，在连接页核实已保存的 Sub2API 连接。需要回退时，重新上传完整的恢复前副本并按同一流程预检。

保留与档案匹配的 `SUB_POOL_SECRET_KEY`，并分别保管。迁移 035 和 036 导出的旧 `.spbak` 可通过同一个文件入口上传。升级前保留对应软件版本及数据库备份。

为应用数据库预留空间，覆盖同时保存的加密档案、上传文件、恢复前副本、候选数据及 WAL。未决恢复上传在计划过期后于下次预检或服务启动时清理。已保存的档案及恢复前副本继续可下载。反向代理的上传上限应容纳配置的档案上限，超时应覆盖 Go 的备份时限。对大文件路径关闭整请求和整响应缓冲。

数据库级灾备可用 `pg_dump -Fc` 制作逻辑备份并用 `pg_restore` 在隔离数据库验证恢复。若需恢复到指定时间点，使用 PostgreSQL 物理基础备份和连续 WAL 归档，保留覆盖目标时间的 WAL，并定期演练恢复。 [逻辑备份](https://www.postgresql.org/docs/18/backup-dump.html) · [连续归档与时间点恢复](https://www.postgresql.org/docs/18/continuous-archiving.html)

## 浏览器验收

使用独立的本机 PostgreSQL 测试实例。将 `SUB_POOL_E2E_DATABASE_URL` 指向 `127.0.0.1` 上的 `subpool_test_` 前缀数据库，连接角色需能创建数据库。套件会为每次运行创建并清理独立数据库。若 `psql` 未加入 PATH，用 `SUB_POOL_E2E_PSQL` 指定它的路径。

```sh
bun install --frozen-lockfile --linker hoisted
node node_modules/@playwright/test/cli.js install chromium
bun run --cwd web build
bun run test:e2e
```

套件通过 Next 和 Go 访问回环 Sub2API 夹具，余额与恢复操作使用该轮测试数据。

## 项目文档

- [后端开发、HTTP 接口与业务数据](https://github.com/DSLZL/Sub-Pool/blob/main/backend/README.md)
- [前端开发、控制台路由与 HTTPS 入口](https://github.com/DSLZL/Sub-Pool/blob/main/web/README.md)
