# Sub-Pool

Sub-Pool 使用 Go、PostgreSQL 和 Next.js 管理 Sub2API 连接及相关业务。浏览器只访问前端，前端通过固定的本机地址转发到 Go；数据库和上游凭据由 Go 管理。

## Docker Compose 部署

安装 Docker Engine / Docker Desktop 和 Docker Compose v2，下载本仓库的 `compose.yaml`、`.env.example`。容器镜像支持 Linux amd64、arm64，包含完整前后端。

1. 复制 `.env.example` 为 `.env`，分别生成数据库密码和加密密钥并填入文件：

   ```sh
   node -e "console.log(require('node:crypto').randomBytes(32).toString('hex'))"
   node -e "console.log(require('node:crypto').randomBytes(32).toString('base64'))"
   ```

   第一行输出填入 `POSTGRES_PASSWORD`，第二行填入 `SUB_POOL_SECRET_KEY`。没有 Node.js 时可使用 `openssl rand -hex 32`、`openssl rand -base64 32`。密钥必须长期保留，并与数据库备份配套保存。不要提交 `.env`。

2. 拉取并启动：

   ```sh
   docker compose pull
   docker compose up -d --wait
   ```

3. 创建首个管理员，没有默认账号或密码。将符合要求的密码写入仅自己可读的临时文件 `admin-password.txt`，内容为至少十个字符，包含英文字母和数字，使用 UTF-8 无 BOM；创建后删除该文件。

   macOS / Linux：

   ```sh
   docker compose exec -T app /app/sub-pool bootstrap admin < admin-password.txt
   ```

   Windows PowerShell：

   ```powershell
   $OutputEncoding = [System.Text.UTF8Encoding]::new($false)
   (Get-Content -Raw .\admin-password.txt).TrimEnd("`r", "`n") | docker compose exec -T app /app/sub-pool bootstrap admin
   ```

4. 打开 <http://127.0.0.1:3000>，使用该管理员登录并配置 Sub2API。数据库使用独立的 `sub_pool` 数据库，不能指向 Sub2API 的数据库。

`docker compose down` 停止服务并保留数据卷；`docker compose down -v` 会删除数据库。升级前备份数据库和密钥，选择镜像标签后重新执行 pull / up；启动会自动运行迁移。已有数据卷的数据库密码不会因修改 `.env` 自动变化。

从源码本地构建使用 `docker compose build`，再执行 `docker compose up -d --no-build --wait`。应用以非 root 用户运行，只对外映射前端端口，Go 和 PostgreSQL 均不发布端口。

### 配置与 HTTPS

| 配置 | 用途 |
| --- | --- |
| `POSTGRES_PASSWORD` | 独立数据库密码，使用随机十六进制值以适合数据库 URL |
| `SUB_POOL_SECRET_KEY` | 固定的 base64 编码 32 字节加密密钥 |
| `IMAGE_TAG` | 默认 `main`；可固定为 `sha-<完整提交 SHA>` |
| `SUB_POOL_PUBLIC_ORIGIN` | 浏览器实际访问的完整 origin，无末尾斜杠 |
| `SUB_POOL_BIND` / `SUB_POOL_PORT` | 宿主机监听地址和端口，默认 `127.0.0.1:3000` |

公网部署请在前端前配置 HTTPS 反向代理，将 `SUB_POOL_PUBLIC_ORIGIN` 改为实际的 `https://域名`；按代理位置调整宿主机监听地址。仅修改端口时也必须同步修改 public origin。不要将本地 HTTP 示例直接作为公网入口。

前后端之间仍使用容器内部的 `127.0.0.1:8081`，因此 `SUB_POOL_ALLOW_LOOPBACK_HTTP=true` 必须保留；它仅放行 loopback HTTP，公网 origin 使用 HTTPS 时仍执行 Secure Cookie 和精确 Origin 校验。默认不信任客户端转发的来源 IP；需要来源封禁或审计时，按[来源代理部署说明](https://github.com/DSLZL/Sub-Pool/blob/main/web/README.md)配置可信 HTTPS 入口，不能直接透传浏览器的 X-Forwarded-For。

控制台目录为 `web/app/console`，页面统一使用 `/console` 前缀；登录为 `/login`。根地址及原来的控制台地址会跳转到新地址，并保留查询参数。

## 六平台运行包

[Build and publish 工作流](https://github.com/DSLZL/Sub-Pool/actions/workflows/build.yml)生成以下完整前后端包，每个平台使用原生 runner 构建，并带 SHA-256 校验文件：

| 系统 | 架构 | 产物 |
| --- | --- | --- |
| Linux | amd64 / arm64 | `sub-pool-linux-<arch>.tar.gz` |
| macOS | amd64 / arm64 | `sub-pool-darwin-<arch>.tar.gz` |
| Windows | amd64 / arm64 | `sub-pool-windows-<arch>.tar.gz` |

在成功运行的 Artifacts 下载对应包，解压到新目录。安装匹配系统和架构的 **Node.js 26 LTS**，准备独立 PostgreSQL 18 数据库；不需要安装 Go、Bun 或重新安装前端依赖。Linux 包面向 glibc 发行版；Alpine 等 musl 环境请使用容器或自行构建。

复制 `.env.example` 为 `.env`，填入 `SUB_POOL_DATABASE_URL`、`SUB_POOL_SECRET_KEY` 和 public origin。先执行迁移：

```sh
node --env-file=.env start.mjs migrate
```

用 `node --env-file=.env start.mjs bootstrap admin` 创建管理员，从标准输入提供上述临时密码文件（macOS/Linux 可追加 `< admin-password.txt`；PowerShell 使用前面的 UTF-8 管道写法）。然后启动：

```sh
node --env-file=.env start.mjs
```

启动入口同时管理 Go 与 Next，任一进程异常退出会停止另一进程并返回失败；Linux/macOS/容器的 SIGTERM/SIGINT 会转发停止信号。Windows 控制台可用 Ctrl+C 停止。`SUB_POOL_HOST` / `SUB_POOL_PORT` 设置原生包的前端监听，默认 `127.0.0.1:3000`，Go 固定监听 `127.0.0.1:8081`。

## 自动构建与镜像

提交到 `main`、推送 `v*` 标签、Pull Request 和手动运行会触发工作流。Go 使用 `actions/setup-go` 缓存模块和编译结果，缓存依赖键为 `backend/go.sum`；镜像使用按架构隔离的 BuildKit 缓存。

主线成功后发布 `ghcr.io/dslzl/sub-pool:main`、`:latest` 和按提交标记的 `:sha-<完整提交 SHA>` 多架构镜像；版本标签额外生成对应镜像标签。平台 SHA 镜像先通过独立数据库的迁移、管理员创建、同源登录、重启持久化与静态资源检查，再合并 manifest。Pull Request 不登录或推送 GHCR；发布使用仓库的 `GITHUB_TOKEN` 与 `packages: write`，无需配置个人访问令牌。

本流程不会自动创建版本标签或 GitHub Release。构建、隔离数据库测试及模拟部署通过不代表所有真实 Sub2API 资金能力已取得生产资格；具体功能仍受现有后端门禁限制。
