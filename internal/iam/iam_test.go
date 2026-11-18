package iam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// policyDoc 是测试断言策略 JSON 时使用的通用结构。
type policyDoc struct {
	Statement []struct {
		Effect    string
		Action    []string
		Resource  []string
		Condition map[string]map[string]any
	}
}

func loadTestPolicies(t *testing.T) *Policies {
	t.Helper()
	p, err := LoadPolicies(filepath.Join("..", "..", "policy"), testPolicyOptions())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testPolicyOptions() PolicyOptions {
	return PolicyOptions{
		HomeBucket:   "home",
		SharedBucket: "shared",
		SharedDirs:   []string{"software", "document"},
	}
}

func TestPoliciesValidJSON(t *testing.T) {
	p := loadTestPolicies(t)
	pols := map[string][]byte{
		"group":           p.Group,
		"operator":        p.Operator,
		"shared-operator": p.SharedOperator,
		"server-data":     p.Data,
		"server-admin":    p.Admin,
	}
	for name, raw := range pols {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s invalid JSON: %v", name, err)
		}
		if !strings.Contains(string(raw), "s3:") {
			t.Fatalf("%s missing s3 action", name)
		}
	}
}

func TestUserGroupPolicyUploadScope(t *testing.T) {
	p := loadTestPolicies(t)
	var doc policyDoc
	if err := json.Unmarshal(p.Group, &doc); err != nil {
		t.Fatal(err)
	}
	allowActions := map[string]bool{
		"s3:PutObject":                true,
		"s3:AbortMultipartUpload":     true,
		"s3:ListMultipartUploadParts": true,
		"s3:PutObjectTagging":         true,
		"s3:DeleteObject":             true,
		"s3:DeleteObjectVersion":      true,
	}
	seenAllow := map[string]bool{}
	uploadResource := "arn:aws:s3:::home/${aws:username}/upload/*"
	protectResources := map[string]bool{
		"arn:aws:s3:::home/${aws:username}/upload/.keep": false,
		"arn:aws:s3:::home/${aws:username}/upload/":      false,
	}
	for _, st := range doc.Statement {
		for _, action := range st.Action {
			if action == "s3:ListAllMyBuckets" || action == "s3:ListBucketMultipartUploads" {
				t.Fatalf("user group must not grant %s", action)
			}
			if action == "s3:GetObject" {
				for _, res := range st.Resource {
					if !strings.HasPrefix(res, "arn:aws:s3:::shared/") {
						t.Fatalf("user group GetObject outside shared dirs: %s", res)
					}
				}
			}
			if !allowActions[action] || st.Effect != "Allow" {
				continue
			}
			seenAllow[action] = true
			for _, res := range st.Resource {
				if res != uploadResource {
					t.Fatalf("%s resource outside upload: %s", action, res)
				}
			}
		}
		for _, res := range st.Resource {
			if strings.Contains(res, "/inbound/") || strings.Contains(res, "/outbound/") {
				t.Fatalf("user group must not grant inbound/outbound access: %s", res)
			}
		}
	}
	for action := range allowActions {
		if !seenAllow[action] {
			t.Fatalf("user group missing upload action %s", action)
		}
	}
	for _, st := range doc.Statement {
		if st.Effect != "Deny" {
			continue
		}
		for _, action := range st.Action {
			if action != "s3:DeleteObject" && action != "s3:DeleteObjectVersion" {
				continue
			}
			for _, res := range st.Resource {
				if _, ok := protectResources[res]; ok {
					protectResources[res] = true
				}
			}
		}
	}
	for res, seen := range protectResources {
		if !seen {
			t.Fatalf("user group must deny deleting %s", res)
		}
	}
	text := string(p.Group)
	for _, want := range []string{
		"${aws:username}/upload",
		"${aws:username}/upload/",
		"${aws:username}/upload/*",
		"${aws:username}/inbound/",
		"${aws:username}/outbound/",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("user group missing %q", want)
		}
	}
}

func TestUserGroupCanListOwnDirs(t *testing.T) {
	p := loadTestPolicies(t)
	var doc policyDoc
	if err := json.Unmarshal(p.Group, &doc); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"${aws:username}", "${aws:username}/",
		"${aws:username}/inbound", "${aws:username}/inbound/", "${aws:username}/inbound/*",
		"${aws:username}/outbound", "${aws:username}/outbound/", "${aws:username}/outbound/*",
		"${aws:username}/upload", "${aws:username}/upload/", "${aws:username}/upload/*",
	}
	allowed := map[string]bool{}
	for _, st := range doc.Statement {
		if st.Effect != "Allow" {
			continue
		}
		isList := false
		for _, a := range st.Action {
			if a == "s3:ListBucket" {
				isList = true
			}
		}
		if !isList {
			continue
		}
		for _, w := range want {
			if hasPrefixValue(st.Condition["StringLike"]["s3:prefix"], w) {
				allowed[w] = true
			}
		}
	}
	for _, w := range want {
		if !allowed[w] {
			t.Fatalf("user group ListBucket missing prefix %q", w)
		}
	}
}

