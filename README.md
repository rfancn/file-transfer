# 简易文件摆渡系统

用于在相互隔离的网络之间，按用户、按方向安全交换文件。所有文件集中存放在 RustFS 的 `file-transfer` 桶中：

- 用户使用 `ft-client`（用户端）上传和下载；
- 需要直连 RustFS 的用户使用 RustFS Console 或 Cyberduck 访问自己有权访问的目录；
- 管理员使用 `ft-admin`（管理客户端）初始化系统、管理用户和清理数据；
- `ft-server` 部署在 RustFS 同机，统一完成身份认证、目录隔离和文件代理。

## 目录模型

```text
file-transfer/
└── <用户名>/
    ├── inbound/                    # 外网 → 内网
    ├── outbound/<YYYYMMDDNN>/      # 内网 → 外网，每次上传一个批次
    └── upload/                     # 工作电脑 → 服务器
```

| 目录 | 摆渡方向 | 写入方 | 读取方 |
|---|---|---|---|
| `inbound/` | 外网 → 内网 | 摆渡操作员 | 用户 |
| `outbound/<批次>/` | 内网 → 外网 | 用户 | 摆渡操作员 |
| `upload/` | 工作电脑 → 服务器 | 工作电脑用户 | 用户 |

`outbound` 每次上传自动生成当天递增的批次号，例如 `2026111301`。批次号是摆渡操作员取件的凭据，也是后续清理的边界。`inbound` 和 `upload` 是一次性交付目录，按需人工或定时清理。

## 角色与工具

| 角色 | 使用工具 | 主要职责 |
|---|---|---|
| 用户 | `ft-client`（用户端） | 下载 `inbound`/`upload`，上传 `outbound` |
| 工作电脑用户 | RustFS Console / Cyberduck | 上传 `upload`，查看自己的 `inbound` |
| 摆渡操作员 | RustFS Console / Cyberduck（`ft-operator`） | 向所有用户 `inbound` 放件，从所有用户 `outbound` 取件 |
| 管理员 | `ft-admin`（管理客户端） | 初始化、创建/删除用户、重置密码、清理数据 |
| 服务端 | `ft-server` | 认证、目录隔离、文件代理、断点续传、审计 |

普通用户在 Console 中只能读取自己的目录内容，但可能看到其他用户的顶层目录名；摆渡操作员可以浏览所有用户的 `inbound` 和 `outbound`，但不能访问 `upload`。

## 三类摆渡场景

### 场景一：外网拷入内网（`inbound`）

用于把外网文件交付给用户。

1. 摆渡操作员使用 `ft-operator` 账号登录 RustFS Console。
2. 将文件上传到目标用户的 `inbound/` 下，按带日期信息的批次号建立子目录，例如：
   ```text
   zhangsan/inbound/2026111301/report.pdf
   ```
3. 用户运行 `ft-client`，选择「下载拷入文件（从 inbound 下载）」。
4. 按子目录浏览；文件只展示，选中目录后可以进入查看或下载整个目录。只支持整目录下载，文件清单和总大小会先确认。
5. 系统用cronjob或者手动运行`ft-admin clean`命令来清理 `inbound` 中的历史文件。

也可以使用命令行下载单个文件：

```sh
ft-client download inbound 2026-11-13/report.pdf --output ./inbound
```

注意事项：

- 普通用户对 `inbound` 只读，不能自行放入文件；外网文件统一由摆渡操作员写入。
- `inbound` 中的文件不会被自动移到其他目录，下载不会改变远端内容。
- 定时清理可执行 `ft-admin clean inbound --days 3`。

### 场景二：内网拷出外网（`outbound`）

用于把内网文件交付到外网，每次交付形成一个独立批次。

1. 用户运行 `ft-client`，选择「上传拷出文件（上传到 outbound）」。
2. 选择本地文件或目录，确认账号、批次号、源路径、目标路径、文件数、总大小和文件清单。
3. 确认后开始上传。文件数或总大小超过提示阈值时会显示醒目警告，避免误传。
4. 上传成功后，客户端输出批次号，例如：
   ```text
   批次号: 2026111301
   ```
