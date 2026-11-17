# 简易文件摆渡系统

用于在相互隔离的网络之间，按用户、按方向安全交换文件。个人文件集中存放在 RustFS 的 `home` 桶中，公共资料存放在 `shared` 桶中：

- 用户使用 `ft-client`（用户端）上传和下载；
- 工作电脑用户建议使用 Cyberduck 管理自己的文件（见「Cyberduck 配置」），也可使用 RustFS Console，均只能访问自己有权访问的目录；
- 管理员使用 `ft-admin`（管理客户端）初始化系统、管理用户和清理数据；
- `ft-server` 部署在 RustFS 同机，统一完成身份认证、目录隔离和文件代理。

## 目录模型

```text
home/                                # 个人数据，s3.home_bucket 可配置
└── <用户名>/
    ├── inbound/                    # 外网 → 内网
    ├── outbound/<YYYYMMDDNN>/      # 内网 → 外网，每次上传一个批次
    └── upload/                     # 工作电脑 → 服务器

shared/                              # 公共资料，s3.shared_bucket 可配置
├── software/                       # 共享目录，s3.shared_dirs 可配置
└── document/
```

普通用户组可以列出并下载 `shared` 桶中 `s3.shared_dirs` 配置目录下的内容，不能写入或删除。共享操作员组可以列出、上传和删除共享桶内容，但不能删除桶。

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
| 工作电脑用户 | RustFS Console / Cyberduck | 列出自己的 `inbound`/`outbound`/`upload`，上传或删除自己 `upload` 下的内容；不能删除 `upload` 目录本身，不能下载或写入 `inbound`/`outbound` |
| 摆渡操作员 | RustFS Console / Cyberduck（operator 账号） | 向所有用户 `inbound` 放件/删除，从所有用户 `outbound` 取件 |
| 共享操作员 | RustFS Console / Cyberduck（shared-operator 账号） | 列出、上传、下载和删除共享桶内容；不能删除桶 |
| 管理员 | `ft-admin`（管理客户端） | 初始化、创建/删除用户、重置密码、清理数据 |
| 服务端 | `ft-server` | 认证、目录隔离、文件代理、断点续传、审计 |

普通用户在 Console/Cyberduck 中可以列出自己的 `inbound`、`outbound` 和 `upload`，可以上传和删除自己 `upload` 下的内容（`upload` 目录本身不可删）；`inbound`/`outbound` 不可下载、不可写入，也不能删除。摆渡操作员可以浏览所有用户的 `inbound` 和 `outbound`，但不能访问 `upload`。

## 三类摆渡场景

### 场景一：外网拷入内网（`inbound`）

用于把外网文件交付给用户。

1. 摆渡操作员使用自己的 operator 账号（加入 `ft-operator-group`）登录 RustFS Console。
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
- 下载时如果本地已存在同名文件，会询问覆盖、跳过还是取消。
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

1. 工作电脑用户建议使用 Cyberduck（配置见下方「Cyberduck 配置」），也可使用 RustFS Console，用自己的账号登录。
2. 将文件或目录上传到自己的 `upload/` 下，例如：
   ```text
   zhangsan/upload/project-a/data.zip
   ```
3. 用户运行 `ft-client`，选择「下载上传文件（从 upload 下载）」。
4. 在列表中勾选文件或目录：
   - 空格勾选；
   - 勾选目录会包含其下全部内容，部分选中显示 `[-]`；
   - 选择「确认下载清单」后显示汇总，再选择保存路径并下载；本地已存在同名文件时可选择覆盖或跳过。
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

### Cyberduck 配置

