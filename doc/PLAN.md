# RustFS 文件摆渡平台设计

本仓库包含三个 Go 程序：

- `ft-server`：与 RustFS 同机部署，验证用户、签发 token，并用服务凭据代理 S3 操作。
- `ft-client`：服务器网客户端，通过 `ft-server` 上传/下载，支持分片和断点续传。
- `ft-admin`：管理客户端，经 `ft-server` 管理接口完成用户管理、初始化和清理。

## 1. 目标

在 RustFS 的 `file-transfer` 桶中，为每个用户提供三个隔离目录：

```text
file-transfer/
└── <username>/
    ├── inbound/                    # 外网 → 内网
    ├── outbound/<YYYYMMDDNN>/      # 内网 → 外网，按上传批次组织
    └── upload/                     # 工作网 → 服务器网
```

- 服务器网只使用 `ft-client`，不直连 RustFS、不接触 S3 凭据。
- 工作网可使用 RustFS Console/Cyberduck，也可使用 `ft-client`。
- `outbound` 每次上传生成独立批次目录（`YYYYMMDD` + 序号，无分隔符，如 `2026111301`），便于操作员区分和清理。

## 2. 架构

```text
工作网
  RustFS Console / Cyberduck（用户凭据直连 RustFS）
        │
        ▼
     RustFS

服务器网
  ft-client ── HTTP ──▶ ft-server ── S3 ──▶ RustFS
                          │
                          ├─ 用户/管理员认证
                          ├─ 按路径限制 S3 操作
                          ├─ 分片上传与断点续传
                          ├─ 数据清理
                          └─ 运行日志 + 审计日志
```

- `ft-server` 与 RustFS 同机，通过 `127.0.0.1:9000` 访问。
- 客户端与服务端在同一局域网，明文 HTTP，不使用 TLS。
- 客户端不持有 S3 服务凭据；`ft-server` 使用 `ft-data`/`ft-admin` 服务身份代理。

## 3. 权限模型

| 身份 | inbound | outbound | upload |
|---|---|---|---|
| `<user>`（Console） | List、Get | List | List、Get、Put |
| `ft-data`（数据面） | List、Get | List、Put | List、Get |
| `ft-admin`（管理面） | 管理、删除 | 管理、删除 | 管理、删除 |
| `ft-operator`（操作员） | List、Put、递归 List、Delete（`.keep` 除外） | List、Get | 无 |
| `ft-client`（经 ft-server） | List、Get | List、Put | List、Get |

- 用户个人策略限制在自己的 `<username>/` 前缀内。
- `ft-data` 只写 `outbound`，只读 `inbound`/`upload`，并可为三个目录创建 `.keep` 占位。
- `ft-operator` 可浏览所有用户的 `inbound`/`outbound`，可递归列出并删除 `inbound` 内容；`inbound/.keep` 显式 Deny 删除；不能进入 `upload`。
- 普通用户 Console 策略无法在 S3 层把根目录列表过滤为“只显示自己的目录”，因此可能看到其他用户的顶层目录名，但无法读取内容。

## 4. ft-server

### 4.1 接口

除注册、登录外均需 Bearer token；管理接口需管理员 token。错误统一为 `{"error":"code","message":"..."}`。

| 方法 | 路径 | 说明 | 认证 |
|---|---|---|---|
| POST | `/v1/users` | 自助注册（用户名 + 密码） | 无 |
| POST | `/v1/sessions` | 用户登录 | 无 |
| GET | `/v1/files?dir=inbound` | 列出目录 | 用户 |
| GET | `/v1/files/{path}` | 下载文件 | 用户 |
| PUT | `/v1/files/{path}` | 上传小文件 | 用户 |
| POST | `/v1/uploads` | 初始化分片上传 | 用户 |
| PUT | `/v1/uploads/{id}/parts/{n}` | 上传分片 | 用户 |
| GET | `/v1/uploads/{id}/parts` | 查询已上传分片 | 用户 |
| PUT | `/v1/uploads/{id}` | 完成分片上传 | 用户 |
| DELETE | `/v1/uploads/{id}` | 终止分片上传 | 用户 |
| POST | `/v1/admin/login` | 管理员登录 | 无 |
| POST | `/v1/admin/bootstrap` | 初始化系统 | 管理员 |
| GET | `/v1/admin/users` | 列出用户 | 管理员 |
| POST | `/v1/admin/users` | 添加用户 | 管理员 |
| PUT | `/v1/admin/users/{username}/password` | 重置密码 | 管理员 |
| DELETE | `/v1/admin/users/{username}` | 删除用户及数据 | 管理员 |
| DELETE | `/v1/admin/data/{dir}?days=N&dry_run=true` | 清理数据 | 管理员 |

