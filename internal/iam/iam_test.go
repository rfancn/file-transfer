package iam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadTestPolicies(t *testing.T) *Policies {
	t.Helper()
	p, err := LoadPolicies(filepath.Join("..", "..", "policy"), "file-transfer")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPoliciesValidJSON(t *testing.T) {
	p := loadTestPolicies(t)
	userPolicy, err := p.UserPolicy("alice")
	if err != nil {
		t.Fatal(err)
	}
	pols := map[string][]byte{
		"group":        p.Group,
		"user":         userPolicy,
		"operator":     p.Operator,
		"server-data":  p.Data,
		"server-admin": p.Admin,
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

func TestUserPolicyCannotDeleteUpload(t *testing.T) {
	p := loadTestPolicies(t)
	raw, err := p.UserPolicy("alice")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3:DeleteObject") {
		t.Fatalf("user policy must not allow deleting upload objects: %s", raw)
	}
}

func TestUserTemplateRenders(t *testing.T) {
	p := loadTestPolicies(t)
	raw, err := p.UserPolicy("alice")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"alice/inbound/", "alice/outbound/", "alice/upload/",
		"arn:aws:s3:::file-transfer/alice/inbound/*",
		"arn:aws:s3:::file-transfer/alice/upload/*",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered user policy missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "{{") {
		t.Fatalf("rendered user policy still contains placeholders: %s", text)
	}
}

func TestOperatorPolicyCanDeleteInbound(t *testing.T) {
	p := loadTestPolicies(t)
	var doc struct {
		Statement []struct {
			Effect   string
			Action   []string
			Resource []string
		}
	}
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
	var doc struct {
		Statement []struct {
			Effect    string
			Action    []string
			Condition map[string]map[string]any
		}
	}
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

func TestLoadPoliciesMissingFile(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, policyServerAdmin)
	if _, err := LoadPolicies(dir, "bucket"); err == nil || !strings.Contains(err.Error(), policyServerAdmin) {
		t.Fatalf("期望缺少 %s 的错误，得到 %v", policyServerAdmin, err)
	}
}

func TestLoadPoliciesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(`{"Version":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, "bucket"); err == nil || !strings.Contains(err.Error(), policyUserGroupFile) {
		t.Fatalf("期望 %s JSON 错误，得到 %v", policyUserGroupFile, err)
	}
}

func TestLoadPoliciesUnknownPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	bad := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::{{bucket}}/{{other}}/*"]}]}`
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, "bucket"); err == nil || !strings.Contains(err.Error(), "未知占位符") {
		t.Fatalf("期望未知占位符错误，得到 %v", err)
	}
}

func TestLoadPoliciesRequiresUserPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	bad := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::{{bucket}}/*"]}]}`
	if err := os.WriteFile(filepath.Join(dir, policyUserTemplate), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, "bucket"); err == nil || !strings.Contains(err.Error(), placeholderUser) {
		t.Fatalf("期望缺少 %s 的错误，得到 %v", placeholderUser, err)
	}
}

func TestLoadPoliciesEmptyDir(t *testing.T) {
	if _, err := LoadPolicies(t.TempDir(), "bucket"); err == nil {
		t.Fatal("空目录应返回错误")
	}
}

func TestUserPolicyRequiresUser(t *testing.T) {
	p := loadTestPolicies(t)
	if _, err := p.UserPolicy(""); err == nil {
		t.Fatal("空用户名应返回错误")
	}
	if _, err := p.UserPolicy("bad name"); err == nil {
		t.Fatal("非法用户名应返回错误")
	}
}

func TestLoadPoliciesResidualBrace(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	bad := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::{{bucket}}/user}}/*"]}]}`
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir, "bucket"); err == nil || !strings.Contains(err.Error(), "未知占位符") {
		t.Fatalf("期望残留占位符错误，得到 %v", err)
	}
}

func writeValidPolicies(t *testing.T, dir, skip string) {
	t.Helper()
	plain := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::{{bucket}}/*"]}]}`
	template := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::{{bucket}}/{{user}}/*"]}]}`
	files := map[string]string{
		policyUserGroupFile:  plain,
		policyUserTemplate:   template,
		policyOperatorFile:   plain,
		policyServerDataFile: plain,
		policyServerAdmin:    plain,
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
	for _, name := range []string{"ft-data", "ft-admin", "ft-operator", "ft-server-data", "ft-server-admin", "ft-copy-out"} {
		if !IsSystemUser(name) {
			t.Fatalf("%s should be a system user", name)
		}
	}
	for _, name := range []string{"alice", "fanyu", "ft-user-group"} {
		if IsSystemUser(name) {
			t.Fatalf("%s should not be a system user", name)
		}
	}
}

func TestUserPolicyName(t *testing.T) {
	if got := UserPolicyName("alice"); got != "ft-user-alice-policy" {
		t.Fatalf("got %s", got)
	}
}
