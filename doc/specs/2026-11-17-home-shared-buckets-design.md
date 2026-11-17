# home 桶、共享桶与共享操作员组设计

- 日期：2026-11-17
- 状态：设计已确认，待评审
- 范围：`ft-server` 配置、IAM 策略、`ft-admin init`、`ft-admin user add`

## 1. 背景

当前系统只有一个桶，桶名由 `s3.bucket` 配置（默认 `file-transfer`），用户目录直接挂在桶下：

```text
file-transfer/
└── <用户名>/
    ├── inbound/
    ├── outbound/
    └── upload/
```

需要把个人数据与公共资料分开：

1. 个人数据放进独立的 home 桶，桶名可配置，默认 `home`；用户目录结构不变。
2. 新增共享桶，桶名可配置，默认 `shared`；init 时在桶下创建可配置的公共目录，默认 `software`、`document`。普通用户组对配置目录内容有 list/get 权限。
3. 新增共享操作员组 `shared-operator-group`，对共享桶内容有 list/get/put/delete 权限，但不能删除桶。

## 2. 已确认决策

| 决策 | 结论 |
|---|---|
| home 桶配置 | 新增 `s3.home_bucket`，移除 `s3.bucket` |
| 共享桶配置 | 新增 `s3.shared_bucket`、`s3.shared_dirs`，默认 `shared`、`["software", "document"]` |
| 共享操作员组 | 新建独立组 `shared-operator-group`，与 `ft-operator-group` 互不影响 |
| 共享操作员策略名 | `shared-operator-policy` |
| 用户组共享权限 | `ft-user-group` 增加共享桶根目录浏览和配置目录的 list/get |
| 组成员管理 | `ft-admin user add` 支持 `--group shared-operator`，交互式菜单也可选 |
| `ft-client` | 不改，仍只代理 home 桶；共享桶通过 Cyberduck / RustFS Console 访问 |
| 设计文档位置 | `doc/specs/` |

## 3. 目标与非目标

### 3.1 目标

- `ft-admin init` 幂等地创建 home 桶、共享桶及共享目录。
- 普通用户只能读取共享桶中配置目录的内容，不能读写其他目录。
- 共享操作员可以管理共享桶内容，但不能删除桶。
- home 桶的现有摆渡流程和权限不变。
- 配置错误在启动阶段明确报错，不静默切换到错误的桶。

### 3.2 非目标

- 不把共享桶接入 `ft-client` 的上传/下载/分片 API。
- 不迁移 home 桶中的历史数据。
- 不改变 `ft-operator-group` 的现有权限和成员。
- 不实现共享桶的定时清理。
- 不实现共享目录的嵌套路径；`shared_dirs` 只允许单级目录名。

## 4. 桶与目录结构

```text
home/                                  # 个人数据桶，s3.home_bucket
└── <用户名>/
    ├── inbound/.keep
    ├── outbound/.keep
    └── upload/.keep

shared/                                # 共享桶，s3.shared_bucket
├── software/.keep
└── document/.keep
```

- 用户占位目录仍由 `iam.Manager.CreatePlaceholders` 在创建用户时写入 home 桶。
- 共享目录由 `ft-admin init` 写入 `<dir>/.keep` 占位对象，与现有目录标记方式一致。
- `.keep` 仅用于让目录在 S3 浏览器中可见，不承载业务数据。

## 5. 配置模型

### 5.1 结构体

```go
type S3Section struct {
    Endpoint     string   `toml:"endpoint"`
    HomeBucket   string   `toml:"home_bucket"`
    SharedBucket string   `toml:"shared_bucket"`
    SharedDirs   []string `toml:"shared_dirs"`
    Region       string   `toml:"region"`
}
```

### 5.2 默认值与示例

```toml
[s3]
  endpoint = "http://127.0.0.1:9000"
  home_bucket = "home"
  shared_bucket = "shared"
  shared_dirs = ["software", "document"]
  region = "us-east-1"
```

### 5.3 配置来源