注册请求：

```json
{"username":"zhangsan","password":"Abcd1234!"}
```

密码至少 8 位，包含大写、小写、数字和特殊字符。旧客户端不传密码时，服务端仍生成随机密码并返回，保持兼容。

### 4.2 配置

`ft-server.toml` 与可执行文件同目录，也可用 `-c` 指定：

```toml
[server]
  listen = ":8080"
  log_file = "/var/log/file-transfer/ft-server.log"

[s3]
  endpoint = "http://127.0.0.1:9000"
  bucket = "file-transfer"
  region = "us-east-1"

[auth]
  token_secret = "..."
  token_ttl = "24h"
  upload_secret = "..."
  multipart_ttl = "168h"

[retention]
  inbound_days = 3
  outbound_days = 0      # 0 表示不自动清理
  upload_days = 3
  incomplete_days = 3

[multipart]
  direct_max = "100MiB"
  max_part = "512MiB"

  [[multipart.tiers]]
    max = "5GiB"
    part = "16MiB"

  [[multipart.tiers]]
    max = "50GiB"
    part = "32MiB"

  [[multipart.tiers]]
    max = "625GiB"
    part = "64MiB"

  [[multipart.tiers]]
    max = "1.25TiB"
    part = "128MiB"

[log]
  max_age_days = 30
  max_backups = 30
  max_size_mb = 100
  compress = true
```

- 大小支持 `B/KiB/MiB/GiB/TiB` 及 `K/M/G/T` 简写。
- `token_secret`/`upload_secret` 为示例默认值或空时，启动自动生成随机密钥并写回。
- `secrets/s3_credentials.json`（0600）保存 `ft-data`/`ft-admin` 凭据；管理员账号保存在 `secrets/user.json`。
- 环境变量可覆盖：`FT_SERVER_CONFIG`、`FT_S3_ENDPOINT`、`FT_BUCKET`、`FT_TOKEN_SECRET`、`FT_UPLOAD_SECRET`。

### 4.3 分片上传

| 文件大小 | 分片大小 |
|---|---|
| `< 100 MiB` | 直接上传 |
| `100 MiB – 5 GiB` | 16 MiB |
| `5 – 50 GiB` | 32 MiB |
| `50 – 625 GiB` | 64 MiB |
| `625 GiB – 1.25 TiB` | 128 MiB |
| `> 1.25 TiB` | 自动放大，最大 512 MiB |

- 服务端在登录响应中下发 `upload_policy`，客户端据此决定分片大小。
- `upload_id` 为无状态 HMAC，绑定用户名、路径、分片大小和过期时间。
- 客户端续传状态保存在 `~/.cache/file-transfer/<user>-<sha256(abs_path)>.json`（0600），只含路径、大小、mtime、`upload_id` 和分片 ETag，不含密码/token。
- 续传前查询服务端已上传分片，大小一致才跳过。
- 上传完成后删除状态文件；文件大小或 mtime 变化时重新初始化。

### 4.4 清理

- `[retention]` 控制默认保留天数：inbound/upload/incomplete 默认 3 天，outbound 默认 0。
- `ft-admin clean [inbound|outbound|upload|incomplete] [--days N] [--dry-run] [--yes]`。
- 不带目录时默认清理 inbound 和 upload；outbound 数据不动。
- `--days` 未指定时使用服务端 retention；目录 retention 为 0 时必须显式指定天数。

### 4.5 审计日志

审计事件写入运行日志，字段包含：

```text
event=audit action actor actor_type target result request_id remote_ip
             client_user client_host
```

- 覆盖所有 `/v1` 请求，包括认证、用户管理、文件访问和分片操作。
- `client_user`/`client_host` 来自客户端请求头 `X-Client-User`/`X-Client-Host`（当前 Linux 登录用户和主机名），可被伪造，仅作辅助审计。
- 不记录密码、token、S3 密钥和请求体。

### 4.6 路径校验

- 路径必须是相对路径，拒绝绝对路径、空段、`.`、`..`、控制字符和非法 UTF-8。
- 相对路径最大 1024 字节，单段最大 255 字节。
- 下载只允许 `inbound/`、`upload/`；上传只允许 `outbound/`。

## 5. ft-client

### 5.1 启动与凭证

- 默认读取 `/etc/file-transfer/config.toml`，可用 `-c` 或 `FT_CLIENT_CONFIG` 覆盖。
- 本地凭证保存在 `~/.file-transfer`（TOML，0600）：

```toml
username = "zhangsan"
password = "Abcd1234!"
```

