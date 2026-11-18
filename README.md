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

数据库没有管理员时，服务启动后会创建默认管理员，用户名和密码均为 `admin`。已有管理员继续使用原账号和密码。登录后可在 **个人设置** 修改初始密码。

1. 在 **连接与采集** 保存 Sub2API 地址和管理凭据，运行已保存连接的检查。健康、鉴权与各项能力分别显示检查结果。
2. 纳管需要观察的账号。在 **账号与额度** 查看最近成功采集、上游时间和后续失败记录。
3. 在 **参与者与额度池** 绑定参与者并维护份额。在 **用户与权限** 为普通用户授予需要的功能和数据范围。

额度池和份额保存在 Sub-Pool。用量页面展示已采集的记录，缺失数据会保留为未知。钱包、费用估算及上游操作依各自的能力检查开放。

## 维护部署

| 配置 | 用途 |
| --- | --- |
| `SUB_POOL_PUBLIC_ORIGIN` | 浏览器实际访问的 origin，默认 `http://127.0.0.1:3000`。填写协议、主机和必要的端口即可。 |
| `SUB_POOL_BIND` | Compose 在宿主机的监听地址，默认 `127.0.0.1`。 |
| `SUB_POOL_HOST` | 原生包的前端监听地址，默认 `127.0.0.1`。 |
| `SUB_POOL_PORT` | 两种部署的前端访问端口，默认 `3000`。 |
| `IMAGE_TAG` | Compose 镜像版本，默认 `main`。可使用发布标签或 `sha-<完整提交 SHA>` 固定版本。 |

- **公网访问。** 在前端前配置 HTTPS 反向代理，并将 `SUB_POOL_PUBLIC_ORIGIN` 改为实际访问的 HTTPS origin。仅更换本地访问端口时也要同步修改 origin。前后端使用内部回环 HTTP，保留这一连接的 `SUB_POOL_ALLOW_LOOPBACK_HTTP=true`。
- **升级。** 先备份 Sub-Pool 数据库和 `SUB_POOL_SECRET_KEY`，再更新镜像标签并重新执行拉取、启动命令。应用启动时检查并应用迁移。
- **数据持久化。** `docker compose down` 停止服务并保留数据库卷。已有卷继续使用原数据库密码，密码轮换需要在 PostgreSQL 中同步更新。
- **健康检查。** 前端的 `/health` 检查到 Go 服务的连通性。Sub2API 的状态在 **连接与采集** 页面检查。

## 项目文档

- [后端开发、HTTP 接口与业务数据](https://github.com/DSLZL/Sub-Pool/blob/main/backend/README.md)
- [前端开发、控制台路由与 HTTPS 入口](https://github.com/DSLZL/Sub-Pool/blob/main/web/README.md)
- [Sub2API 资金一致性补丁](https://github.com/DSLZL/Sub-Pool/blob/main/integrations/sub2api/README.md)