5. 用户将批次号和用户名告知摆渡操作员。
6. 摆渡操作员在 Console 中进入 `zhangsan/outbound/2026111301/`，核对并下载文件，完成外网拷出。
7. 拷出数据不会清理，方便以后进行审计

注意事项：

- 普通用户对 `outbound` 只写不可读，上传完成后不能从该目录下载回来，请保留本地源文件。
- 每次上传都生成新批次；批次号也可通过 `ft-client list outbound` 查询。
- 上传中断后再次上传同一源路径和目标，客户端会提示「断点续传 / 重新上传 / 取消」：
  - 断点续传沿用原批次，只补齐未完成或修改过的文件；
  - 重新上传分配新批次，并覆盖本次计划中的所有文件。
- 批次上传失败时不会提示发起拷出流程；请先完成续传或重新上传。
- `outbound` 默认不自动清理。

### 场景三：工作电脑传输到服务器（`upload`）

用于把工作电脑上的文件传输到服务器。

1. 工作电脑用户在 RustFS Console 中使用自己的账号登录。
2. 将文件或目录上传到自己的 `upload/` 下，例如：
   ```text
   zhangsan/upload/project-a/data.zip
   ```
3. 用户运行 `ft-client`，选择「下载上传文件（从 upload 下载）」。
4. 在列表中勾选文件或目录：
   - 空格勾选；
   - 勾选目录会包含其下全部内容，部分选中显示 `[-]`；
   - 选择「确认下载清单」后显示汇总，再选择保存路径并下载。
5. 下载完成后，由管理员按需清理：

```sh
ft-admin clean upload --days 3
```

也可以使用命令行下载单个文件：

```sh
ft-client download upload project-a/data.zip --output ./upload
```

注意事项：

- `upload` 是工作电脑到服务器的单向交付目录，`ft-client` 不能向其中上传。
- 摆渡操作员不能访问 `upload`；该目录仅文件所属用户、`ft-server` 数据面和 `ft-admin` 管理面可访问。

## 系统组成

### `ft-server`

系统唯一的业务入口，部署在 RustFS 同机，负责：

- 用户注册、登录和管理员登录；
- 按用户和目录限制文件列表、下载、上传范围；
- 使用 `ft-data` / `ft-admin` 服务身份代理 RustFS 操作，客户端不接触 S3 凭据；
- 大文件自动分片、断点续传和未完成分片清理；
- 用户初始化、用户管理和过期数据清理；
- 运行日志与操作审计。

`ft-server` 未完成初始化时，业务接口返回 503，并提示先执行 `ft-admin init`。

### `ft-client`（用户端）

用户端交互式命令行工具：

- 首次使用可登录已有账号或注册新用户；
- 上传本地文件或整个目录到 `outbound`，自动分配批次号；
- 从 `inbound` 按子目录浏览并整目录下载；
- 从 `upload` 多选文件和目录下载；
- 上传和下载均支持中断后续传；
- 保存登录凭证后可自动登录。

### `ft-admin`（管理客户端）

管理客户端，经 `ft-server` 完成：

- 系统初始化；
- 添加用户、删除用户、重置密码、列出用户；
- 按目录和保留天数清理数据或未完成分片；
- 支持非交互执行，便于 cron 定时清理。

### RustFS Console

直连 RustFS 的工具：

- 普通用户使用个人账号上传 `upload`、读取自己的 `inbound`；
- 摆渡操作员使用 `ft-operator` 向所有用户 `inbound` 放件、从所有用户 `outbound` 取件。

## 部署与初始化

### 1. 构建

```sh
make build
```

生成 `bin/ft-server`、`bin/ft-client`、`bin/ft-admin`。

### 2. 配置并启动 `ft-server`

`ft-server` 必须与 RustFS 同机部署，通过本机地址访问 RustFS。最小配置示例：

