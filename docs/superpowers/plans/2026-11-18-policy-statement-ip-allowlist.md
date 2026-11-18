# 策略语句 IP 白名单 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为三个组策略中的指定语句（Sid）增加 `aws:SourceIp` 条件，并通过 ft-server 管理接口和 ft-admin 命令设置，规则持久化到 `policy/*.json`。

**Architecture:** 策略模板为每条语句增加唯一 `Sid`；新增 `internal/iam/iprules.go` 负责 CIDR 规范化、按 Sid 合并条件、模板写回与内存同步；HTTP 层新增 `PUT /v1/admin/policies/:name/statements/:sid/ip-ranges`；ft-admin 新增 `policy ip` 子命令和菜单项。

**Tech Stack:** Go 1.26、Gin、cobra、madmin-go、`net/netip`、标准库 `testing`。

---

## 前置：创建分支

- [ ] **Step 0: 创建功能分支**

```bash
git switch -c feat/policy-ip-allowlist
```

Expected: `Switched to a new branch 'feat/policy-ip-allowlist'`

---

### Task 1: 为策略模板添加 Sid

**Files:**
- Modify: `policy/user-group.json`
- Modify: `policy/operator-group.json`
- Modify: `policy/shared-operator-group.json`
- Test: `internal/iam/iam_test.go`

- [ ] **Step 1: 写失败的测试**

在 `internal/iam/iam_test.go` 末尾追加：

```go
func TestPolicyTemplateSIDs(t *testing.T) {
	expected := map[string][]string{
		policyUserGroupFile: {
			"HomeBucketMetadata", "ListOwnDirs", "UploadWrite", "UploadMarkerProtect",
			"SharedBucketMetadata", "SharedRootList", "SharedDirList", "SharedRead",
		},
		policyOperatorFile: {
			"ListAllBuckets", "HomeBucketMetadata", "ListRootDirs", "ListInboundRecursive",
			"UploadDirDeny", "OutsideInboundDeny", "InboundWrite", "InboundMarkerProtect", "OutboundRead",
		},
		policySharedOperatorFile: {
			"SharedBucketMetadata", "SharedBucketList", "SharedMultipart",
			"SharedRead", "SharedWrite", "SharedMarkerProtect", "SharedBucketDeleteDeny",
		},
	}
	for file, want := range expected {
		raw, err := os.ReadFile(filepath.Join("..", "..", "policy", file))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Statement []struct {
				Sid string `json:"Sid"`
			} `json:"Statement"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		seen := map[string]bool{}
		for i, st := range doc.Statement {
			if st.Sid == "" {
				t.Fatalf("%s 第 %d 条语句缺少 Sid", file, i+1)
			}
			if seen[st.Sid] {
				t.Fatalf("%s Sid 重复: %s", file, st.Sid)
			}
			seen[st.Sid] = true
		}
		for _, sid := range want {
			if !seen[sid] {
				t.Fatalf("%s 缺少 Sid %s", file, sid)
			}
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run TestPolicyTemplateSIDs -v
```

Expected: FAIL，提示 `policy/user-group.json 第 1 条语句缺少 Sid`。

- [ ] **Step 3: 给三个模板的每条语句加 Sid**

在每个 Statement 对象的第一个字段之前加 `"Sid": "<值>",`。按语句顺序：

`policy/user-group.json`：

| 语句序号 | Sid |
|---|---|
| 1 | `HomeBucketMetadata` |
| 2 | `ListOwnDirs` |
| 3 | `UploadWrite` |
| 4 | `UploadMarkerProtect` |
| 5 | `SharedBucketMetadata` |
| 6 | `SharedRootList` |
| 7 | `SharedDirList` |
| 8 | `SharedRead` |

`policy/operator-group.json`：

| 语句序号 | Sid |
|---|---|
| 1 | `ListAllBuckets` |
| 2 | `HomeBucketMetadata` |
| 3 | `ListRootDirs` |
| 4 | `ListInboundRecursive` |
| 5 | `UploadDirDeny` |
| 6 | `OutsideInboundDeny` |
| 7 | `InboundWrite` |
| 8 | `InboundMarkerProtect` |
| 9 | `OutboundRead` |

`policy/shared-operator-group.json`：

| 语句序号 | Sid |
|---|---|
| 1 | `SharedBucketMetadata` |
| 2 | `SharedBucketList` |
| 3 | `SharedMultipart` |
| 4 | `SharedRead` |
| 5 | `SharedWrite` |
| 6 | `SharedMarkerProtect` |
| 7 | `SharedBucketDeleteDeny` |

例如 `policy/user-group.json` 第 3 条语句修改为：

```json
{
  "Sid": "UploadWrite",
  "Action": [
    "s3:PutObject",
    "s3:AbortMultipartUpload",
    "s3:ListMultipartUploadParts",
    "s3:PutObjectTagging",
    "s3:DeleteObject",
    "s3:DeleteObjectVersion"
  ],
  "Effect": "Allow",
  "Resource": [
    "arn:aws:s3:::{{HOME_BUCKET}}/${aws:username}/upload/*"
  ]
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run TestPolicyTemplateSIDs -v
```

Expected: PASS

- [ ] **Step 5: 运行整个 iam 包测试，确认加 Sid 没破坏现有断言**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -v
```

Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add policy/ internal/iam/iam_test.go
git commit -m "feat: 为组策略语句添加 Sid"
```

---

### Task 2: 移除旧版单用户策略逻辑

**Files:**
- Modify: `internal/iam/iam.go:108`（删除 `UserPolicyName`）
- Modify: `internal/iam/iam.go:353-376`（`DeleteUser`）
- Modify: `internal/server/handler/auth.go:182-196`（`isRegisteredUser`）
- Test: `internal/iam/iam_test.go`（删除 `TestUserPolicyName`）

- [ ] **Step 1: 删除 `UserPolicyName`**

删除 `internal/iam/iam.go` 中的这一行：

```go
func UserPolicyName(user string) string { return "ft-user-" + user + "-policy" }
```

- [ ] **Step 2: 清理 `DeleteUser`**

把 `DeleteUser` 中的：

```go
	oldPolicy := UserPolicyName(user)
	_ = m.Detach(ctx, oldPolicy, user, "")
	for _, g := range info.MemberOf {
		_ = m.RemoveFromGroup(ctx, g, user)
	}
	if err := m.Admin.RemoveUser(ctx, user); err != nil {
		return true, n, err
	}
	_ = m.Admin.RemoveCannedPolicy(ctx, oldPolicy)
	return true, n, nil
```

改为：

```go
	for _, g := range info.MemberOf {
		_ = m.RemoveFromGroup(ctx, g, user)
	}
	if err := m.Admin.RemoveUser(ctx, user); err != nil {
		return true, n, err
	}
	return true, n, nil
```

- [ ] **Step 3: 简化 `isRegisteredUser`**

把 `internal/server/handler/auth.go` 中的：

```go
	if info.PolicyName == iam.UserPolicyName(username) {
		return true
	}
	for _, g := range info.MemberOf {
		if g == iam.UserGroup {
			return true
		}
	}
	return false
```

改为：

```go
	for _, g := range info.MemberOf {
		if g == iam.UserGroup {
			return true
		}
	}
	return false
```

- [ ] **Step 4: 删除测试**

删除 `internal/iam/iam_test.go` 中的：

```go
func TestUserPolicyName(t *testing.T) {
	if got := UserPolicyName("alice"); got != "ft-user-alice-policy" {
		t.Fatalf("got %s", got)
	}
}
```

- [ ] **Step 5: 确认没有残留引用**

```bash
rg -n 'UserPolicyName|ft-user-.*-policy' --type go .
```

Expected: 无输出。

- [ ] **Step 6: 运行相关测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam ./internal/server/... -v
```

Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/iam/iam.go internal/iam/iam_test.go internal/server/handler/auth.go
git commit -m "refactor: 移除旧版单用户策略逻辑"
```

---

### Task 3: 新增 CIDR 规范化与错误定义

**Files:**
- Create: `internal/iam/iprules.go`
- Test: `internal/iam/iprules_test.go`

- [ ] **Step 1: 写失败的测试**

创建 `internal/iam/iprules_test.go`：

```go
package iam

import (
	"errors"
	"testing"
)

func TestNormalizeCIDRs(t *testing.T) {
	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "10.0.0.0/8"
	}
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"empty", nil, []string{}, false},
		{"ipv4", []string{"10.0.0.0/8", "192.168.1.0/24"}, []string{"10.0.0.0/8", "192.168.1.0/24"}, false},
		{"canonicalize", []string{"10.0.0.1/8"}, []string{"10.0.0.0/8"}, false},
		{"dedupe", []string{"10.0.0.1/8", "10.0.0.0/8"}, []string{"10.0.0.0/8"}, false},
		{"trim", []string{"  10.0.0.0/8  "}, []string{"10.0.0.0/8"}, false},
		{"ipv6", []string{"2001:db8::1/32"}, []string{"2001:db8::/32"}, false},
		{"empty value", []string{""}, nil, true},
		{"invalid", []string{"bad"}, nil, true},
		{"too many", tooMany, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeCIDRs(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidCIDR) {
					t.Fatalf("err = %v, want ErrInvalidCIDR", err)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got = %v, want = %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got = %v, want = %v", got, tt.want)
				}
			}
		})
	}
}