- 有凭证：直接显示菜单。
- 无凭证：启动时选择「登录已有账号 / 注册新用户 / 退出」。
- 未注册用户登录时返回 `not_registered`，客户端引导注册；注册用户名需使用本人姓名全拼，密码需满足强度规则。
- 注册成功后保存凭证并自动登录，提示凭证保存路径。

### 5.2 配置

```toml
[server]
  url = "http://ft-server:8080"

[upload]
  warn_files = 20
  warn_size = "100MiB"

[download]
  name_width = 80
```

- 上传文件数或总大小超过阈值时显示醒目警告；0 表示关闭对应提示。
- `name_width` 控制下载清单中文件名的显示列数。

### 5.3 交互

- 菜单和输入使用 promptui/readline。
- 快捷键：方向键移动、回车确认/进入、空格勾选、ESC 返回上级、Ctrl+C 退出程序。
- 路径输入支持 Tab 补全，多匹配时再次 Tab 列出候选；提示显示当前路径、默认值和操作说明。
- 子命令：`register`、`list`、`download`、`upload`；无参数时进入交互菜单。

### 5.4 上传

- 每次上传生成新批次目录 `outbound/<YYYYMMDDNN>/`。
- 上传前显示批次号、源路径、目标、文件数、总大小；超过配置阈值时提示。
- 任务状态保存在 `~/.cache/file-transfer/tasks/`，记录源路径、目标、批次、文件清单和完成状态。
- 中断判断综合本地任务状态、远端批次文件比对和分片状态。
- 中断后可选「断点续传（沿用原批次）」或「重新上传（新批次，覆盖本次文件）」。
- 清单确认中比较上次任务和本次文件集，显示新增/缺失/修改/未变；文件减少时提示续传不会删除远端旧文件。
- 上传完成后提示批次号，并要求向 ft-operator 提供批次号以发起拷出流程。
- 上传完成或部分失败后直接退出程序；上传前取消返回上级菜单。

### 5.5 下载

- **inbound**：按子目录浏览；文件只展示，不支持单独下载；选中目录后可进入或下载整个目录。
- **upload**：文件和目录可多选；空格勾选、回车进入目录、`[确认下载清单]` 汇总下载。
- 勾选上级目录后，下级文件和目录显示为已选；部分选中显示 `[-]`。
- 下载清单按路径展示：整个目录折叠为 `目录/` 并标注文件数/大小，部分选中列出具体文件；超长路径中间省略。
- 确认清单后才询问保存路径（默认本地当前路径），随后开始下载。
- 下载完成或部分失败后直接退出程序。

## 6. ft-admin

- 默认读取 `/etc/file-transfer/config.toml`（TOML，与 ft-client 共用），只读取 `[server] url`；可用 `-c` 或 `FT_ADMIN_CONFIG` 覆盖。
- 管理员凭证通过 `FT_ADMIN_USER` / `FT_ADMIN_PASSWORD` 提供。
- 交互使用 promptui：菜单、输入、密码遮罩、确认；ESC 返回、Ctrl+C 退出。
- 菜单功能：添加用户、删除用户、重置密码、列出用户、清理数据、初始化。
- 删除用户和清理数据需要确认；非交互使用 `--yes`。
- cron 使用 `script/ft-clean.sh`，从 `/etc/file-transfer/ft-admin.env` 读取管理员凭证并自动追加 `--yes`。

## 7. 部署

```sh
make build
make test
```

- `ft-server`：与 RustFS 同机，systemd 运行，配置 `ft-server.toml`。
- `ft-admin init`：初始化桶、IAM 身份和策略。
- `ft-client`：`script/install.sh` 安装二进制和 `/etc/file-transfer/config.toml`。
- 示例配置：`config/client.example.toml`、`config/server.example.toml`。

目录结构：

```text
cmd/{server,client,admin}/
internal/
  adminapi/ adminuser/ auth/ bootstrap/ client/ cliui/ config/
  iam/ logging/ protocol/ s3/ server/ version/
config/
script/install.sh
script/ft-clean.sh
```

## 8. 安全与限制

- 仅限局域网明文 HTTP；`ft-server` 的 8080 端口不应暴露到不可信网络。
- token 默认 24 小时；客户端收到 401 自动重登并重试一次。
- 客户端账号密码以明文保存在 `~/.file-transfer`（0600）；不需要自动登录时删除该文件。
- `ft-operator` 可删除所有用户 `inbound/` 内容，但 `inbound/.keep` 受 Deny 保护；策略变更需重启 `ft-server` 生效。
- 审计中的客户端用户名/主机名来自请求头，可被伪造。
- 普通用户在 Console 中可能看到其他用户的顶层目录名，但无法读取内容。
- 旧批次目录不会自动删除；需要 ft-operator 或管理员按批次清理。
- 不实现限流、配额和 RPM/DEB 打包。