```toml
[server]
  listen = ":8080"
  log_file = "/var/log/file-transfer/ft-server.log"

[s3]
  endpoint = "http://127.0.0.1:9000"
  bucket = "file-transfer"

[retention]
  inbound_days = 3
  outbound_days = 0      # 0 表示不自动清理
  upload_days = 3
  incomplete_days = 3
```

完整配置见 [config/ft-server.example.toml](config/ft-server.example.toml)。首次启动时：

- `auth.token_secret` / `auth.upload_secret` 为空或仍为示例值时，会自动生成随机密钥并写回配置；
- 自动生成管理员账号 `admin` 和 10 位随机密码，保存在 `ft-server` 当前工作目录的 `secrets/user.json`（0600），日志中只输出一次密码。

生产环境建议使用 systemd 运行，并确保工作目录可写：

```ini
[Service]
WorkingDirectory=/var/lib/ft-server
ExecStart=/usr/local/bin/ft-server -c /etc/file-transfer/ft-server.toml
Restart=always
```

#### Docker Compose 部署

镜像内只包含 `ft-server`，RustFS 需单独部署在本机。仓库根目录的 `docker-compose.yml` 默认通过 `host.docker.internal:9000` 访问 RustFS：

```sh
docker compose up -d --build
```

配置、密钥和日志分别持久化到 `./config`、`./secrets`、`./logs`（日志文件为 `./logs/ft-server/ft-server.log`）。首次启动会自动生成 `./config/ft-server.toml`；如需自定义配置，可先复制示例文件再启动：

```sh
cp config/ft-server.example.toml config/ft-server.toml
```

RustFS 地址和桶名可通过 `FT_S3_ENDPOINT`、`FT_BUCKET` 环境变量（或 `.env` 文件）覆盖。

### 3. 初始化 RustFS

在服务器主机上执行：

```sh
ft-admin init
```

执行需要RustFS root Access Key / Secret Key（也可用 `RUSTFS_ACCESS_KEY` / `RUSTFS_SECRET_KEY` 提供），root 凭据只在内存中使用。

初始化会创建桶、策略以及三个服务身份：

- `ft-data`：代理用户的数据面操作；
- `ft-admin` 服务身份：`ft-server` 使用的 RustFS 管理面身份，执行用户管理和数据清理（与命令行工具 `ft-admin` 同名，职责不同）；
- `ft-operator`：摆渡操作员，用于 `inbound` / `outbound` 摆渡。

`ft-operator` 的 8 位密码只输出到 `ft-server` 日志一次。首次初始化完成后 `ft-server` 会自动重启加载完整配置，因此 systemd 需要配置 `Restart=always`。

### 4. 创建用户

两种方式：

```sh
# 管理员创建，返回一次性密码
ft-admin user add zhangsan

# 或由用户在内网自行注册
ft-client register
```

注册用户名请使用本人姓名全拼，只能包含字母、数字、下划线和连字符；密码至少 8 位，需包含大写字母、小写字母、数字和特殊字符。

### 5. 安装 `ft-client`

```sh
sudo ./script/install.sh
```

安装脚本会写入 `/etc/file-transfer/ft-client.toml`，也可以非交互指定服务端地址：

```sh
sudo FT_SERVER_URL=http://ft-server:8080 ./script/install.sh
```

## `ft-client` 使用简介

### 配置

默认读取 `/etc/file-transfer/ft-client.toml`，可用 `-c`、`-config` 或 `FT_CLIENT_CONFIG` 指定；`FT_SERVER_URL` 可覆盖服务端地址。

```toml
[server]
  url = "http://ft-server:8080"

# 上传前数据量提示阈值，0 表示关闭对应提示
[upload]
  warn_files = 20
  warn_size = "100MiB"

# 下载清单中文件名的显示宽度
[download]
  name_width = 80
```

### 登录与凭证

