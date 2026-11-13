package iam

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPoliciesValidJSON(t *testing.T) {
	pols := map[string][]byte{
		"group":        GroupPolicy("file-transfer"),
		"user":         UserPolicy("file-transfer", "alice"),
		"admin":        AdminPolicy("file-transfer"),
		"server-data":  ServerDataPolicy("file-transfer"),
		"server-admin": ServerAdminPolicy("file-transfer"),
	}
	for name, p := range pols {
		var v any
		if err := json.Unmarshal(p, &v); err != nil {
			t.Fatalf("%s invalid JSON: %v", name, err)
		}
		if !strings.Contains(string(p), "s3:") {
			t.Fatalf("%s missing s3 action", name)
		}
	}
}

func TestUserPolicyCannotDeleteUpload(t *testing.T) {
	p := string(UserPolicy("file-transfer", "alice"))
	if strings.Contains(p, "s3:DeleteObject") {
		t.Fatalf("user policy must not allow deleting upload objects: %s", p)
	}
}

func TestOperatorPolicyCanDeleteInbound(t *testing.T) {
	var doc struct {
		Statement []struct {
			Effect   string
			Action   []string
			Resource []string
		}
	}
	if err := json.Unmarshal(AdminPolicy("file-transfer"), &doc); err != nil {
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
	var doc struct {
		Statement []struct {
			Effect    string
			Action    []string
			Condition map[string]map[string]any
		}
	}
	if err := json.Unmarshal(AdminPolicy("file-transfer"), &doc); err != nil {
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
	p := string(AdminPolicy("file-transfer"))
	for _, want := range []string{"s3:delimiter", "*/upload/", `"Deny"`} {
		if !strings.Contains(p, want) {
			t.Fatalf("operator policy missing %q: %s", want, p)
		}
	}
}

func TestServerDataPolicyKeepsPlaceholders(t *testing.T) {
	p := string(ServerDataPolicy("file-transfer"))
	for _, want := range []string{"*/inbound/.keep", "*/outbound/.keep", "*/upload/.keep"} {
		if !strings.Contains(p, want) {
			t.Fatalf("missing %s in %s", want, p)
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