工作电脑建议使用 [Cyberduck](https://cyberduck.io/) 直接管理自己的文件。新建 S3（HTTPS）书签，按以下方式填写：

| 配置项 | 值 |
|---|---|
| 连接协议 | S3（HTTPS） |
| 服务器 | S3 API 域名，例如 `s3.example.internal`（不是 `ft-server` 的业务地址） |
| 端口 | `443` |
| Access Key ID | 个人用户名 |
| Secret Access Key | 个人密码 |
| 路径 | `home/<用户名>` |

例如用户 `zhangsan` 的路径为 `home/zhangsan`。连接后可直接在 `upload/` 下拖拽上传、删除自己的文件；`inbound/`、`outbound/` 只可列出，不能下载、写入或删除。

共享资料使用同一账号，把书签路径改为 `shared`，即可浏览 `software/`、`document/` 等配置目录并下载内容；普通用户不能写入或删除共享内容。

注意事项：

- 路径必须包含自己的用户名，直接连接 home 桶根目录会被拒绝（用户组没有 `s3:ListAllMyBuckets` 权限）。
- Cyberduck 直连不经过 `ft-server`，不受应用层限速约束，大文件传输请自行控制并发（见「应用层 I/O 限速」）。

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

- 普通用户使用个人账号列出自己的 `inbound`/`outbound`/`upload`，上传或删除自己 `upload` 下的内容（目录本身不可删）；不能下载或写入 `inbound`/`outbound`；
- 摆渡操作员使用自己的 operator 账号向所有用户 `inbound` 放件/删除、从所有用户 `outbound` 取件。

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
  home_bucket = "home"
  shared_bucket = "shared"
  shared_dirs = ["software", "document"]

[retention]
  inbound_days = 3
  outbound_days = 0      # 0 表示不自动清理
  upload_days = 3
  incomplete_days = 3
```

完整配置见 [config/ft-server.example.toml](config/ft-server.example.toml)。

> **升级说明：**
>
> - `s3.bucket` 已移除。请把原桶名填入 `s3.home_bucket`，并补充 `shared_bucket` / `shared_dirs`；保留旧 `bucket` 键会导致启动报错。
> - 环境变量只保留 `FT_S3_ENDPOINT`。配置文件路径改用 `-c` / `--config`，不再读取 `FT_SERVER_CONFIG` / `FT_CLIENT_CONFIG` / `FT_ADMIN_CONFIG`。
> - 命令行长参数统一使用 `--`，例如 `--config`、`--group`、`--days`；旧的 `-config` 等单横线写法不再支持。
> - `ft-clean.sh` 的凭据文件改为 `ADMIN_USER` / `ADMIN_PASSWORD`，不再读取 `FT_ADMIN_USER` / `FT_ADMIN_PASSWORD` / `FT_ADMIN_ENV`。
> - `docker-compose.yml` 的镜像 tag 和宿主端口改为固定值，需要修改时直接编辑该文件。
> - 升级后重启 `ft-server` 并重新执行 `ft-admin init`，以创建共享桶、共享目录和新组策略。

首次启动时：

- `auth.token_secret` / `auth.upload_secret` 为空或仍为示例值时，会自动生成随机密钥并写回配置；
- 自动生成管理员账号 `admin` 和 10 位随机密码，保存在 `ft-server` 当前工作目录的 `secrets/user.json`（0600），日志中只输出一次密码。

生产环境建议使用 systemd 运行，并确保工作目录可写：

```ini
[Service]
WorkingDirectory=/var/lib/ft-server
ExecStart=/usr/local/bin/ft-server -c /etc/file-transfer/ft-server.toml
Restart=always
```

把仓库根目录的 `policy/` 复制到 `/etc/file-transfer/policy`。`ft-server` 固定使用 `policy` 目录，会先按工作目录、再按配置文件所在目录查找，无需额外配置。

#### Docker Compose 部署

镜像内只包含 `ft-server`，RustFS 需单独部署在本机。仓库根目录的 `docker-compose.yml` 默认通过 `host.docker.internal:9000` 访问 RustFS：

```sh
docker compose up -d --build
```

配置、密钥和日志分别持久化到 `./config`、`./secrets`、`./logs`（日志文件为 `./logs/ft-server/ft-server.log`）。首次启动会自动生成 `./config/ft-server.toml`；如需自定义配置，可先复制示例文件再启动：

```sh
cp config/ft-server.example.toml config/ft-server.toml
```

RustFS 地址可通过 `FT_S3_ENDPOINT` 环境变量（或 `.env` 文件）覆盖；桶名和共享目录仅在 `ft-server.toml` 中配置。

#### IAM 策略文件

IAM 策略从代码中抽离到仓库根目录 `policy/`：

| 文件 | 用途 |
|---|---|
| `user-group.json` | 用户组策略：列出、读写和删除自己的 `upload`；列出并读取 `shared` 桶配置目录；`upload/.keep` 受 Deny 保护 |
| `operator-group.json` | 摆渡操作员组策略：所有用户 inbound list/put/delete、outbound list/get，无 upload 和 admin 权限 |
| `shared-operator-group.json` | 共享操作员组策略：共享桶 list/get/put/delete，显式 Deny 删除桶 |
| `server-data.json` | `ft-data` 服务身份策略 |
| `server-admin.json` | `ft-admin` 服务身份策略 |

- 策略文件是模板，支持以下占位符：
  - `{{HOME_BUCKET}}`：替换为 `s3.home_bucket`；
  - `{{SHARED_BUCKET}}`：替换为 `s3.shared_bucket`；
  - `"{{SHARED_DIR_PREFIXES}}"`：替换为 `s3.shared_dirs` 对应的 list 前缀；
  - `"{{SHARED_DIR_OBJECT_ARNS}}"`：替换为 `s3.shared_dirs` 对应的对象 ARN。
- `${aws:username}` 是 RustFS IAM 变量，由 RustFS 在鉴权时替换，不是模板占位符。
- 渲染后会校验 JSON；残留未知 `{{...}}` 会导致启动失败。
- 用户组策略使用 `${aws:username}`，不再为每个用户创建 `ft-user-<username>-policy`。
- 用户组不授予 `s3:ListAllMyBuckets` 和 `s3:ListBucketMultipartUploads`，避免枚举 bucket 或跨用户分片信息；Cyberduck 需直接使用 `home/<username>` 路径。
- 用户组只能列出共享桶根（用于发现顶层目录）和 `s3.shared_dirs` 配置目录，不能读取其他目录。
- `ft-operator-group` 不包含任何 `admin:*` 权限，只负责 inbound/outbound 摆渡。
- `shared-operator-group` 对共享桶有 list/get/put/delete，但不包含 `admin:*` 和桶删除权限。
- `ft-data` 不授予 Delete 权限；`ft-admin clean` 使用 `ft-admin` 服务身份执行删除。
- `ft-server` 启动时加载策略；文件缺失、JSON 非法或残留未知占位符会启动失败。
- 策略目录固定为 `policy`，依次在工作目录和配置文件所在目录查找，无需配置。
- Docker 镜像内默认 `/etc/file-transfer/policy`，`docker-compose.yml` 已把 `./policy` 只读挂载到该路径。
- 修改策略后重启 `ft-server`，再执行 `ft-admin init` 使策略在 RustFS 生效。
- 用户组只能列出自己的 `inbound`/`outbound`/`upload` 前缀；Cyberduck 书签的路径应设置为 `home/<username>`，直接连接 home 桶根目录会被拒绝。

### 3. 初始化 RustFS

在服务器主机上执行：

```sh
# 交互输入 root 凭据
ft-admin init

# 或通过命令行参数/文件提供
ft-admin init --access-key <AK> --secret-key <SK>
ft-admin init --access-key <AK> --secret-key-file /etc/file-transfer/rustfs-secret
```

执行需要 RustFS root Access Key / Secret Key，可用 `--access-key` / `--secret-key` 或 `--secret-key-file` 提供，否则交互输入；root 凭据只在内存中使用。

初始化会创建桶、共享目录、策略和用户组：

- `home` 桶（`s3.home_bucket`）：个人数据，用户目录结构不变；
- `shared` 桶（`s3.shared_bucket`）：公共资料，并在 `s3.shared_dirs` 配置的目录下写入 `.keep`；
- `ft-data`：代理用户的数据面操作；
- `ft-admin` 服务身份：`ft-server` 使用的 RustFS 管理面身份，执行用户管理和数据清理（与命令行工具 `ft-admin` 同名，职责不同）；
- `ft-user-group`：普通用户组，绑定个人目录读写、`upload` 管理和共享目录只读策略；
- `ft-operator-group`：摆渡操作员组，绑定所有用户 `inbound`/`outbound` 的策略；
- `shared-operator-group`：共享操作员组，绑定共享桶 list/get/put/delete 策略。

初始化**不会创建默认操作员或共享操作员账号**。账号通过 `ft-admin user add` 创建时选择对应组，并需要二次确认。首次初始化完成后 `ft-server` 会自动重启加载完整配置，因此 systemd 需要配置 `Restart=always`。

### 4. 创建用户

两种方式：

```sh
# 管理员创建，组通过菜单选择，返回一次性密码
ft-admin user add zhangsan

# 或由用户在内网自行注册
ft-client register
```

交互式执行 `ft-admin user add` 时会弹出用户组菜单：普通用户组 / 摆渡文件操作员组 / 共享文件操作员组。选择高权限组会显示警告并要求二次确认；非交互执行必须用 `--group user`、`--group operator` 或 `--group shared-operator` 指定组，创建高权限账号还需要 `--yes`。

```sh
# 添加共享操作员
ft-admin user add zhangsan --group shared-operator --yes
```

注册用户名请使用本人姓名全拼，只能包含字母、数字、下划线和连字符；密码至少 8 位，需包含大写字母、小写字母、数字和特殊字符。

### 5. 安装 `ft-client`

```sh
sudo ./script/install.sh
```

安装脚本会写入 `/etc/file-transfer/ft-client.toml`，也可以非交互指定服务端地址：

```sh
sudo ./script/install.sh http://ft-server:8080
```

> **安全提示**：安装后二进制属主为 `root`、权限 `0755`，但没有 setuid 位，普通用户直接运行即可以自身身份工作。**不要用 `sudo ft-client` 下载**；也不要把下载目录放在其他用户可写的共享目录（如 `/tmp`）。以 root 运行且下载目录可被他人写入时，攻击者可以预置 `*.part` 符号链接，诱导客户端覆盖任意文件。
>
> 下载目录新建时权限为 `0700`；续传会拒绝非普通文件、属主不是当前用户的 `.part`/`.etag` 临时文件；目标为符号链接或目录时拒绝覆盖；`--force` 使用原子 `rename` 替换普通文件。

## `ft-client` 使用简介

### 配置

默认读取 `/etc/file-transfer/ft-client.toml`，可用 `-c` 或 `--config` 指定；服务端地址在配置文件的 `[server] url` 中设置。

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
- 配置文件可用 `--config` 指定；服务端地址在配置文件的 `[server] url` 中设置。
- 管理员账号密码可用 `--admin-user` / `--admin-password` 或 `--admin-password-file` 提供，否则启动时交互输入。`--admin-password` 会出现在进程列表中，自动化场景建议使用 `--admin-password-file`。
- 启动后先登录 `ft-server`，再显示菜单或执行子命令。

### 命令

| 命令 | 说明 |
|---|---|
| `ft-admin init` | 初始化 RustFS 桶、策略和系统身份 |
| `ft-admin user add <username> [--group user\|operator\|shared-operator] [--yes]` | 添加用户，交互式用菜单选组；非交互必须指定 `--group`，高权限组需 `--yes` |
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

`script/ft-clean.sh` 从 `/etc/file-transfer/ft-admin.env`（0600）读取管理员凭证，把密码写入临时 0600 文件后通过 `--admin-password-file` 传给 `ft-admin`，避免密码出现在进程列表中。服务器地址从 `/etc/file-transfer/ft-client.toml` 的 `[server] url` 读取：

```sh
ADMIN_USER=admin
ADMIN_PASSWORD=...
```

```cron
0 3 * * * /path/to/ft-clean.sh inbound --days 3 >> /var/log/ft-clean.log 2>&1
```

脚本固定使用 `/usr/local/bin/ft-admin` 和 `/etc/file-transfer/ft-client.toml`。

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

### 应用层 I/O 限速

`ft-server` 通过 `[limits]` 限制经过它的上传、下载和分片上传：

| 配置 | 说明 | 默认 |
|---|---|---:|
| `enabled` | 总开关 | `true` |
| `max_concurrent` | 全局同时传输数 | 4 |
| `max_concurrent_per_user` | 单用户同时传输数 | 1 |
| `global_read_bps` | 全局下载带宽，0 表示不限 | 0 |
| `global_write_bps` | 全局上传带宽，0 表示不限 | 0 |
| `per_user_read_bps` | 单用户下载带宽，0 表示不限 | 0 |
| `per_user_write_bps` | 单用户上传带宽，0 表示不限 | 0 |
| `queue_timeout` | 排队超时 | 10s |

- 并发上限是第一道闸门，用于保护 HDD 寻道；bps 默认 0，先用 warp 测出对象层吞吐，再写约 70%、读约 75% 填入。
- 单用户带宽建议取全局值的一半，防止一个用户占满。
- 超过 `queue_timeout` 返回 `429` 和 `Retry-After`，`ft-client` 会自动退避重试。
- Console/Cyberduck 直连不经过 `ft-server`，不受这些值限制，需要由系统层 cgroup 兜底。
- 修改配置后重启 `ft-server` 生效；置 `enabled = false` 可整体关闭限速。

## 权限矩阵

| 身份 | `inbound` | `outbound` | `upload` |
|---|---|---|---|
| 普通用户（Console/Cyberduck） | 列出 | 列出 | 列出、上传、删除自己的文件；`upload` 目录本身受保护 |
| 用户（`ft-client` 用户端） | 列出、读取 | 列出、上传 | 列出、读取 |
| 操作员（`ft-operator-group`） | 列出、上传、删除所有用户 | 列出、读取所有用户 | 无权限 |
| 管理员（`ft-admin` 管理客户端） | 管理、删除 | 管理、删除 | 管理、删除 |

普通用户只能列出自己的 `inbound`/`outbound`/`upload` 前缀，无法看到其他用户目录。Cyberduck 建议把书签初始路径设置为 `home/<用户名>`（见「Cyberduck 配置」）。

### 共享桶权限矩阵

| 身份 | 共享桶根 | `s3.shared_dirs` 配置目录 | 其他目录 |
|---|---|---|---|
| 普通用户（`ft-user-group`） | 列出顶层目录 | 列出、读取 | 无权限 |
| 共享操作员（`shared-operator-group`） | 列出 | 列出、读取、上传、删除 | 列出、读取、上传、删除 |
| 其他用户 | 无权限 | 无权限 | 无权限 |

共享操作员不能删除共享桶，也不能管理 IAM 用户或策略。

## 安全边界

- 客户端支持 `http://` 和 `https://` 服务端地址；跨不可信网络时应使用 HTTPS。
- `ft-server` 位于反向代理（如 Traefik）之后且使用自签名或内部 CA 证书时，可在 `ft-client` 配置中设置 `server.ca_cert` 指向签发该证书的 CA PEM；临时调试可用 `server.insecure_skip_verify = true`，两者不能同时设置。
- `ft-server` 的管理端口不应暴露到不可信网络。
- `ft-server` 不向客户端下发 S3 凭据；所有 RustFS 操作由服务身份代理。
- `ft-client` 以明文保存账号密码（0600）；不需要自动登录时删除 `~/.file-transfer`。
- `ft-server` 管理员账号和服务身份凭据保存在 `secrets/`（0600），请严格限制访问。
- RustFS root 凭据只在初始化期间于内存中使用，不写入磁盘。
- `ft-operator-group` 成员可以读取所有用户 `outbound`、删除所有用户 `inbound` 对象（`inbound/.keep` 除外），是高权限角色；创建操作员账号时 `ft-admin` 会要求二次确认，服务端也会校验 `confirm_operator` 并记录审计日志。
- 操作员策略不包含任何 `admin:*` 权限，不能管理 IAM 用户或策略。
- `shared-operator-group` 成员可以读写和删除共享桶内容，但不能删除桶；创建共享操作员账号同样需要二次确认并记录审计日志。
- 初始化不创建默认操作员或共享操作员账号；成员必须显式创建并加入对应组。
- 生产环境请替换配置中的全部默认密钥和口令。

## 常见问题

**外网用户为什么不能直接上传到 `inbound`？**

`inbound` 由操作员组账号统一写入，普通用户只读，避免外网账号直接向内部目录放件。外网文件先交给摆渡操作员，再进入目标用户的 `inbound`。

**用户上传后为什么找不到文件？**

上传写入的是 `outbound/<批次>/`，普通用户对该目录只写不可读。请保留本地源文件，并把批次号告知摆渡操作员取件。

**工作电脑可以用 `ft-client` 上传到 `upload` 吗？**

不能。`ft-client upload` 只写入 `outbound`。工作电脑传文件到服务器建议使用 Cyberduck（配置见「Cyberduck 配置」）或 RustFS Console 上传到 `upload/`，再由用户通过 `ft-client` 下载。

**忘记批次号怎么办？**

执行 `ft-client list outbound` 查看自己的批次目录，或查看上传完成时输出的批次号。

**`outbound` 为什么没有被自动清理？**

默认配置中 `outbound_days = 0`，即不自动清理。需要显式执行 `ft-admin clean outbound --days N`，或在配置中设置保留天数。

**`ft-admin init` 报 `创建桶 ...: Permanent Redirect`？**

通常是 `s3.endpoint` 或 `s3.region` 不匹配：

- 确认 `s3.endpoint` / `FT_S3_ENDPOINT` 使用 RustFS 的 HTTPS API 地址，例如 `https://s3.example.internal`；不要用 `http://host.docker.internal:9000` 访问只提供 HTTPS 的入口。
- 确认 `s3.region` 与 bucket 所在 region 一致；当前部署通常为 `us-east-1`。
- 如果 `ft-server` 在 Docker 中，确认容器能解析该域名，并信任 RustFS 的证书。
- 新版本会在错误中带出当前 endpoint、配置 region 和服务端返回的 region。