- 无本地凭证时，启动菜单提供「登录已有账号 / 注册新用户 / 退出」。
- 登录或注册成功后，账号密码保存到 `~/.file-transfer`（0600，明文），后续自动登录。
- 保存的凭证失效时会删除并提示重新登录。
- 使用 `--password-stdin` 时不会保存密码。

### 常用命令

| 命令 | 说明 |
|---|---|
| `ft-client` | 进入交互菜单 |
| `ft-client register [--username NAME]` | 注册新用户 |
| `ft-client list <inbound\|outbound\|upload>` | 列出目录文件 |
| `ft-client upload <本地路径> [--dest NAME] [--yes]` | 上传到 `outbound` |
| `ft-client download <inbound\|upload> <相对路径> [--output DIR] [--force]` | 下载单个文件 |

交互菜单：

```text
上传拷出文件（上传到 outbound）
下载拷入文件（从 inbound 下载）
下载上传文件（从 upload 下载）
退出
```

方向键移动，回车确认，空格勾选，ESC 返回上级，Ctrl+C 退出。上传路径支持 Tab 补全。在管道或非终端环境下，菜单和确认会退化为编号输入和 `y/N` 输入；登录密码可通过 `--password-stdin` 提供。

上传和下载流程结束后程序会退出，需要继续操作时重新运行即可。

## `ft-admin` 使用简介

### 配置与登录

- 默认读取 `/etc/file-transfer/ft-client.toml`（与 `ft-client` 共用），只使用 `[server] url`。
- 配置文件可用 `-config` 或 `FT_ADMIN_CONFIG` 指定；`FT_SERVER_URL` 可覆盖服务端地址。
- 管理员账号密码通过 `FT_ADMIN_USER` / `FT_ADMIN_PASSWORD` 提供，否则启动时交互输入。
- 启动后先登录 `ft-server`，再显示菜单或执行子命令。

### 命令

| 命令 | 说明 |
|---|---|
| `ft-admin init` | 初始化 RustFS 桶、策略和系统身份 |
| `ft-admin user add <username>` | 添加用户，输出一次性密码 |
| `ft-admin user del <username> [--yes]` | 删除用户及其全部数据 |
| `ft-admin user reset <username>` | 重置用户密码，输出一次性新密码 |
| `ft-admin user list` | 列出用户、类型、状态、策略和组 |
| `ft-admin clean [目录] [--days N] [--dry-run] [--yes]` | 清理数据或未完成分片 |

无子命令时进入交互菜单：添加用户、删除用户、重置用户密码、列出用户、清理数据、初始化、退出。

### 清理规则

```sh
ft-admin clean                          # 按服务端配置清理 inbound 和 upload
ft-admin clean inbound --days 3         # 清理 inbound 中 3 天前的数据
ft-admin clean outbound --days 7        # 默认配置下 outbound 需显式指定天数
ft-admin clean incomplete --dry-run     # 只列出将清理的未完成分片
ft-admin clean upload --yes             # 非交互执行
```

- 不指定目录时只清理 `inbound` 和 `upload`，不触碰 `outbound`。
- 不指定 `--days` 时使用 `ft-server` 的 `[retention]` 配置。
- `inbound` / `upload` 按文件修改时间清理；`outbound` 按批次目录日期清理；`incomplete` 清理过期的未完成分片。
- `--dry-run` 只列出待删除对象；`--yes` 跳过确认。

### 定时清理

`script/ft-clean.sh` 从 `/etc/file-transfer/ft-admin.env`（0600）读取管理员凭证并自动追加 `--yes`。环境文件中的变量需要 `export`：

```sh
export FT_ADMIN_USER=admin
export FT_ADMIN_PASSWORD=...
export FT_SERVER_URL=http://127.0.0.1:8080
```

```cron
0 3 * * * /path/to/ft-clean.sh inbound --days 3 >> /var/log/ft-clean.log 2>&1
```

可用环境变量：`FT_ADMIN_ENV`、`FT_ADMIN_BIN`、`FT_ADMIN_CONFIG`。

