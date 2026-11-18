# 策略语句 IP 白名单设计

日期：2026-11-18

## 1. 背景与目标

需要为 IAM 策略中的**某条语句（子项）**增加来源 IP 限制。例如：对 `ft-user-policy` 中负责
`arn:aws:s3:::home/${aws:username}/upload/*` 写操作的语句增加 `aws:SourceIp` 条件，
使只有指定网段内的请求可以使用这些权限。

目标：

1. `ft-server` 提供管理接口，按 `(策略名, Sid)` 设置一条或多条 CIDR 网段。
2. `ft-admin` 提供命令行和交互菜单调用该接口。
3. 规则直接写入 `policy/*.json` 模板，保证注册新用户、`ft-admin init`、服务重启后不丢失。

## 2. 范围

只允许修改以下三个由模板管理的组策略：

| 策略名 | 模板文件 | 绑定组 |
|---|---|---|
| `ft-user-policy` | `policy/user-group.json` | `ft-user-group` |
| `ft-operator-policy` | `policy/operator-group.json` | `ft-operator-group` |
| `shared-operator-policy` | `policy/shared-operator-group.json` | `shared-operator-group` |

非目标：

- 不支持修改 `ft-data-policy`、`ft-admin-policy` 及其他任意策略；
- 不支持给 Deny 语句设置 IP 条件；
- 不提供读取当前 IP 规则的独立接口（模板文件即事实来源）。

## 3. 接口契约

### 3.1 请求

```
PUT /v1/admin/policies/:name/statements/:sid/ip-ranges
Authorization: Bearer <admin token>
Content-Type: application/json
```

请求体：

```json
{ "cidrs": ["10.0.0.0/8", "192.168.1.0/24"] }
```

- `cidrs` 字段必须显式提供；
- `cidrs: []` 表示清除该语句的 IP 条件；
- 每条网段使用 `net/netip.ParsePrefix` 校验并规范化为掩码形式（`10.0.0.1/8` → `10.0.0.0/8`），
  自动去重，最多 100 条，支持 IPv4 和 IPv6；空字符串会被拒绝。

### 3.2 响应

```json
{ "policy": "ft-user-policy", "sid": "UploadWrite", "cidrs": ["10.0.0.0/8"] }
```

### 3.3 错误码

| 场景 | HTTP | code |
|---|---|---|
| 请求体非法、`cidrs` 缺失 | 400 | `invalid_request` |
| CIDR 格式非法 | 400 | `invalid_cidr` |
| 策略不在允许范围 | 400 | `unsupported_policy` |
| 目标是 `ft-data-policy` / `ft-admin-policy` | 403 | `service_policy` |
| 策略中不存在该 Sid | 400 | `sid_not_found` |
| Sid 对应的语句不是 Allow | 400 | `statement_not_allow` |
| 同一策略中 Sid 重复 | 500 | `invalid_policy` |
| 策略目录不可写 | 500 | `policy_write_failed` |
| 模板已写入但下发 RustFS 失败 | 502 | `apply_failed` |
| RustFS 不可用 | 502 | `rustfs_unavailable` |

所有管理接口沿用现有 `AdminAuth` + `RequireReady` 中间件。修改成功后写入审计日志，
`Action = "admin.policy.ip.set"`，`Target = "<policy>/<sid>"`。

## 4. 策略条件语义

对 `Sid` 匹配且 `Effect == "Allow"` 的语句，合并以下条件：

```json
"Condition": {
  "IpAddress": {
    "aws:SourceIp": ["10.0.0.0/8", "192.168.1.0/24"]
  }
}
```

规则：

- 保留语句原有的其他 `Condition` 键和值；
- 已存在 `IpAddress` 时只替换 `aws:SourceIp`，保留 `IpAddress` 下的其他键；
- `cidrs` 为空时删除 `aws:SourceIp`；`IpAddress` 变空则删除 `IpAddress`；`Condition` 变空则删除 `Condition`；
- 只修改目标语句，其他语句原样保留；
- 重新序列化使用 `json.MarshalIndent`（2 空格缩进），键顺序会按字母排序。

## 5. 模板改动

为三个模板的每条语句增加唯一 `Sid`。Sid 在同一策略内唯一，命名如下：

### `ft-user-policy`（`policy/user-group.json`）

| Sid | 用途 |
|---|---|
| `HomeBucketMetadata` | home 桶元数据读取 |
| `ListOwnDirs` | 列出自己的目录 |
| `UploadWrite` | 自己的 `upload/` 目录写/删 |
| `UploadMarkerProtect` | 保护 `upload/.keep` |
| `SharedBucketMetadata` | shared 桶元数据读取 |
| `SharedRootList` | 列出 shared 根目录 |
| `SharedDirList` | 列出 shared 配置目录 |
| `SharedRead` | 读取 shared 配置目录 |

