# home 桶、共享桶与共享操作员组实现计划

> 依据：`doc/specs/2026-11-17-home-shared-buckets-design.md`

**目标：** 增加可配置的 home 桶、共享桶及 `shared-operator-group`，并保持现有摆渡流程不变。

**技术栈：** Go、BurntSushi/toml、minio-go、madmin-go、Gin。

---

## 任务 1：配置模型与校验

- [x] 修改 `internal/config/config.go`：`S3Section` 增加 `HomeBucket`、`SharedBucket`、`SharedDirs`，移除 `Bucket`。
- [x] 更新默认值、`applyServerEnv`、`SaveServerConfig`。
- [x] 增加桶名/目录名校验、home 与 shared 不能相同、旧 `s3.bucket` 检测。
- [x] 更新 `internal/config/config_test.go`。
- [x] 更新 `config/ft-server.example.toml`。

验证：`go test ./internal/config/...`

## 任务 2：策略模板与策略文件

- [x] 修改 `internal/iam/policies.go`：`PolicyOptions`、占位符渲染、`SharedOperator` 字段、加载 `shared-operator-group.json`。
- [x] 更新 `policy/user-group.json`：home 占位符 + 共享桶只读规则。
- [x] 更新 `policy/operator-group.json`、`server-data.json`、`server-admin.json`：home 占位符。
- [x] 新增 `policy/shared-operator-group.json`。
- [x] 更新 `internal/iam/iam_test.go`。

验证：`go test ./internal/iam/...`

## 任务 3：IAM 组与 init

- [x] `internal/iam/iam.go`：新增 `SharedAdminGroup`、`SharedAdminPol`、`EnsureSharedOperatorGroup`、`SetupUser` 支持新组。
- [x] `internal/bootstrap/bootstrap.go`：创建共享桶、共享目录、共享操作员组；所有 home 引用改为 `HomeBucket`。
- [x] `internal/auth/auth.go`：保留名增加新组/策略名。
- [x] `internal/server/server.go`：使用 `HomeBucket`，加载策略时传入 `PolicyOptions`。
- [x] `internal/server/handler/{admin,auth,clean}.go`、`cmd/server/main.go`：使用 `HomeBucket`。

验证：`go test ./internal/... ./cmd/...`

## 任务 4：ft-admin 组选项

- [x] `cmd/admin/main.go`：菜单和 `--group shared-operator` 映射，高权限警告。
- [x] `internal/server/handler/admin.go`：允许新组、确认校验、审计动作。
- [x] `internal/server/handler/admin_test.go`：新组确认测试。

验证：`go test ./cmd/admin/... ./internal/server/...`

## 任务 5：文档

- [x] 更新 `README.md` 的桶结构、配置项、策略表、init、权限矩阵、迁移说明。

## 任务 6：整体验证

- [x] `go test -race ./...`
- [x] `go vet ./...`