## `ft-server` 运行与维护

### 启动阶段

1. 读取配置并初始化日志。
2. 生成或加载 `ft-server` 管理员账号。
3. 检查服务身份凭据是否已初始化：
   - 未初始化：业务接口不可用，等待执行 `ft-admin init`；
   - 已初始化：正常提供用户和管理员服务。

### 初始化后

- `ft-admin init` 完成后 `ft-server` 自动重启，加载服务身份和完整配置。
- 创建用户时会写入 `inbound/.keep`、`outbound/.keep`、`upload/.keep` 占位文件，保证空目录可见。
- 服务身份凭据保存在 `ft-server` 工作目录的 `secrets/` 下（0600），请限制读取权限。

### 日常运维

- 登录 token 默认 24 小时有效；客户端凭证失效后会自动重新登录一次。
- 大文件上传中断后可从已完成分片继续，服务端按保留天数清理长期未完成的分片。
- 运行日志按大小和天数轮转，记录认证、用户管理、文件访问和清理等审计事件。
- 审计日志中的客户端用户名和主机名来自客户端自报，可被伪造，仅作辅助审计。
- 修改用户策略或权限后，建议重启 `ft-server` 使策略重新生效。

## 权限矩阵

| 身份 | `inbound` | `outbound` | `upload` |
|---|---|---|---|
| 普通用户（Console/Cyberduck） | 列出、读取自己的目录 | 仅列出自己的目录 | 列出、读取、上传自己的目录 |
| 用户（`ft-client` 用户端） | 列出、读取 | 列出、上传 | 列出、读取 |
| 摆渡操作员（`ft-operator`） | 列出、上传、删除所有用户 | 列出、读取所有用户 | 无权限 |
| 管理员（`ft-admin` 管理客户端） | 管理、删除 | 管理、删除 | 管理、删除 |

普通用户无法在 S3 层把桶根目录列表过滤为只显示自己的目录，因此 Console 中可能看到其他用户的顶层目录名，但无法读取其内容。

## 安全边界

- `ft-server` 仅用于可信局域网，使用明文 HTTP；不要将管理端口暴露到不可信网络。
- 客户端只允许 `http://` 服务端地址，不应跨不可信网络使用。
- `ft-server` 不向客户端下发 S3 凭据；所有 RustFS 操作由服务身份代理。
- `ft-client` 以明文保存账号密码（0600）；不需要自动登录时删除 `~/.file-transfer`。
- `ft-server` 管理员账号和服务身份凭据保存在 `secrets/`（0600），请严格限制访问。
- RustFS root 凭据只在初始化期间于内存中使用，不写入磁盘。
- `ft-operator` 可以删除所有用户 `inbound` 下的对象（`inbound/.keep` 除外），请谨慎授权。
- 生产环境请替换配置中的全部默认密钥和口令。

## 常见问题

**外网用户为什么不能直接上传到 `inbound`？**

`inbound` 由 `ft-operator` 统一写入，普通用户只读，避免外网账号直接向内部目录放件。外网文件先交给摆渡操作员，再进入目标用户的 `inbound`。

**用户上传后为什么找不到文件？**

上传写入的是 `outbound/<批次>/`，普通用户对该目录只写不可读。请保留本地源文件，并把批次号告知摆渡操作员取件。

**工作电脑可以用 `ft-client` 上传到 `upload` 吗？**

不能。`ft-client upload` 只写入 `outbound`。工作电脑传文件到服务器请使用 RustFS Console 或 Cyberduck 上传到 `upload`，再由用户通过 `ft-client` 下载。

**忘记批次号怎么办？**

执行 `ft-client list outbound` 查看自己的批次目录，或查看上传完成时输出的批次号。

**`outbound` 为什么没有被自动清理？**

默认配置中 `outbound_days = 0`，即不自动清理。需要显式执行 `ft-admin clean outbound --days N`，或在配置中设置保留天数。