### `ft-operator-policy`（`policy/operator-group.json`）

| Sid | 用途 |
|---|---|
| `ListAllBuckets` | 列出所有桶 |
| `HomeBucketMetadata` | home 桶元数据读取 |
| `ListRootDirs` | 列出根目录和 inbound/outbound |
| `ListInboundRecursive` | 递归列出 inbound |
| `UploadDirDeny` | 禁止列出 upload |
| `OutsideInboundDeny` | 禁止列出 inbound 之外的目录 |
| `InboundWrite` | 所有用户 inbound 写/删 |
| `InboundMarkerProtect` | 保护 inbound `.keep` |
| `OutboundRead` | 读取所有用户 outbound |

### `shared-operator-policy`（`policy/shared-operator-group.json`）

| Sid | 用途 |
|---|---|
| `SharedBucketMetadata` | shared 桶元数据读取 |
| `SharedBucketList` | 列出 shared 桶与分片 |
| `SharedMultipart` | 分片上传管理 |
| `SharedRead` | 读取 shared 对象 |
| `SharedWrite` | 写/删 shared 对象 |
| `SharedMarkerProtect` | 保护共享目录 `.keep` |
| `SharedBucketDeleteDeny` | 禁止删除 shared 桶 |

模板升级后，已有部署需重新执行一次 `ft-admin init`，或直接通过新接口设置一次（接口会应用含 Sid 的新模板）。

## 6. 服务端实现

### 6.1 `internal/iam`

- `policies.go`
  - 新增策略名到模板文件的映射（只含三个组策略），以及策略名到绑定组的映射；
  - 新增纯函数 `SetStatementSourceIP(policy []byte, sid string, cidrs []string) ([]byte, error)`：
    解析策略、按 Sid 定位语句、合并/清除条件、重新序列化；不涉及文件或 RustFS；
  - 新增 `NormalizeCIDRs([]string) ([]string, error)`；
  - 导出 `RenderPolicy`（现有 `renderPolicy` 的包装），供接口修改模板后重新渲染；
  - 定义 `ErrUnsupportedPolicy`、`ErrServicePolicy`、`ErrSIDNotFound`、`ErrStatementNotAllow`、
    `ErrInvalidCIDR`、`ErrInvalidPolicy`、`ErrPolicyWrite` 哨兵错误。
- `iam.go`
  - `Manager` 增加 `PolicyDir string` 字段；
  - 新增 `SetStatementIPRanges(ctx, policyName, sid string, cidrs []string, opts PolicyOptions) ([]string, error)`：
    1. 校验策略名并规范化 CIDR；
    2. 获取包级 `policyWriteMu`（与 `ReplacePolicy` 共享），串行化所有策略写操作；
    3. 先找出绑定该策略的用户和组（`ListUsers` + `ListGroups`/`GetGroupDescription`），
       支持逗号分隔的多策略字段；组描述读取失败直接返回错误，不静默跳过；
    4. 读取模板文件，调用 `SetStatementSourceIP`；
    5. 用 `PolicyOptions` 渲染并校验 JSON；
    6. 原子写回模板文件（同目录临时文件 + `rename`，保留原权限）；
    7. 更新内存中的渲染结果（见 6.2）；
    8. 用脱离请求取消的 30 秒上下文调用内部 `replacePolicy` 立即下发；
       失败返回 `ErrApplyFailed`，表示规则已持久化但未下发；
    9. 返回规范化后的 CIDR 列表。
  - `ReplacePolicy` 改为获取 `policyWriteMu` 后调用内部 `replacePolicy`，
    保证用户注册、`ft-admin init` 与本接口不会并发交叉写同一策略。
  - 下发的策略内容取自已修改模板的渲染结果，因此会同时补上模板中的 Sid；旧的、无 Sid 的线上策略
    也会被新模板覆盖（与现有 `ft-admin init` 行为一致）。
  - `applyGroupPolicy` 改为从带锁的内存文档读取模板，而不是直接读 `m.Policies` 字段；
    `groupSpec` 去掉 `doc` 函数，统一按策略名取文档。
- `bootstrap.Run` 中直接下发 `policies.Group` 的位置改为调用 `mgr.Policies.Document(iam.UserGroupPol)`，
  保证初始化时也使用带锁的最新模板。
- `Policies` 增加 `sync.RWMutex` 及方法 `Document(name string) ([]byte, error)`、`SetDocument(name string, doc []byte)`，
  供接口写入、用户注册/初始化读取，避免并发读写竞争。

