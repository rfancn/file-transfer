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
	p, err := LoadPolicies(filepath.Join("..", "..", "policy"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPoliciesValidJSON(t *testing.T) {
	p := loadTestPolicies(t)
	pols := map[string][]byte{
		"group":        p.Group,
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

func TestUserGroupPolicyUploadOnly(t *testing.T) {
	p := loadTestPolicies(t)
	var doc struct {
		Statement []struct {
			Effect   string
			Action   []string
			Resource []string
		}
	}
	if err := json.Unmarshal(p.Group, &doc); err != nil {
		t.Fatal(err)
	}
	allowActions := map[string]bool{
		"s3:PutObject":                true,
		"s3:AbortMultipartUpload":     true,
		"s3:ListMultipartUploadParts": true,
		"s3:PutObjectTagging":         true,
	}
	denyActions := map[string]bool{
		"s3:DeleteObject":        true,
		"s3:DeleteObjectVersion": true,
		"s3:DeleteObjectTagging": true,
	}
	seenAllow := map[string]bool{}
	seenDeny := map[string]bool{}
	uploadResource := "arn:aws:s3:::file-transfer/${aws:username}/upload/*"
	for _, st := range doc.Statement {
		for _, action := range st.Action {
			if action == "s3:GetObject" || action == "s3:ListAllMyBuckets" || action == "s3:ListBucketMultipartUploads" {
				t.Fatalf("user group must not grant %s", action)
			}
			if allowActions[action] {
				seenAllow[action] = true
			}
			if denyActions[action] {
				if st.Effect != "Deny" {
					t.Fatalf("%s must be denied, got effect %s", action, st.Effect)
				}
				seenDeny[action] = true
			}
			if allowActions[action] || denyActions[action] {
				for _, res := range st.Resource {
					if res != uploadResource {
						t.Fatalf("%s resource outside upload: %s", action, res)
					}
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
	for action := range denyActions {
		if !seenDeny[action] {
			t.Fatalf("user group missing deny action %s", action)
		}
	}
	text := string(p.Group)
	for _, want := range []string{
		"${aws:username}/upload",
		"${aws:username}/upload/",
		"${aws:username}/upload/*",
		`"Deny"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("user group missing %q", want)
		}
	}
	for _, forbidden := range []string{"${aws:username}/inbound", "${aws:username}/outbound"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("user group must not mention %q", forbidden)
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
	if _, err := LoadPolicies(dir); err == nil || !strings.Contains(err.Error(), policyServerAdmin) {
		t.Fatalf("期望缺少 %s 的错误，得到 %v", policyServerAdmin, err)
	}
}

func TestLoadPoliciesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeValidPolicies(t, dir, "")
	if err := os.WriteFile(filepath.Join(dir, policyUserGroupFile), []byte(`{"Version":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicies(dir); err == nil || !strings.Contains(err.Error(), policyUserGroupFile) {
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
	if _, err := LoadPolicies(dir); err == nil || !strings.Contains(err.Error(), "未知占位符") {
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
	if _, err := LoadPolicies(dir); err == nil || !strings.Contains(err.Error(), "未知占位符") {
		t.Fatalf("期望残留占位符错误，得到 %v", err)
	}
}

func TestLoadPoliciesEmptyDir(t *testing.T) {
	if _, err := LoadPolicies(t.TempDir()); err == nil {
		t.Fatal("空目录应返回错误")
	}
}

func writeValidPolicies(t *testing.T, dir, skip string) {
	t.Helper()
	plain := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::file-transfer/*"]}]}`
	files := map[string]string{
		policyUserGroupFile:  plain,
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

func TestUserPolicyName(t *testing.T) {
	if got := UserPolicyName("alice"); got != "ft-user-alice-policy" {
		t.Fatalf("got %s", got)
	}
}