func TestEditablePolicyHelpers(t *testing.T) {
	for _, name := range []string{UserGroupPol, AdminGroupPol, SharedAdminPol} {
		if !IsEditablePolicy(name) {
			t.Fatalf("%s 应允许编辑", name)
		}
		if IsServicePolicy(name) {
			t.Fatalf("%s 不应是服务策略", name)
		}
	}
	for _, name := range []string{DataPolicy, ServerAdminPol} {
		if IsEditablePolicy(name) {
			t.Fatalf("%s 不应允许编辑", name)
		}
		if !IsServicePolicy(name) {
			t.Fatalf("%s 应是服务策略", name)
		}
	}
	if IsEditablePolicy("custom-policy") {
		t.Fatal("未知策略不应允许编辑")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run 'TestNormalizeCIDRs|TestEditablePolicyHelpers' -v
```

Expected: FAIL，编译错误 `undefined: NormalizeCIDRs`。

- [ ] **Step 3: 创建 `internal/iam/iprules.go` 骨架**

```go
package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxCIDRs = 100

var (
	ErrUnsupportedPolicy = errors.New("策略不支持 IP 白名单设置")
	ErrServicePolicy     = errors.New("服务身份策略不允许修改")
	ErrSIDNotFound       = errors.New("策略中不存在指定 Sid")
	ErrStatementNotAllow = errors.New("指定 Sid 不是 Allow 语句")
	ErrInvalidCIDR       = errors.New("无效的 IP 网段")
	ErrInvalidPolicy     = errors.New("策略内容无效")
	ErrPolicyWrite       = errors.New("写入策略文件失败")
)

// editablePolicies 是允许设置 IP 白名单的策略到模板文件和绑定组的映射。
var editablePolicies = map[string]struct {
	file  string
	group string
}{
	UserGroupPol:   {policyUserGroupFile, UserGroup},
	AdminGroupPol:  {policyOperatorFile, AdminGroup},
	SharedAdminPol: {policySharedOperatorFile, SharedAdminGroup},
}

// EditablePolicies 返回允许设置 IP 白名单的策略名。
func EditablePolicies() []string {
	return []string{UserGroupPol, AdminGroupPol, SharedAdminPol}
}

// IsEditablePolicy 返回 policyName 是否允许设置 IP 白名单。
func IsEditablePolicy(policyName string) bool {
	_, ok := editablePolicies[policyName]
	return ok
}

// IsServicePolicy 返回 policyName 是否为服务身份策略。
func IsServicePolicy(policyName string) bool {
	return policyName == DataPolicy || policyName == ServerAdminPol
}

// NormalizeCIDRs 校验并规范化网段列表，去掉重复项。
func NormalizeCIDRs(cidrs []string) ([]string, error) {
	if len(cidrs) > maxCIDRs {
		return nil, fmt.Errorf("%w: 最多 %d 条", ErrInvalidCIDR, maxCIDRs)
	}
	out := make([]string, 0, len(cidrs))
	seen := make(map[string]struct{}, len(cidrs))
	for _, raw := range cidrs {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("%w: 不能为空", ErrInvalidCIDR)
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidCIDR, value)
		}
		normalized := prefix.Masked().String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

// 后续任务会在这里补充 SetStatementSourceIP 与模板写回逻辑；先保留占位导入。
var (
	_ = context.Background
	_ = json.Marshal
	_ = os.ReadFile
	_ = filepath.Join
	_ = sync.Mutex{}
)
```

> 说明：最后的占位变量只是为了让本任务的骨架能编译；Task 4 会删除它们并补充正式实现。

- [ ] **Step 4: 运行测试确认通过**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run 'TestNormalizeCIDRs|TestEditablePolicyHelpers' -v
```

Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/iam/iprules.go internal/iam/iprules_test.go
git commit -m "feat: 添加 CIDR 规范化与可编辑策略定义"
```

---

### Task 4: 实现按 Sid 合并 `aws:SourceIp` 条件

**Files:**
- Modify: `internal/iam/iprules.go`
- Test: `internal/iam/iprules_test.go`

- [ ] **Step 1: 写失败的测试**

在 `internal/iam/iprules_test.go` 追加：

```go
type parsedStatement struct {
	Sid       string                    `json:"Sid"`
	Effect    string                    `json:"Effect"`
	Condition map[string]map[string]any `json:"Condition"`
}

func parseStatements(t *testing.T, raw []byte) map[string]parsedStatement {
	t.Helper()
	var doc struct {
		Statement []parsedStatement `json:"Statement"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]parsedStatement{}
	for _, st := range doc.Statement {
		out[st.Sid] = st
	}
	return out
}

func sourceIPs(t *testing.T, st parsedStatement) []string {
	t.Helper()
	ipAddress := st.Condition["IpAddress"]
	if ipAddress == nil {
		return nil
	}
	raw, ok := ipAddress["aws:SourceIp"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("aws:SourceIp 值不是字符串: %#v", v)
		}
		out = append(out, s)
	}
	return out
}

const testPolicy = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "UploadWrite",
      "Effect": "Allow",
      "Action": ["s3:PutObject", "s3:DeleteObject"],
      "Resource": ["arn:aws:s3:::home/${aws:username}/upload/*"]
    },
    {
      "Sid": "SharedRead",
      "Effect": "Allow",
      "Action": ["s3:GetObject"],
      "Resource": ["arn:aws:s3:::shared/*"],
      "Condition": {"StringLike": {"s3:prefix": ["a/*"]}, "IpAddress": {"aws:SourceVpc": ["vpc-1"]}}
    },
    {
      "Sid": "Protect",
      "Effect": "Deny",
      "Action": ["s3:DeleteObject"],
      "Resource": ["arn:aws:s3:::home/*/.keep"]
    }
  ]
}`

func TestSetStatementSourceIPAddsAndPreserves(t *testing.T) {
	updated, err := SetStatementSourceIP([]byte(testPolicy), "UploadWrite", []string{"10.0.0.0/8", "192.168.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	st := parseStatements(t, updated)
	got := sourceIPs(t, st["UploadWrite"])
	if len(got) != 2 || got[0] != "10.0.0.0/8" || got[1] != "192.168.1.0/24" {
		t.Fatalf("aws:SourceIp = %v", got)
	}
	if prefix := st["SharedRead"].Condition["StringLike"]["s3:prefix"]; prefix == nil {
		t.Fatal("其他语句的 Condition 被修改")
	}
	if st["Protect"].Condition != nil {
		t.Fatal("Deny 语句不应被修改")
	}
}

func TestSetStatementSourceIPKeepsOtherIPAddressKeys(t *testing.T) {
	updated, err := SetStatementSourceIP([]byte(testPolicy), "SharedRead", []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	st := parseStatements(t, updated)
	if len(sourceIPs(t, st["SharedRead"])) != 1 {
		t.Fatal("未添加 aws:SourceIp")
	}
	if vpc := st["SharedRead"].Condition["IpAddress"]["aws:SourceVpc"]; vpc == nil {
		t.Fatal("aws:SourceVpc 被删除")
	}
	if st["SharedRead"].Condition["StringLike"] == nil {
		t.Fatal("StringLike 条件被删除")
	}
}

func TestSetStatementSourceIPReplaces(t *testing.T) {
	first, err := SetStatementSourceIP([]byte(testPolicy), "UploadWrite", []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := SetStatementSourceIP(first, "UploadWrite", []string{"172.16.0.0/12"})
	if err != nil {
		t.Fatal(err)
	}
	got := sourceIPs(t, parseStatements(t, second)["UploadWrite"])
	if len(got) != 1 || got[0] != "172.16.0.0/12" {
		t.Fatalf("aws:SourceIp = %v", got)
	}
}

func TestSetStatementSourceIPClears(t *testing.T) {
	first, err := SetStatementSourceIP([]byte(testPolicy), "SharedRead", []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := SetStatementSourceIP(first, "SharedRead", nil)
	if err != nil {
		t.Fatal(err)
	}
	st := parseStatements(t, cleared)["SharedRead"]
	if sourceIPs(t, st) != nil {
		t.Fatal("aws:SourceIp 未清除")
	}
	if st.Condition["IpAddress"]["aws:SourceVpc"] == nil {
		t.Fatal("aws:SourceVpc 不应被清除")
	}
	if st.Condition["StringLike"] == nil {
		t.Fatal("StringLike 不应被清除")
	}
}

func TestSetStatementSourceIPClearsEmptyCondition(t *testing.T) {
	first, err := SetStatementSourceIP([]byte(testPolicy), "UploadWrite", []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := SetStatementSourceIP(first, "UploadWrite", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := parseStatements(t, cleared)["UploadWrite"]; st.Condition != nil {
		t.Fatalf("Condition 未清理: %#v", st.Condition)
	}
}

func TestSetStatementSourceIPErrors(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		sid    string
		want   error
	}{
		{"unknown sid", testPolicy, "Missing", ErrSIDNotFound},
		{"deny statement", testPolicy, "Protect", ErrStatementNotAllow},
		{"malformed json", `{"Version":`, "UploadWrite", ErrInvalidPolicy},
		{"statement not array", `{"Version":"2012-10-17","Statement":{}}`, "UploadWrite", ErrInvalidPolicy},
		{"duplicate sid", `{"Version":"2012-10-17","Statement":[{"Sid":"X","Effect":"Allow"},{"Sid":"X","Effect":"Allow"}]}`, "X", ErrInvalidPolicy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SetStatementSourceIP([]byte(tt.policy), tt.sid, []string{"10.0.0.0/8"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run TestSetStatementSourceIP -v
```

Expected: FAIL，`undefined: SetStatementSourceIP`。

- [ ] **Step 3: 实现 `SetStatementSourceIP` 并删除占位变量**

在 `internal/iam/iprules.go` 中，把文件末尾的占位变量块：

```go
// 后续任务会在这里补充 SetStatementSourceIP 与模板写回逻辑；先保留占位导入。
var (
	_ = context.Background
	_ = json.Marshal
	_ = os.ReadFile
	_ = filepath.Join
	_ = sync.Mutex{}
)
```

替换为：

```go
// SetStatementSourceIP 为指定 Sid 的 Allow 语句设置或清除 aws:SourceIp 条件。
// cidrs 为空时清除条件。
func SetStatementSourceIP(policy []byte, sid string, cidrs []string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(policy, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	rawStatements, ok := doc["Statement"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: Statement 必须是数组", ErrInvalidPolicy)
	}
	matches := 0
	for _, raw := range rawStatements {
		statement, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: Statement 项必须是对象", ErrInvalidPolicy)
		}
		statementSID, _ := statement["Sid"].(string)
		if statementSID != sid {
			continue
		}
		matches++
		effect, _ := statement["Effect"].(string)
		if !strings.EqualFold(effect, "Allow") {
			return nil, ErrStatementNotAllow
		}
		if err := setSourceIPCondition(statement, cidrs); err != nil {
			return nil, err
		}
	}
	if matches == 0 {
		return nil, ErrSIDNotFound
	}
	if matches > 1 {
		return nil, fmt.Errorf("%w: 重复 Sid %q", ErrInvalidPolicy, sid)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	return append(out, '\n'), nil
}

func setSourceIPCondition(statement map[string]any, cidrs []string) error {
	condition := map[string]any{}
	if raw, ok := statement["Condition"]; ok {
		condition, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: Condition 必须是对象", ErrInvalidPolicy)
		}
	}
	ipAddress := map[string]any{}
	if raw, ok := condition["IpAddress"]; ok {
		ipAddress, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: IpAddress 必须是对象", ErrInvalidPolicy)
		}
	}
	if len(cidrs) == 0 {
		delete(ipAddress, "aws:SourceIp")
		if len(ipAddress) == 0 {
			delete(condition, "IpAddress")
		}
	} else {
		values := make([]any, len(cidrs))
		for i, cidr := range cidrs {
			values[i] = cidr
		}
		ipAddress["aws:SourceIp"] = values
		condition["IpAddress"] = ipAddress
	}
	if len(condition) == 0 {
		delete(statement, "Condition")
		return nil
	}
	statement["Condition"] = condition
	return nil
}
```

同时删除不再使用的导入（`context`、`os`、`path/filepath`、`sync` 暂时还会在后续任务使用；如果编译报未使用，先保留到 Task 6 再删）。当前 `iprules.go` 只用到 `encoding/json`、`errors`、`fmt`、`net/netip`、`strings`，请把导入改为：

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run TestSetStatementSourceIP -v
```

Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/iam/iprules.go internal/iam/iprules_test.go
git commit -m "feat: 支持按 Sid 合并 SourceIp 条件"
```

---

### Task 5: 为内存策略文档加锁并统一读取路径

**Files:**
- Modify: `internal/iam/policies.go`
- Modify: `internal/iam/iam.go`
- Modify: `internal/bootstrap/bootstrap.go`
- Test: `internal/iam/iam_test.go`

- [ ] **Step 1: 写失败的测试**

在 `internal/iam/iam_test.go` 追加（文件顶部需要新增 `"errors"` 和 `"sync"` 导入）：

```go
func TestPoliciesDocument(t *testing.T) {
	p := loadTestPolicies(t)
	doc, err := p.Document(UserGroupPol)
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != string(p.Group) {
		t.Fatal("Document 返回值与 Group 不一致")
	}
	updated := []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow","Action":["s3:PutObject"],"Resource":["*"]}]}`)
	if err := p.SetDocument(UserGroupPol, updated); err != nil {
		t.Fatal(err)
	}
	doc, err = p.Document(UserGroupPol)
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != string(updated) {
		t.Fatalf("Document = %s", doc)
	}
	doc[0] = 'x'
	again, _ := p.Document(UserGroupPol)
	if again[0] == 'x' {
		t.Fatal("Document 返回了内部切片")
	}
	if _, err := p.Document(DataPolicy); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("err = %v, want ErrUnsupportedPolicy", err)
	}
	if err := p.SetDocument(ServerAdminPol, updated); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("err = %v, want ErrUnsupportedPolicy", err)
	}
}

func TestPoliciesConcurrentDocumentAccess(t *testing.T) {
	p := loadTestPolicies(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = p.Document(UserGroupPol)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = p.SetDocument(UserGroupPol, []byte(`{"Version":"2012-10-17","Statement":[]}`))
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run 'TestPoliciesDocument|TestPoliciesConcurrent' -v
```

Expected: FAIL，`p.Document undefined`。

- [ ] **Step 3: 给 `Policies` 加锁和方法**

把 `internal/iam/policies.go` 中的：

```go
// Policies 保存从 policy 目录加载的 IAM 策略。
type Policies struct {
	Group          []byte
	Operator       []byte
	SharedOperator []byte
	Data           []byte
	Admin          []byte
}
```

改为：

```go
// Policies 保存从 policy 目录加载的 IAM 策略。
type Policies struct {
	mu             sync.RWMutex
	Group          []byte
	Operator       []byte
	SharedOperator []byte
	Data           []byte
	Admin          []byte
}

// Document 返回指定组策略当前的渲染文档副本。
func (p *Policies) Document(name string) ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch name {
	case UserGroupPol:
		return cloneBytes(p.Group), nil
	case AdminGroupPol:
		return cloneBytes(p.Operator), nil
	case SharedAdminPol:
		return cloneBytes(p.SharedOperator), nil
	default:
		return nil, ErrUnsupportedPolicy
	}
}

// SetDocument 更新指定组策略的渲染文档。
func (p *Policies) SetDocument(name string, doc []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch name {
	case UserGroupPol:
		p.Group = cloneBytes(doc)
	case AdminGroupPol:
		p.Operator = cloneBytes(doc)
	case SharedAdminPol:
		p.SharedOperator = cloneBytes(doc)
	default:
		return ErrUnsupportedPolicy
	}
	return nil
}

func cloneBytes(b []byte) []byte {
	return append([]byte(nil), b...)
}
```

在导入中加入 `"sync"`。

- [ ] **Step 4: 导出 `RenderPolicy`**

在 `internal/iam/policies.go` 的 `renderPolicy` 函数下方加入：

```go
// RenderPolicy 渲染策略模板，供运行时修改策略后重新生成内存文档。
func RenderPolicy(name string, raw []byte, opts PolicyOptions) ([]byte, error) {
	return renderPolicy(name, raw, opts)
}
```

- [ ] **Step 5: 简化 `groupSpec` 和 `applyGroupPolicy`**

把 `internal/iam/iam.go` 中的：

```go
type groupSpec struct {
	policy string
	doc    func(*Policies) []byte
}

// groupSpecs 是用户组到策略的统一映射，新增组时只改这里。
var groupSpecs = map[string]groupSpec{
	UserGroup:        {UserGroupPol, func(p *Policies) []byte { return p.Group }},
	AdminGroup:       {AdminGroupPol, func(p *Policies) []byte { return p.Operator }},
	SharedAdminGroup: {SharedAdminPol, func(p *Policies) []byte { return p.SharedOperator }},
}
```

改为：

```go
type groupSpec struct {
	policy string
}

// groupSpecs 是用户组到策略的统一映射，新增组时只改这里。
var groupSpecs = map[string]groupSpec{
	UserGroup:        {UserGroupPol},
	AdminGroup:       {AdminGroupPol},
	SharedAdminGroup: {SharedAdminPol},
}
```

把 `applyGroupPolicy` 改为：

```go
// applyGroupPolicy 把组对应的策略绑定到该组。
func (m *Manager) applyGroupPolicy(ctx context.Context, group string) error {
	spec := groupSpecs[group]
	doc, err := m.Policies.Document(spec.policy)
	if err != nil {
		return err
	}
	return m.ReplacePolicy(ctx, spec.policy, doc, nil, []string{group})
}
```

- [ ] **Step 6: 让 bootstrap 走 `Document`**

把 `internal/bootstrap/bootstrap.go` 中的：

```go
	if err := mgr.ReplacePolicy(ctx, iam.UserGroupPol, policies.Group, nil, []string{iam.UserGroup}); err != nil {
		return res, fmt.Errorf("更新用户组策略: %w", err)
	}
```

改为：

```go
	userGroupPolicy, err := mgr.Policies.Document(iam.UserGroupPol)
	if err != nil {
		return res, fmt.Errorf("读取用户组策略: %w", err)
	}
	if err := mgr.ReplacePolicy(ctx, iam.UserGroupPol, userGroupPolicy, nil, []string{iam.UserGroup}); err != nil {
		return res, fmt.Errorf("更新用户组策略: %w", err)
	}
```

- [ ] **Step 7: 运行测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam ./internal/bootstrap ./internal/server/... -v
```

Expected: 全部 PASS；`-race` 下并发测试也通过。

- [ ] **Step 8: 提交**

```bash
git add internal/iam/policies.go internal/iam/iam.go internal/iam/iam_test.go internal/bootstrap/bootstrap.go
git commit -m "refactor: 组策略文档统一走加锁读取"
```

---

### Task 6: 实现模板写回与立即下发

**Files:**
- Modify: `internal/iam/iprules.go`
- Modify: `internal/iam/iam.go`（`Manager` 增加 `PolicyDir`）
- Modify: `internal/server/server.go:54`
- Test: `internal/iam/iprules_test.go`

- [ ] **Step 1: 写失败的测试**

在 `internal/iam/iprules_test.go` 追加：

```go
func setupPolicyDir(t *testing.T) (string, PolicyOptions) {
	t.Helper()
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	group := `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "UploadWrite",
      "Effect": "Allow",
      "Action": ["s3:PutObject", "s3:DeleteObject"],
      "Resource": ["arn:aws:s3:::home/${aws:username}/upload/*"]
    },
    {
      "Sid": "SharedRead",
      "Effect": "Allow",
      "Action": ["s3:GetObject"],
      "Resource": ["{{SHARED_DIR_OBJECT_ARNS}}"]
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(group), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, testPolicyOptions()
}

func TestRenderUpdatedPolicyWritesFileAndMemory(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{PolicyDir: dir, Policies: policies}

	normalized, rendered, err := m.renderUpdatedPolicy(UserGroupPol, "UploadWrite", []string{"10.0.0.1/8", "10.0.0.0/8"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || normalized[0] != "10.0.0.0/8" {
		t.Fatalf("normalized = %v", normalized)
	}
	if got := sourceIPs(t, parseStatements(t, rendered)["UploadWrite"]); len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("rendered aws:SourceIp = %v", got)
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, policyUserGroupFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), `"aws:SourceIp"`) {
		t.Fatalf("模板文件未写入条件: %s", onDisk)
	}
	mem, err := policies.Document(UserGroupPol)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mem), `"aws:SourceIp"`) {
		t.Fatalf("内存文档未更新: %s", mem)
	}
	if strings.Contains(string(mem), "{{") {
		t.Fatalf("内存文档未渲染: %s", mem)
	}
}

func TestRenderUpdatedPolicyClears(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{PolicyDir: dir, Policies: policies}
	if _, _, err := m.renderUpdatedPolicy(UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, opts); err != nil {
		t.Fatal(err)
	}
	_, rendered, err := m.renderUpdatedPolicy(UserGroupPol, "UploadWrite", nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	st := parseStatements(t, rendered)["UploadWrite"]
	if st.Condition != nil {
		t.Fatalf("Condition 未清除: %#v", st.Condition)
	}
}

func TestRenderUpdatedPolicyRejects(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{PolicyDir: dir, Policies: policies}
	if _, _, err := m.renderUpdatedPolicy(DataPolicy, "UploadWrite", nil, opts); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := m.renderUpdatedPolicy(UserGroupPol, "Missing", nil, opts); !errors.Is(err, ErrSIDNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := m.renderUpdatedPolicy(UserGroupPol, "UploadWrite", []string{"bad"}, opts); !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetStatementIPRangesRejectsServicePolicy(t *testing.T) {
	m := &Manager{}
	_, err := m.SetStatementIPRanges(context.Background(), DataPolicy, "UploadWrite", []string{"10.0.0.0/8"}, PolicyOptions{})
	if !errors.Is(err, ErrServicePolicy) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/iam -run 'TestRenderUpdatedPolicy|TestSetStatementIPRangesRejects' -v
```

Expected: FAIL，`m.renderUpdatedPolicy undefined`。

- [ ] **Step 3: 实现写回与下发**

在 `internal/iam/iprules.go` 的导入中加入：

```go
	"context"
	"os"
	"path/filepath"
	"sync"
```

在 `SetStatementSourceIP` 之后追加：

```go
// policyUpdateMu 串行化策略模板的读取、修改、写回和下发。
var policyUpdateMu sync.Mutex

// SetStatementIPRanges 修改指定策略模板中的语句并立即下发到 RustFS。
func (m *Manager) SetStatementIPRanges(ctx context.Context, policyName, sid string, cidrs []string, opts PolicyOptions) ([]string, error) {
	if IsServicePolicy(policyName) {
		return nil, ErrServicePolicy
	}
	spec, ok := editablePolicies[policyName]
	if !ok {
		return nil, ErrUnsupportedPolicy
	}
	policyUpdateMu.Lock()
	defer policyUpdateMu.Unlock()

	normalized, rendered, err := m.renderUpdatedPolicy(policyName, sid, cidrs, opts)
	if err != nil {
		return nil, err
	}
	users, groups, err := m.policyAttachments(ctx, policyName, spec.group)
	if err != nil {
		return nil, err
	}
	if err := m.ReplacePolicy(ctx, policyName, rendered, users, groups); err != nil {
		return nil, err
	}
	return normalized, nil
}

// renderUpdatedPolicy 修改模板、原子写回并更新内存文档。
func (m *Manager) renderUpdatedPolicy(policyName, sid string, cidrs []string, opts PolicyOptions) ([]string, []byte, error) {
	spec, ok := editablePolicies[policyName]
	if !ok {
		return nil, nil, ErrUnsupportedPolicy
	}
	normalized, err := NormalizeCIDRs(cidrs)
	if err != nil {
		return nil, nil, err
	}
	if m.PolicyDir == "" {
		return nil, nil, fmt.Errorf("%w: policy 目录未配置", ErrPolicyWrite)
	}
	path := filepath.Join(m.PolicyDir, spec.file)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取策略 %s: %v", path, err)
	}
	updated, err := SetStatementSourceIP(raw, sid, normalized)
	if err != nil {
		return nil, nil, err
	}
	rendered, err := RenderPolicy(path, updated, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if err := validatePolicyJSON(rendered); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if err := writeFileAtomic(path, updated); err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v", ErrPolicyWrite, path, err)
	}
	if m.Policies != nil {
		if err := m.Policies.SetDocument(policyName, rendered); err != nil {
			return nil, nil, err
		}
	}
	return normalized, rendered, nil
}

// policyAttachments 返回绑定指定策略的用户和组，并保证绑定组在列表中。
func (m *Manager) policyAttachments(ctx context.Context, policyName, group string) ([]string, []string, error) {
	if m.Admin == nil {
		return nil, nil, errors.New("RustFS 管理客户端未初始化")
	}
	allUsers, err := m.Admin.ListUsers(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("列出用户: %w", err)
	}
	var users []string
	for name, info := range allUsers {
		if info.PolicyName == policyName {
			users = append(users, name)
		}
	}
	allGroups, err := m.Admin.ListGroups(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("列出组: %w", err)
	}
	groups := []string{group}
	for _, name := range allGroups {
		if name == group {
			continue
		}
		desc, err := m.Admin.GetGroupDescription(ctx, name)
		if err != nil {
			continue
		}
		if desc.Policy == policyName {
			groups = append(groups, name)
		}
	}
	return users, groups, nil
}

// writeFileAtomic 先写同目录临时文件再 rename，避免中途失败留下半个文件。
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [ ] **Step 4: 给 `Manager` 增加 `PolicyDir`**

把 `internal/iam/iam.go` 中的：

```go
type Manager struct {
	Admin         *madmin.AdminClient
	S3            *s3.Manager
	PlaceholderS3 *s3.Manager
	Policies      *Policies
	Bucket        string
}
```

改为：

```go
type Manager struct {
	Admin         *madmin.AdminClient
	S3            *s3.Manager
	PlaceholderS3 *s3.Manager
	Policies      *Policies
	PolicyDir     string
	Bucket        string
}
```

- [ ] **Step 5: 在 server 装配时设置 `PolicyDir`**

在 `internal/server/server.go` 的：

```go
	im.Policies = policies
	im.PlaceholderS3 = data
```

改为：

```go
	im.Policies = policies
	im.PolicyDir = policyDir
	im.PlaceholderS3 = data
```

- [ ] **Step 6: 运行测试**

```bash
GOPROXY=https://goproxy.cn,direct go test -race ./internal/iam -run 'TestRenderUpdatedPolicy|TestSetStatementIPRangesRejects' -v
```

Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/iam/iprules.go internal/iam/iam.go internal/server/server.go internal/iam/iprules_test.go
git commit -m "feat: 写回策略模板并立即下发 RustFS"
```

---

### Task 7: 新增协议结构与 adminapi 客户端

**Files:**
- Modify: `internal/protocol/protocol.go`
- Modify: `internal/adminapi/client.go`
- Test: `internal/adminapi/client_test.go`

- [ ] **Step 1: 写失败的测试**

在 `internal/adminapi/client_test.go` 追加（导入中加入 `"github.com/rfancn/file-transfer/internal/protocol"`）：

```go
func TestSetStatementIPRanges(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	var gotCidrs []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		var req protocol.SetStatementIPRangesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Cidrs != nil {
			gotCidrs = *req.Cidrs
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.SetStatementIPRangesResponse{
			Policy: "ft-user-policy",
			Sid:    "UploadWrite",
			Cidrs:  gotCidrs,
		})
	}))
	defer ts.Close()

	c, err := New(config.ClientSection{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	res, err := c.SetStatementIPRanges(context.Background(), "ft-user-policy", "UploadWrite", []string{"10.0.0.0/8", "192.168.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer admin-token" || gotMethod != http.MethodPut {
		t.Fatalf("auth=%q method=%q", gotAuth, gotMethod)
	}
	if gotPath != "/v1/admin/policies/ft-user-policy/statements/UploadWrite/ip-ranges" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(gotCidrs) != 2 || gotCidrs[0] != "10.0.0.0/8" {
		t.Fatalf("cidrs = %v", gotCidrs)
	}
	if res.Policy != "ft-user-policy" || res.Sid != "UploadWrite" || len(res.Cidrs) != 2 {
		t.Fatalf("res = %+v", res)
	}
}

func TestSetStatementIPRangesClearSendsEmptyArray(t *testing.T) {
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.SetStatementIPRangesResponse{Policy: "ft-user-policy", Sid: "UploadWrite", Cidrs: []string{}})
	}))
	defer ts.Close()

	c, err := New(config.ClientSection{URL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	if _, err := c.SetStatementIPRanges(context.Background(), "ft-user-policy", "UploadWrite", nil); err != nil {
		t.Fatal(err)
	}
	if gotBody != `{"cidrs":[]}` {
		t.Fatalf("body = %s", gotBody)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/adminapi -run TestSetStatementIPRanges -v
```

Expected: FAIL，`protocol.SetStatementIPRangesRequest undefined`。

- [ ] **Step 3: 添加协议结构**

在 `internal/protocol/protocol.go` 末尾追加：

```go
type SetStatementIPRangesRequest struct {
	Cidrs *[]string `json:"cidrs"`
}

type SetStatementIPRangesResponse struct {
	Policy string   `json:"policy"`
	Sid    string   `json:"sid"`
	Cidrs  []string `json:"cidrs"`
}
```

- [ ] **Step 4: 添加客户端方法**

在 `internal/adminapi/client.go` 的 `Clean` 方法之后追加：

```go
// SetStatementIPRanges 设置或清除指定策略语句的 IP 白名单。
func (c *Client) SetStatementIPRanges(ctx context.Context, policyName, sid string, cidrs []string) (protocol.SetStatementIPRangesResponse, error) {
	var out protocol.SetStatementIPRangesResponse
	values := cidrs
	if values == nil {
		values = []string{}
	}
	payload, err := json.Marshal(protocol.SetStatementIPRangesRequest{Cidrs: &values})
	if err != nil {
		return out, err
	}
	u := c.baseURL + "/v1/admin/policies/" + url.PathEscape(policyName) +
		"/statements/" + url.PathEscape(sid) + "/ip-ranges"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	if err := c.doJSON(req, &out); err != nil {
		return out, err
	}
	return out, nil
}
```

- [ ] **Step 5: 运行测试确认通过**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/adminapi -run TestSetStatementIPRanges -v
```

Expected: 全部 PASS

- [ ] **Step 6: 提交**

```bash
git add internal/protocol/protocol.go internal/adminapi/client.go internal/adminapi/client_test.go
git commit -m "feat: 添加策略语句 IP 白名单客户端"
```

---

### Task 8: 新增 ft-server HTTP 接口

**Files:**
- Modify: `internal/server/handler/admin.go`
- Modify: `internal/server/router/router.go`
- Test: `internal/server/handler/admin_test.go`
- Test: `internal/server/router/router_test.go`

- [ ] **Step 1: 写失败的 handler 测试**

在 `internal/server/handler/admin_test.go` 追加（导入中加入 `"github.com/rfancn/file-transfer/internal/iam"`）：

```go
func TestAdminSetStatementIPRangesRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		policy string
		sid    string
		body   string
		status int
		code   string
	}{
		{"missing cidrs", "ft-user-policy", "UploadWrite", `{}`, http.StatusBadRequest, "invalid_request"},
		{"invalid cidr", "ft-user-policy", "UploadWrite", `{"cidrs":["bad"]}`, http.StatusBadRequest, "invalid_cidr"},
		{"service policy", "ft-admin-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"]}`, http.StatusForbidden, "service_policy"},
		{"unsupported policy", "custom-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"]}`, http.StatusBadRequest, "unsupported_policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{App: &app.App{IAM: &iam.Manager{}}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/v1/admin/policies/x/statements/y/ip-ranges", strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = gin.Params{
				{Key: "name", Value: tt.policy},
				{Key: "sid", Value: tt.sid},
			}

			h.AdminSetStatementIPRanges(c)

			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", w.Code, tt.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.code) {
				t.Fatalf("body = %s", w.Body.String())
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/handler -run TestAdminSetStatementIPRangesRejects -v
```

Expected: FAIL，`h.AdminSetStatementIPRanges undefined`。

- [ ] **Step 3: 实现 handler**

在 `internal/server/handler/admin.go` 末尾追加（导入中加入 `"errors"`）：

```go
// AdminSetStatementIPRanges 设置或清除指定策略语句的 IP 白名单。
func (h *Handler) AdminSetStatementIPRanges(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req protocol.SetStatementIPRangesRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Cidrs == nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "cidrs 不能为空")
		return
	}
	policyName := c.Param("name")
	sid := c.Param("sid")
	opts := iam.PolicyOptions{
		HomeBucket:   h.Cfg.S3.HomeBucket,
		SharedBucket: h.Cfg.S3.SharedBucket,
		SharedDirs:   h.Cfg.S3.SharedDirs,
	}
	cidrs, err := h.IAM.SetStatementIPRanges(c.Request.Context(), policyName, sid, *req.Cidrs, opts)
	if err != nil {
		logging.Error("admin set statement ip ranges", "policy", policyName, "sid", sid, "err", err)
		writePolicyError(c, err)
		return
	}
	logging.Audit(logging.AuditEvent{
		Action:    "admin.policy.ip.set",
		Actor:     c.GetString("admin"),
		ActorType: "admin",
		Target:    policyName + "/" + sid,
		Result:    "success",
		RequestID: c.GetString("request_id"),
		RemoteIP:  c.ClientIP(),
	})
	response.JSON(c, http.StatusOK, protocol.SetStatementIPRangesResponse{
		Policy: policyName,
		Sid:    sid,
		Cidrs:  cidrs,
	})
}

func writePolicyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, iam.ErrInvalidCIDR):
		response.Err(c, http.StatusBadRequest, "invalid_cidr", err.Error())
	case errors.Is(err, iam.ErrServicePolicy):
		response.Err(c, http.StatusForbidden, "service_policy", err.Error())
	case errors.Is(err, iam.ErrUnsupportedPolicy):
		response.Err(c, http.StatusBadRequest, "unsupported_policy", "只允许 ft-user-policy、ft-operator-policy、shared-operator-policy")
	case errors.Is(err, iam.ErrSIDNotFound):
		response.Err(c, http.StatusBadRequest, "sid_not_found", "策略中不存在指定 Sid，请先执行 ft-admin init 更新策略")
	case errors.Is(err, iam.ErrStatementNotAllow):
		response.Err(c, http.StatusBadRequest, "statement_not_allow", "只能给 Allow 语句设置 IP 白名单")
	case errors.Is(err, iam.ErrInvalidPolicy):
		response.Err(c, http.StatusInternalServerError, "invalid_policy", "策略内容无效")
	case errors.Is(err, iam.ErrPolicyWrite):
		response.Err(c, http.StatusInternalServerError, "policy_write_failed", "写入策略文件失败，请检查 policy 目录权限")
	default:
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 管理服务不可用")
	}
}
```

- [ ] **Step 4: 注册路由**

在 `internal/server/router/router.go` 的受保护路由区加入：

```go
	protected.PUT("/policies/:name/statements/:sid/ip-ranges", middleware.RequireReady(a), h.AdminSetStatementIPRanges)
```

放在 `protected.DELETE("/data/:dir", ...)` 之后。

- [ ] **Step 5: 写路由测试**

在 `internal/server/router/router_test.go` 的 `TestRouteGroupsRequireAuth` 用例表中加入：

```go
		{http.MethodPut, "/v1/admin/policies/ft-user-policy/statements/UploadWrite/ip-ranges", `{"cidrs":[]}`, http.StatusUnauthorized},
```

- [ ] **Step 6: 运行测试确认通过**

```bash
GOPROXY=https://goproxy.cn,direct go test ./internal/server/handler ./internal/server/router -run 'TestAdminSetStatementIPRangesRejects|TestRouteGroupsRequireAuth' -v
```

Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add internal/server/handler/admin.go internal/server/handler/admin_test.go internal/server/router/router.go internal/server/router/router_test.go
git commit -m "feat: 添加策略语句 IP 白名单接口"
```

---

### Task 9: 新增 ft-admin 命令与菜单项

**Files:**
- Create: `cmd/admin/policy.go`
- Create: `cmd/admin/policy_test.go`
- Modify: `cmd/admin/menu.go`

- [ ] **Step 1: 写失败的测试**

创建 `cmd/admin/policy_test.go`：

```go
package main

import (
	"reflect"
	"testing"
)

func TestSplitCIDRs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"single", []string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}},
		{"multiple", []string{"10.0.0.0/8", "192.168.1.0/24"}, []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"comma", []string{"10.0.0.0/8,192.168.1.0/24"}, []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"trim and skip empty", []string{" 10.0.0.0/8 , ", ",192.168.1.0/24"}, []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{"empty", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitCIDRs(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
GOPROXY=https://goproxy.cn,direct go test ./cmd/admin -run TestSplitCIDRs -v
```

Expected: FAIL，`undefined: splitCIDRs`。

- [ ] **Step 3: 实现命令**

创建 `cmd/admin/policy.go`：

```go
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/iam"
)

var (
	policyIPClear bool
	policyIPYes   bool

	policyCmd = &cobra.Command{
		Use:   "policy",
		Short: "管理 IAM 策略",
	}

	policyIPCmd = &cobra.Command{
		Use:   "ip <policy> <sid> [cidr...]",
		Short: "设置策略语句的 IP 白名单",
		Args:  cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			cidrs := splitCIDRs(args[2:])
			setPolicyIPs(adminState.ac, args[0], args[1], cidrs, policyIPClear, policyIPYes)
		},
	}
)

func init() {
	rootCmd.AddCommand(policyCmd)
	policyCmd.AddCommand(policyIPCmd)
	policyIPCmd.Flags().BoolVar(&policyIPClear, "clear", false, "清除 IP 白名单")
	policyIPCmd.Flags().BoolVar(&policyIPYes, "yes", false, "跳过确认")
}

// splitCIDRs 拆分位置参数中的逗号，返回去空白后的非空网段。
func splitCIDRs(args []string) []string {
	var out []string
	for _, arg := range args {
		for _, part := range strings.Split(arg, ",") {
			if v := strings.TrimSpace(part); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func setPolicyIPs(ac *adminapi.Client, policyName, sid string, cidrs []string, clear, yes bool) {
	if clear && len(cidrs) > 0 {
		failf("--clear 不能与网段参数同时使用")
		return
	}
	if iam.IsServicePolicy(policyName) {
		failf("服务身份策略不允许修改")
		return
	}
	if !iam.IsEditablePolicy(policyName) {
		failf("策略只允许 %s", strings.Join(iam.EditablePolicies(), "、"))
		return
	}
	sid = strings.TrimSpace(sid)
	if sid == "" {
		failf("Sid 不能为空")
		return
	}
	if !clear && len(cidrs) == 0 {
		if !cliui.IsInteractive() {
			failf("请提供至少一个网段，或使用 --clear 清除")
			return
		}
		value, err := cliui.Input("IP 网段（逗号分隔，留空表示清除）", "", nil)
		if canceledByUser(err) {
			return
		}
		if err != nil {
			failf("读取网段失败: %v", err)
			return
		}
		cidrs = splitCIDRs([]string{value})
		clear = len(cidrs) == 0
	}
	if !yes {
		if clear {
			fmt.Printf("将清除策略 %s 语句 %s 的 IP 白名单。\n", policyName, sid)
		} else {
			fmt.Printf("将策略 %s 语句 %s 的 IP 白名单设置为：%s\n", policyName, sid, strings.Join(cidrs, ", "))
		}
		if !confirm("确认修改") {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := ac.SetStatementIPRanges(ctx, policyName, sid, cidrs)
	if err != nil {
		failf("设置 IP 白名单失败: %v", err)
		return
	}
	if len(res.Cidrs) == 0 {
		fmt.Printf("已清除策略 %s 语句 %s 的 IP 白名单\n", res.Policy, res.Sid)
		return
	}
	fmt.Printf("已设置策略 %s 语句 %s 的 IP 白名单：%s\n", res.Policy, res.Sid, strings.Join(res.Cidrs, ", "))
}

func runPolicyIPMenu(ac *adminapi.Client) {
	names := iam.EditablePolicies()
	labels := []string{
		"ft-user-policy（普通用户）",
		"ft-operator-policy（摆渡操作员）",
		"shared-operator-policy（共享操作员）",
	}
	idx, err := cliui.Select("选择策略", labels)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "选择策略失败: %v\n", err)
		return
	}
	sid, err := cliui.Input("语句 Sid（例如 UploadWrite）", "", nil)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 Sid 失败: %v\n", err)
		return
	}
	value, err := cliui.Input("IP 网段（逗号分隔，留空表示清除）", "", nil)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取网段失败: %v\n", err)
		return
	}
	cidrs := splitCIDRs([]string{value})
	setPolicyIPs(ac, names[idx], strings.TrimSpace(sid), cidrs, len(cidrs) == 0, false)
}
```

`cmd/admin/policy.go` 需要 `"os"` 导入（`runPolicyIPMenu` 使用 `os.Stderr`），把导入列表改为：

```go
import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/iam"
)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
GOPROXY=https://goproxy.cn,direct go test ./cmd/admin -run TestSplitCIDRs -v
```

Expected: PASS

- [ ] **Step 5: 接入菜单**

把 `cmd/admin/menu.go` 的菜单项和 switch 改为：

```go
		idx, err := cliui.Select("请选择操作", []string{
			"添加用户",
			"删除用户",
			"重置用户密码",
			"列出用户",
			"清理数据",
			"设置策略 IP 白名单",
			"初始化",
			"退出",
		})
```

```go
		case 4:
			runClean(ac, "", -1, false, false)
		case 5:
			runPolicyIPMenu(ac)
		case 6:
			runInit(&cfg, ac, "", "")
		case 7:
			return
```

- [ ] **Step 6: 编译并运行全部 cmd 测试**

```bash
GOPROXY=https://goproxy.cn,direct go test ./cmd/... -v
```

Expected: 全部 PASS

- [ ] **Step 7: 提交**

```bash
git add cmd/admin/policy.go cmd/admin/policy_test.go cmd/admin/menu.go
git commit -m "feat: ft-admin 添加策略 IP 白名单命令"
```

---

### Task 10: 更新部署与文档

**Files:**
- Modify: `docker-compose.yml`
- Modify: `README.md`

- [ ] **Step 1: 让 Docker 策略目录可写**

把 `docker-compose.yml` 中的：

```yaml
      - ./policy:/etc/file-transfer/policy:ro
```

改为：

```yaml
      - ./policy:/etc/file-transfer/policy
```

- [ ] **Step 2: 更新 README 策略目录说明**

在 `README.md` 的 IAM 策略文件小节中，把：

```markdown
- 修改策略后重启 `ft-server`，再执行 `ft-admin init` 使策略在 RustFS 生效。
```

改为：

```markdown
- 修改策略后重启 `ft-server`，再执行 `ft-admin init` 使策略在 RustFS 生效。
- 策略目录必须可写：`ft-admin policy ip` 会直接修改 `policy/*.json` 并立即下发。
- 升级时如果整体覆盖 `policy/*.json`，已设置的 IP 白名单会丢失；升级前请保留或合并这些文件。
```

- [ ] **Step 3: 新增“策略语句 IP 白名单”文档**

在 IAM 策略文件小节末尾追加：

````markdown
#### 策略语句 IP 白名单

可以为三个组策略中的指定语句（Sid）增加 `aws:SourceIp` 条件，只有来自指定网段的请求才能使用该语句的权限。

管理接口：

```http
PUT /v1/admin/policies/:name/statements/:sid/ip-ranges
Authorization: Bearer <admin token>
Content-Type: application/json

{ "cidrs": ["10.0.0.0/8", "192.168.1.0/24"] }
```

- 只允许 `ft-user-policy`、`ft-operator-policy`、`shared-operator-policy`；
- `cidrs: []` 表示清除该语句的 IP 白名单；
- 规则写入对应 `policy/*.json` 模板，需要策略目录可写；
- 修改后立即下发 RustFS，无需重启。

命令：

```sh
ft-admin policy ip ft-user-policy UploadWrite 10.0.0.0/8 192.168.1.0/24
ft-admin policy ip ft-user-policy UploadWrite --clear
```

常用 Sid：

| 策略 | Sid | 说明 |
|---|---|---|
| `ft-user-policy` | `UploadWrite` | 自己的 `upload/` 写/删 |
| `ft-user-policy` | `SharedRead` | 读取共享目录 |
| `ft-operator-policy` | `InboundWrite` | 所有用户 `inbound` 写/删 |
| `ft-operator-policy` | `OutboundRead` | 读取所有用户 `outbound` |
| `shared-operator-policy` | `SharedWrite` | 写/删共享桶对象 |

完整 Sid 列表见 `policy/*.json` 中的 `Sid` 字段。
````

- [ ] **Step 4: 更新 ft-admin 命令表**

在 `README.md` 的 `ft-admin` 命令表中加入：

```markdown
| `ft-admin policy ip <policy> <sid> [cidr...] [--clear] [--yes]` | 设置策略语句的 IP 白名单 |
```

- [ ] **Step 5: 更新菜单说明**

把：

```markdown
无子命令时进入交互菜单：添加用户、删除用户、重置用户密码、列出用户、清理数据、初始化、退出。
```

改为：

```markdown
无子命令时进入交互菜单：添加用户、删除用户、重置用户密码、列出用户、清理数据、设置策略 IP 白名单、初始化、退出。
```

- [ ] **Step 6: 提交**

```bash
git add docker-compose.yml README.md
git commit -m "docs: 说明策略 IP 白名单与策略目录写权限"
```

---

### Task 11: 全量验证

**Files:** 无新增文件

- [ ] **Step 1: 格式化**

```bash
gofmt -w internal/iam/iprules.go internal/iam/iprules_test.go internal/iam/policies.go internal/iam/iam.go internal/iam/iam_test.go internal/bootstrap/bootstrap.go internal/server/server.go internal/server/handler/admin.go internal/server/handler/admin_test.go internal/server/router/router.go internal/server/router/router_test.go internal/protocol/protocol.go internal/adminapi/client.go internal/adminapi/client_test.go cmd/admin/policy.go cmd/admin/policy_test.go cmd/admin/menu.go
```

Expected: 无输出

- [ ] **Step 2: go vet**

```bash
GOPROXY=https://goproxy.cn,direct go vet ./...
```

Expected: 无输出

- [ ] **Step 3: 全量测试（含 race）**

```bash
GOPROXY=https://goproxy.cn,direct go test -race ./...
```

Expected: 全部 PASS

- [ ] **Step 4: 编译三个二进制**

```bash
GOPROXY=https://goproxy.cn,direct make build
```

Expected: 生成 `bin/ft-server`、`bin/ft-client`、`bin/ft-admin`，无错误。

- [ ] **Step 5: 手工验证（需要真实 RustFS）**

1. 在目标 RustFS 版本上确认带 `Sid` 的策略可以正常加载；
2. 执行：

```bash
./bin/ft-admin policy ip ft-user-policy UploadWrite 10.0.0.0/8
```

3. 用白名单外地址以普通用户执行 `ft-client upload`，预期收到 AccessDenied；
4. 用白名单内地址执行同样的上传，预期成功；
5. 执行 `./bin/ft-admin policy ip ft-user-policy UploadWrite --clear`，再次用白名单外地址上传，预期恢复成功。

- [ ] **Step 6: 最终提交（如前面各任务已分别提交，此步可跳过）**

```bash
git status --short
```

Expected: 无未提交改动。

---

## 自检结果

- 规格覆盖：接口、Sid、模板写回、内存同步、立即下发、ft-admin 命令/菜单、Docker 可写、旧逻辑移除、测试与风险均已对应到任务。
- 占位符扫描：无 TBD/TODO；所有代码步骤均给出完整实现。
- 类型一致性：`SetStatementIPRanges` 返回 `([]string, error)`；`renderUpdatedPolicy` 返回 `([]string, []byte, error)`；协议结构名与 handler/client 使用一致。