### 6.2 内存同步

`Policies` 保存的是**渲染后**的策略。接口修改模板后必须用相同的 `PolicyOptions`
（来自 `h.Cfg.S3.HomeBucket`、`SharedBucket`、`SharedDirs`）重新渲染，再通过 `SetDocument` 更新内存。
这样后续用户注册、`ft-admin init` 使用的都是带 IP 条件的新模板，无需重启。

### 6.3 HTTP 层

- `internal/protocol/protocol.go`：新增请求/响应结构体；
  请求中 `Cidrs *[]string` 以区分“缺失”和“空数组”。
- `internal/server/handler/admin.go`：新增 `AdminSetStatementIPRanges`，
  负责请求校验、错误映射、审计日志。
- `internal/server/router/router.go`：
  ```go
  protected.PUT("/policies/:name/statements/:sid/ip-ranges", middleware.RequireReady(a), h.AdminSetStatementIPRanges)
  ```

### 6.4 移除旧版单用户策略逻辑

- 删除 `iam.UserPolicyName` 及 `TestUserPolicyName`；
- `DeleteUser` 不再解绑/删除 `ft-user-<username>-policy`；
- `internal/server/handler/auth.go` 的 `isRegisteredUser` 只保留组成员判断；
- README 中相关历史说明同步清理。

## 7. ft-admin

### 7.1 命令

```sh
# 设置（多个网段可用空格或逗号分隔）
ft-admin policy ip ft-user-policy UploadWrite 10.0.0.0/8 192.168.1.0/24
ft-admin policy ip ft-user-policy UploadWrite 10.0.0.0/8,192.168.1.0/24

# 清除
ft-admin policy ip ft-user-policy UploadWrite --clear

# 跳过确认
ft-admin policy ip ft-user-policy UploadWrite 10.0.0.0/8 --yes
```

- 参数：`<policy-name> <sid> [cidr...]`，策略名和 Sid 必填；
- 每个位置参数可包含逗号分隔的多个网段；
- 非交互模式下必须提供网段或 `--clear`，二者不能同时使用；
- 交互模式下缺少网段时提示输入；完整的三项提示在菜单流程中提供；
- 执行前显示策略、Sid、网段并要求确认。

### 7.2 菜单

主菜单新增“设置策略 IP 白名单”，依次提示策略（三个组策略选择）、Sid、网段。

### 7.3 客户端

- `internal/adminapi/client.go`：新增 `SetStatementIPRanges(ctx, policy, sid string, cidrs []string)`；
- 服务端错误沿用 `APIError` 展示。

## 8. 部署与文档

- `docker-compose.yml`：`./policy:/etc/file-transfer/policy:ro` 改为可写挂载；
- README 增加：
  - 新接口说明与错误码；
  - `ft-admin policy ip` 用法；
  - 三个策略的 Sid 对照表；
  - 策略目录必须可写；
  - 升级覆盖 `policy/*.json` 会丢失 IP 规则，升级前需保留/合并。

## 9. 测试

- `internal/iam`：
  - `SetStatementSourceIP` 表驱动测试：新增、替换、清除、保留其他条件、Deny 拒绝、Sid 不存在、Sid 重复、非法 JSON；
  - `NormalizeCIDRs`：IPv4/IPv6、非法值、去重、空列表；
  - 三个模板的 Sid 唯一性及关键 Sid 存在性。
- `internal/server/handler`：缺字段、非法 CIDR、非法策略、服务策略拒绝。
- `internal/server/router`：新路由未认证返回 401。
- `internal/adminapi`：请求编码与服务端错误解析（使用 `httptest`）。
- 全量验证：`go test -race ./...`、`make build`。

## 10. 风险

- RustFS 文档列出了 `IpAddress` 条件运算符，但未明确列出 `aws:SourceIp` 上下文键。
  实现采用 AWS 标准键名，必须在目标 RustFS 版本上实测：用白名单外地址访问应被拒。
- RustFS 文档未明确提到 `Sid`，但 MinIO 的策略解析库支持；同样需要在目标版本上实测，
  确认带 Sid 的策略可以正常加载和生效。
- 直接修改模板会让工作区出现未提交改动；升级覆盖模板会丢失规则，已在文档中提示。
- 替换策略时存在极短的解绑/重绑窗口，期间相关组可能短暂无权限；属管理操作可接受。
- 模板写回后下发失败时，本地模板已是新规则而线上策略可能仍是旧规则；接口返回 `apply_failed`，
  重试本接口或执行 `ft-admin init` 可重新下发并自愈。