func TestOperatorPolicyHasNoAdminActions(t *testing.T) {
	p := loadTestPolicies(t)
	if strings.Contains(string(p.Operator), "admin:") {
		t.Fatal("operator policy must not contain admin actions")
	}
}

func TestOperatorPolicyCanDeleteInbound(t *testing.T) {
	p := loadTestPolicies(t)
	var doc policyDoc
	if err := json.Unmarshal(p.Operator, &doc); err != nil {
		t.Fatal(err)
	}
	var allowDelete, denyKeep bool
	for _, st := range doc.Statement {
		hasDelete := false
		for _, action := range st.Action {
			if action == "s3:DeleteObject" {
				hasDelete = true
			}
		}
		if !hasDelete {
			continue
		}
		switch st.Effect {
		case "Allow":
			allowDelete = true
			for _, res := range st.Resource {
				if !strings.HasSuffix(res, "/*/inbound/*") {
					t.Fatalf("delete resource too broad: %s", res)
				}
			}
		case "Deny":
			for _, res := range st.Resource {
				if strings.HasSuffix(res, "/*/inbound/.keep") {
					denyKeep = true
				}
			}
		}
	}
	if !allowDelete {
		t.Fatal("operator policy should allow deleting inbound objects")
	}
	if !denyKeep {
		t.Fatal("operator must not delete inbound .keep")
	}
}

func TestOperatorPolicyAllowsRecursiveInboundList(t *testing.T) {
	p := loadTestPolicies(t)
	var doc policyDoc
	if err := json.Unmarshal(p.Operator, &doc); err != nil {
		t.Fatal(err)
	}
	var allowRecursive, denyExcludesInbound bool
	for _, st := range doc.Statement {
		isList := false
		for _, a := range st.Action {
			if a == "s3:ListBucket" {
				isList = true
			}
		}
		if !isList {
			continue
		}
		switch st.Effect {
		case "Allow":
			if hasPrefixValue(st.Condition["StringLike"]["s3:prefix"], "*/inbound/*") {
				allowRecursive = true
			}
		case "Deny":
			if hasPrefixValue(st.Condition["StringNotLike"]["s3:prefix"], "*/inbound/*") {
				denyExcludesInbound = true
			}
		}
	}
	if !allowRecursive || !denyExcludesInbound {
		t.Fatalf("allow recursive inbound list=%v, deny excludes inbound=%v", allowRecursive, denyExcludesInbound)
	}
}

func hasPrefixValue(v any, want string) bool {
	arr, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range arr {
		if s, ok := item.(string); ok && s == want {
			return true
		}
	}
	return false
}

func TestOperatorPolicyRestrictsUploadDir(t *testing.T) {
	p := loadTestPolicies(t)
	text := string(p.Operator)
	for _, want := range []string{"s3:delimiter", "*/upload/", `"Deny"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("operator policy missing %q: %s", want, text)
		}
	}
}

func TestServerDataPolicyKeepsPlaceholders(t *testing.T) {
	p := loadTestPolicies(t)
	text := string(p.Data)
	for _, want := range []string{"*/inbound/.keep", "*/outbound/.keep", "*/upload/.keep"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text)
		}
	}
}

func TestUserGroupSharedReadOnly(t *testing.T) {
	p := loadTestPolicies(t)
	text := string(p.Group)
	for _, want := range []string{
		"arn:aws:s3:::shared/software/*",
		"arn:aws:s3:::shared/document/*",
		`"software/"`, `"software/*"`, `"document/"`, `"document/*"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("user group missing shared read %q", want)
		}
	}

	var doc policyDoc
	if err := json.Unmarshal(p.Group, &doc); err != nil {
		t.Fatal(err)
	}
	for _, st := range doc.Statement {
		if st.Effect != "Allow" || !strings.Contains(strings.Join(st.Resource, " "), "arn:aws:s3:::shared") {
			continue
		}
		for _, action := range st.Action {
			switch action {
			case "s3:PutObject", "s3:PutObjectTagging", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:AbortMultipartUpload":
				t.Fatalf("user group must not grant shared write action %s", action)
			}
		}
	}
}