桶名和共享目录只从 `ft-server.toml` 读取，不提供环境变量覆盖。`FT_S3_ENDPOINT` 保留用于覆盖 RustFS 地址；`FT_BUCKET` 删除后不再使用。策略目录固定为 `policy`，依次在工作目录和配置文件所在目录查找，无需配置。

### 5.4 校验规则

- `home_bucket`、`shared_bucket` 非空。
- 两个桶名不能相同。
- 桶名和目录名不能包含 `/`、`\`、空白、单引号、双引号、`*`、`?`、`{`、`}`。
- 目录名不能是 `.` 或 `..`，不能为空，且列表内不能重复。
- `shared_dirs` 至少包含一个目录。
- 旧配置中仍存在 `s3.bucket` 时，启动返回明确错误：

  ```text
  s3.bucket 已移除，请改为 s3.home_bucket；升级时可将原值填入 home_bucket
  ```

## 6. IAM 策略

### 6.1 策略模板

策略文件仍是 `policy/*.json`，加载时渲染以下占位符：

| 占位符 | 替换结果 |
|---|---|
| `{{HOME_BUCKET}}` | home 桶名 |
| `{{SHARED_BUCKET}}` | 共享桶名 |
| `"{{SHARED_DIR_PREFIXES}}"` | `"software/", "software/*", "document/", "document/*"` |
| `"{{SHARED_DIR_OBJECT_ARNS}}"` | `"arn:aws:s3:::shared/software/*", "arn:aws:s3:::shared/document/*"` |

- 前两个占位符可出现在字符串内部。
- 后两个占位符必须作为独立的 JSON 字符串元素出现，替换结果自带引号，因此策略文件在渲染前仍是合法 JSON。
- 渲染后执行 JSON 校验；任何残留的 `{{` 或 `}}` 都视为未知占位符并报错。
- `iam.LoadPolicies(dir, PolicyOptions)` 接收 home 桶、共享桶和共享目录配置。

```go
type PolicyOptions struct {
    HomeBucket   string
    SharedBucket string
    SharedDirs   []string
}

func LoadPolicies(dir string, opts PolicyOptions) (*Policies, error)
```

`Policies` 增加 `SharedOperator []byte`，加载 `shared-operator-group.json`。

### 6.2 普通用户组策略

`policy/user-group.json`：

- 现有 home 规则中的 `file-transfer` 改为 `{{HOME_BUCKET}}`，权限不变。
- 新增共享桶规则：
  - `s3:HeadBucket`、`s3:GetBucketLocation`；
  - 以 `s3:delimiter = "/"` 列出共享桶根，允许查看顶层目录；
  - 允许列出 `software/`、`software/*`、`document/`、`document/*`；
  - 允许对 `software/*`、`document/*` 执行 `GetObject`、`GetObjectAttributes`、`GetObjectTagging`、`GetObjectRetention`、`GetObjectLegalHold`；
  - 不授予共享桶的 Put、Delete 或桶管理权限。

### 6.3 共享操作员组策略

新增 `policy/shared-operator-group.json`，绑定到 `shared-operator-group`：

| 权限 | 范围 |
|---|---|
| `HeadBucket`、`GetBucketLocation`、`GetBucketAcl` 等只读桶元数据 | `arn:aws:s3:::{{SHARED_BUCKET}}` |
| `ListBucket`、`ListBucketMultipartUploads` | `arn:aws:s3:::{{SHARED_BUCKET}}` |
| `GetObject*` | `arn:aws:s3:::{{SHARED_BUCKET}}/*` |
| `PutObject`、`PutObjectTagging`、`DeleteObject`、`DeleteObjectVersion`、`DeleteObjectTagging` | `arn:aws:s3:::{{SHARED_BUCKET}}/*` |
| `AbortMultipartUpload`、`ListMultipartUploadParts` | `arn:aws:s3:::{{SHARED_BUCKET}}/*` |
| 显式 Deny `s3:DeleteBucket` | `arn:aws:s3:::{{SHARED_BUCKET}}` |

不授予：

- `admin:*`；
- `s3:ListAllMyBuckets`；
- 创建、删除、修改桶策略等桶管理权限。

### 6.4 其余策略

- `policy/operator-group.json`：`file-transfer` 改为 `{{HOME_BUCKET}}`，权限不变。
- `policy/server-data.json`：`file-transfer` 改为 `{{HOME_BUCKET}}`，权限不变。
- `policy/server-admin.json`：`file-transfer` 改为 `{{HOME_BUCKET}}`，权限不变。

## 7. init 流程

`ft-admin init` 调用 `ft-server` 的初始化接口，`bootstrap.Run` 按以下顺序执行：

1. 用 root 凭据创建或确认 home 桶 `cfg.S3.HomeBucket`。
2. 创建或确认共享桶 `cfg.S3.SharedBucket`。
3. 为 `cfg.S3.SharedDirs` 中每个目录写入 `<dir>/.keep` 占位对象。
4. 创建或确认服务身份 `ft-data`、`ft-admin`，并绑定对应策略。
5. 创建或确认用户组 `ft-user-group`，绑定渲染后的用户组策略。
6. 创建或确认操作员组 `ft-operator-group`，绑定原有操作员策略。
7. 创建或确认共享操作员组 `shared-operator-group`，绑定 `shared-operator-policy`。
8. 保存服务身份凭据。

行为说明：

- 所有步骤幂等，重复执行不会删除已有对象或报错。
- init 不创建默认操作员或共享操作员用户。
- 共享桶只由 init 和后续运维操作使用；`ft-server` 运行时不代理共享桶请求。
- 首次初始化后是否重启仍由现有 `ConfigChanged` 逻辑决定。

## 8. 用户组管理

### 8.1 常量

```go
const (
    UserGroup    = "ft-user-group"
    UserGroupPol = "ft-user-policy"

    AdminGroup    = "ft-operator-group"
    AdminGroupPol = "ft-operator-policy"

    SharedAdminGroup = "shared-operator-group"
    SharedAdminPol   = "shared-operator-policy"
)
```

### 8.2 添加用户

- 交互式菜单增加「共享操作员组 (shared-operator-group)」。
- 非交互用法：

  ```sh
  ft-admin user add alice --group shared-operator --yes
  ```

- `--group user` 映射 `ft-user-group`，`--group operator` 映射 `ft-operator-group`，`--group shared-operator` 映射 `shared-operator-group`。
- 创建 `ft-operator-group` 或 `shared-operator-group` 成员时：
  - CLI 显示对应的高权限警告；
  - 非交互必须带 `--yes`；
  - 服务端要求 `confirm_operator = true`；
  - 记录审计日志。
- `iam.SetupUser` 对 `shared-operator-group` 只绑定 `shared-operator-policy`，不创建 home 桶下的 `inbound/outbound/upload` 占位目录。
- `auth.reserved` 增加 `shared-operator-group`、`shared-operator-policy`，防止用户名冲突。

### 8.3 删除用户

现有 `DeleteUser` 行为不变：清理 home 桶数据、解除组成员关系、删除用户。共享操作员用户通常没有 home 数据，清理结果为空属正常。

## 9. 运行时改动

- 所有 `cfg.S3.Bucket` 引用改为 `cfg.S3.HomeBucket`，包括：
  - `internal/server/server.go`
  - `internal/server/handler/admin.go`
  - `internal/server/handler/auth.go`
  - `internal/server/handler/clean.go`
  - `internal/bootstrap/bootstrap.go`
  - `cmd/server/main.go`
- `internal/server/app.App` 仍只保存 home 数据面和 IAM 管理面客户端，不新增共享桶运行时依赖。
- `ft-client`、协议和业务路由不变。

## 10. 兼容与迁移

- 升级时必须把现有 `ft-server.toml` 中的 `bucket` 改为 `home_bucket`，并保留原值，例如：

  ```toml
  [s3]
    home_bucket = "file-transfer"
    shared_bucket = "shared"
    shared_dirs = ["software", "document"]
  ```

- 未修改旧配置时 `ft-server` 启动失败，避免默认切到 `home` 后访问空桶。
- 共享桶是新桶，不涉及历史数据迁移。
- 已完成初始化的环境升级后需要重启 `ft-server` 并再次执行 `ft-admin init`，以创建共享桶、目录和新组策略。

## 11. 测试

### 11.1 配置

- 默认值：`home`、`shared`、`["software", "document"]`。
- 非法桶名、非法目录名、重复目录、home 与 shared 相同均报错。
- 旧 `s3.bucket` 配置报错。
- `SaveServerConfig` 写入并回读新字段。

### 11.2 IAM 策略

- 渲染后的四个原有策略和新增策略均为合法 JSON。
- home 策略中的桶名为 `s3.home_bucket`。
- 用户组策略允许共享桶根浏览和配置目录的 list/get，不允许共享桶 Put/Delete。
- 共享操作员策略包含 list/get/put/delete，包含 `Deny s3:DeleteBucket`，不包含 `admin:*` 和 `ListAllMyBuckets`。
- 未知占位符、残留 `{{`/`}}` 仍然报错。
- 目录列表变化时，渲染出的前缀和 ARN 随之变化。

### 11.3 管理接口

- 非法组仍返回 `invalid_group`。
- `ft-operator-group` 和 `shared-operator-group` 在缺少确认时返回 `operator_confirm_required`。
- `ft-admin user add --group shared-operator` 发送的组名正确。
- 交互式菜单包含共享操作员组选项。

### 11.4 回归

- 运行 `go test -race ./...`。
- 运行 `go vet ./...`。
- 现有 home 桶权限测试全部通过。

## 12. 涉及文件

| 文件 | 改动 |
|---|---|
| `internal/config/config.go` | S3 配置结构、默认值、校验、旧键检测、写配置 |
| `internal/config/config_test.go` | 新配置字段与校验测试 |
| `internal/iam/policies.go` | 策略模板渲染、新策略文件、`PolicyOptions` |
| `internal/iam/iam.go` | 新组常量、`EnsureSharedOperatorGroup`、`SetupUser` 支持新组 |
| `internal/iam/iam_test.go` | 策略内容和占位符测试 |
| `internal/bootstrap/bootstrap.go` | 共享桶、共享目录、共享操作员组初始化 |
| `internal/server/server.go` | 使用 `HomeBucket`，加载策略时传入桶和目录 |
| `internal/server/handler/admin.go` | 允许新组、确认校验、审计 |
| `internal/server/handler/admin_test.go` | 新组确认测试 |
| `internal/server/handler/auth.go` | 使用 `HomeBucket` |
| `internal/server/handler/clean.go` | 使用 `HomeBucket` |
| `internal/auth/auth.go` | 保留用户名增加新组/策略名 |
| `internal/adminapi/client.go` | 不需要改签名，组名由调用方传入 |
| `cmd/admin/main.go` | 菜单、`--group shared-operator`、警告文案 |
| `cmd/server/main.go` | 启动日志输出 home/shared 桶 |
| `policy/user-group.json` | home 占位符、共享桶只读规则 |
| `policy/operator-group.json` | home 占位符 |
| `policy/server-data.json` | home 占位符 |
| `policy/server-admin.json` | home 占位符 |
| `policy/shared-operator-group.json` | 新增共享操作员策略 |
| `config/ft-server.example.toml` | 新配置项 |
| `README.md` | 目录模型、配置、策略、权限矩阵、初始化、迁移 |

## 13. 风险与对策

| 风险 | 对策 |
|---|---|
| 旧配置未迁移导致访问空桶 | 检测 `s3.bucket` 并启动报错 |
| 用户通过共享桶根列出目录外对象 | 根列表只允许 `delimiter=/`，对象读取限定在配置目录 ARN |
| 共享操作员误删桶 | 策略显式 Deny `s3:DeleteBucket` |
| 目录名进入策略后破坏 JSON | 配置阶段严格校验目录名，渲染后再做 JSON 校验 |
| 新组策略未生效 | init 每次都更新策略绑定；升级后要求重启并重新 init |