func TestSharedOperatorPolicy(t *testing.T) {
	p := loadTestPolicies(t)
	text := string(p.SharedOperator)
	for _, want := range []string{
		"s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject",
		"arn:aws:s3:::shared/*", `"s3:DeleteBucket"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("shared operator policy missing %q", want)
		}
	}
	if strings.Contains(text, "admin:") {
		t.Fatal("shared operator policy must not contain admin actions")
	}
	if strings.Contains(text, "s3:ListAllMyBuckets") {
		t.Fatal("shared operator policy must not list all buckets")
	}
	if !strings.Contains(text, "/*/.keep") {
		t.Fatal("shared operator policy must protect shared directory markers")
	}

	var doc policyDoc
	if err := json.Unmarshal(p.SharedOperator, &doc); err != nil {
		t.Fatal(err)
	}
	denyDeleteBucket := false
	for _, st := range doc.Statement {
		if st.Effect != "Deny" {
			continue
		}
		for _, action := range st.Action {
			if action == "s3:DeleteBucket" {
				denyDeleteBucket = true
			}
		}
	}
	if !denyDeleteBucket {
		t.Fatal("shared operator policy must deny DeleteBucket")
	}
}

func TestPoliciesRenderConfiguredBucketsAndDirs(t *testing.T) {
	p, err := LoadPolicies(filepath.Join("..", "..", "policy"), PolicyOptions{
		HomeBucket:   "custom-home",
		SharedBucket: "custom-shared",
		SharedDirs:   []string{"tools", "docs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"group": p.Group, "operator": p.Operator, "shared-operator": p.SharedOperator,
		"data": p.Data, "admin": p.Admin,
	} {
		text := string(raw)
		if strings.Contains(text, "file-transfer") || strings.Contains(text, "{{") || strings.Contains(text, "}}") {
			t.Fatalf("%s 未正确渲染: %s", name, text)
		}
	}
	group := string(p.Group)
	for _, want := range []string{
		"arn:aws:s3:::custom-home/${aws:username}/upload/*",
		"arn:aws:s3:::custom-shared/tools/*",
		"arn:aws:s3:::custom-shared/docs/*",
		`"tools/"`, `"tools/*"`, `"docs/"`, `"docs/*"`,
	} {
		if !strings.Contains(group, want) {
			t.Fatalf("用户组策略缺少 %q: %s", want, group)
		}
	}
	if !strings.Contains(string(p.SharedOperator), "arn:aws:s3:::custom-shared/*") {
		t.Fatal("共享操作员策略未使用自定义共享桶")
	}
}

func TestLoadPoliciesRequiresBuckets(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	if _, err := LoadPolicies(dir, PolicyOptions{}); err == nil || !strings.Contains(err.Error(), "桶名") {
		t.Fatalf("期望缺少桶名错误，得到 %v", err)
	}
}

func TestLoadPoliciesRequiresSharedDirs(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	tokenPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["{{SHARED_DIR_OBJECT_ARNS}}"]}]}`
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(tokenPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPolicies(dir, PolicyOptions{HomeBucket: "home", SharedBucket: "shared"})
	if err == nil || !strings.Contains(err.Error(), "至少一个共享目录") {
		t.Fatalf("期望缺少共享目录错误，得到 %v", err)
	}
}

func TestLoadPoliciesMissingFile(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, policyServerAdmin)
	if _, err := LoadPolicies(dir, testPolicyOptions()); err == nil || !strings.Contains(err.Error(), policyServerAdmin) {
		t.Fatalf("期望缺少 %s 的错误，得到 %v", policyServerAdmin, err)
	}
}

func TestLoadPoliciesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(`{"Version":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, testPolicyOptions()); err == nil || !strings.Contains(err.Error(), policyUserGroupFile) {
		t.Fatalf("期望 %s JSON 错误，得到 %v", policyUserGroupFile, err)
	}
}

func TestLoadPoliciesUnknownPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	bad := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::file-transfer/{{other}}/*"]}]}`
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, testPolicyOptions()); err == nil || !strings.Contains(err.Error(), "未知占位符") {
		t.Fatalf("期望未知占位符错误，得到 %v", err)
	}
}

func TestLoadPoliciesResidualBrace(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	bad := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::file-transfer/user}}/*"]}]}`
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, testPolicyOptions()); err == nil || !strings.Contains(err.Error(), "未知占位符") {
		t.Fatalf("期望残留占位符错误，得到 %v", err)
	}
}

func TestLoadPoliciesEmptyDir(t *testing.T) {
	if _, err := LoadPolicies(t.TempDir(), testPolicyOptions()); err == nil {
		t.Fatal("空目录应返回错误")
	}
}

func writeValidPolicies(t *testing.T, dir, skip string) {
	t.Helper()
	plain := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::file-transfer/*"]}]}`
	files := map[string]string{
		policyUserGroupFile:      plain,
		policyOperatorFile:       plain,
		policySharedOperatorFile: plain,
		policyServerDataFile:     plain,
		policyServerAdmin:        plain,
	}
	for name, content := range files {
		if name == skip {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIsSystemUser(t *testing.T) {
	for _, name := range []string{"ft-data", "ft-admin", "ft-server-data", "ft-server-admin", "ft-copy-out"} {
		if !IsSystemUser(name) {
			t.Fatalf("%s should be a system user", name)
		}
	}
	for _, name := range []string{"alice", "fanyu", "ft-operator", "ft-user-group"} {
		if IsSystemUser(name) {
			t.Fatalf("%s should not be a system user", name)
		}
	}
}

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
		if len(doc.Statement) != len(want) {
			t.Fatalf("%s 语句数量 = %d，期望 %d", file, len(doc.Statement), len(want))
		}
		seen := map[string]bool{}
		for i, st := range doc.Statement {
			if doc.Statement[i].Sid != want[i] {
				t.Fatalf("%s 第 %d 条语句 Sid = %s，期望 %s", file, i+1, doc.Statement[i].Sid, want[i])
			}
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
