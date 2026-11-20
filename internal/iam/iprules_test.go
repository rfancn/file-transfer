package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/minio/madmin-go/v3"
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
		{"plain ipv4", []string{"10.0.0.1"}, []string{"10.0.0.1/32"}, false},
		{"plain ipv6", []string{"2001:db8::1"}, []string{"2001:db8::1/128"}, false},
		{"plain and cidr dedupe", []string{"10.0.0.1", "10.0.0.1/32", "2001:db8::1", "2001:db8::1/128"}, []string{"10.0.0.1/32", "2001:db8::1/128"}, false},
		{"dedupe", []string{"10.0.0.1/8", "10.0.0.0/8"}, []string{"10.0.0.0/8"}, false},
		{"trim", []string{"  10.0.0.0/8  "}, []string{"10.0.0.0/8"}, false},
		{"ipv6", []string{"2001:db8::1/32"}, []string{"2001:db8::/32"}, false},
		{"ipv6 zone", []string{"fe80::1%eth0"}, nil, true},
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
			if got == nil {
				t.Fatal("成功时应返回非 nil 切片")
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

	want := map[string]policyDef{
		UserGroupPol:   {UserGroup, UserGroupPol, policyUserGroupFile},
		AdminGroupPol:  {AdminGroup, AdminGroupPol, policyOperatorFile},
		SharedAdminPol: {SharedAdminGroup, SharedAdminPol, policySharedOperatorFile},
	}
	for name, w := range want {
		got, ok := policyDefForPolicy(name)
		if !ok || got != w {
			t.Fatalf("%s 映射 = %#v, want %#v", name, got, w)
		}
	}
	gotPolicies := EditablePolicies()
	wantPolicies := []string{UserGroupPol, AdminGroupPol, SharedAdminPol}
	if len(gotPolicies) != len(wantPolicies) {
		t.Fatalf("EditablePolicies = %v", gotPolicies)
	}
	for i := range gotPolicies {
		if gotPolicies[i] != wantPolicies[i] {
			t.Fatalf("EditablePolicies = %v, want %v", gotPolicies, wantPolicies)
		}
	}
}

func parseStatements(t *testing.T, raw []byte) map[string]policyStatement {
	t.Helper()
	var doc policyDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]policyStatement{}
	for _, st := range doc.Statement {
		out[st.Sid] = st
	}
	return out
}

func sourceIPs(t *testing.T, st policyStatement) []string {
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

	before := parseStatements(t, []byte(testPolicy))
	after := parseStatements(t, updated)
	if !reflect.DeepEqual(before["SharedRead"], after["SharedRead"]) {
		t.Fatalf("非目标语句 SharedRead 被修改: %#v", after["SharedRead"])
	}
	if !reflect.DeepEqual(before["Protect"], after["Protect"]) {
		t.Fatalf("Deny 语句 Protect 被修改: %#v", after["Protect"])
	}
	if !strings.HasSuffix(string(updated), "\n") {
		t.Fatal("输出缺少结尾换行")
	}
	if !strings.Contains(string(updated), "\n  \"Statement\"") {
		t.Fatal("输出不是两空格缩进")
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
		{"empty sid", testPolicy, "", ErrSIDNotFound},
		{"non-string sid", `{"Version":"2012-10-17","Statement":[{"Sid":123,"Effect":"Allow"}]}`, "123", ErrInvalidPolicy},
		{"statement item not object", `{"Version":"2012-10-17","Statement":["x"]}`, "X", ErrInvalidPolicy},
		{"condition not object", `{"Version":"2012-10-17","Statement":[{"Sid":"X","Effect":"Allow","Condition":"x"}]}`, "X", ErrInvalidPolicy},
		{"ipaddress not object", `{"Version":"2012-10-17","Statement":[{"Sid":"X","Effect":"Allow","Condition":{"IpAddress":"x"}}]}`, "X", ErrInvalidPolicy},
		{"duplicate allow then deny", `{"Version":"2012-10-17","Statement":[{"Sid":"X","Effect":"Allow"},{"Sid":"X","Effect":"Deny"}]}`, "X", ErrInvalidPolicy},
		{"duplicate deny then allow", `{"Version":"2012-10-17","Statement":[{"Sid":"X","Effect":"Deny"},{"Sid":"X","Effect":"Allow"}]}`, "X", ErrInvalidPolicy},
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

func TestSetStatementSourceIPNullCondition(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Sid":"X","Effect":"Allow","Condition":null}]}`
	updated, err := SetStatementSourceIP([]byte(policy), "X", []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	got := sourceIPs(t, parseStatements(t, updated)["X"])
	if len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("aws:SourceIp = %v", got)
	}
}

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
	m := &Manager{PolicyDir: dir, Policies: policies, Options: opts}
	def, _ := policyDefForPolicy(UserGroupPol)

	rendered, err := m.renderUpdatedPolicy(def, "UploadWrite", []string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
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
	if !strings.Contains(string(onDisk), "{{SHARED_DIR_OBJECT_ARNS}}") {
		t.Fatalf("模板占位符被替换，写回了渲染内容: %s", onDisk)
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
	m := &Manager{PolicyDir: dir, Policies: policies, Options: opts}
	def, _ := policyDefForPolicy(UserGroupPol)
	if _, err := m.renderUpdatedPolicy(def, "UploadWrite", []string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	rendered, err := m.renderUpdatedPolicy(def, "UploadWrite", nil)
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
	m := &Manager{PolicyDir: dir, Policies: policies, Options: opts}
	def, _ := policyDefForPolicy(UserGroupPol)
	if _, err := m.renderUpdatedPolicy(def, "Missing", nil); !errors.Is(err, ErrSIDNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetStatementIPRangesRejectsServicePolicy(t *testing.T) {
	m := &Manager{}
	_, err := m.SetStatementIPRanges(context.Background(), DataPolicy, "UploadWrite", []string{"10.0.0.0/8"}, IPRangeSet)
	if !errors.Is(err, ErrServicePolicy) {
		t.Fatalf("err = %v", err)
	}
}

func TestHasPolicy(t *testing.T) {
	tests := []struct {
		value, name string
		want        bool
	}{
		{"ft-user-policy", "ft-user-policy", true},
		{"other, ft-user-policy", "ft-user-policy", true},
		{" ft-user-policy , other", "ft-user-policy", true},
		{"other", "ft-user-policy", false},
		{"", "ft-user-policy", false},
	}
	for _, tt := range tests {
		if got := hasPolicy(tt.value, tt.name); got != tt.want {
			t.Fatalf("hasPolicy(%q, %q) = %v, want %v", tt.value, tt.name, got, tt.want)
		}
	}
}

func TestCollectPolicyAttachments(t *testing.T) {
	users := map[string]madmin.UserInfo{
		"alice": {PolicyName: "ft-user-policy"},
		"bob":   {PolicyName: "other, ft-user-policy"},
		"carol": {PolicyName: "other"},
	}
	groups := []string{"ft-user-group", "extra", "other-group"}
	getGroup := func(name string) (*madmin.GroupDesc, error) {
		switch name {
		case "extra":
			return &madmin.GroupDesc{Name: name, Policy: "ft-user-policy"}, nil
		case "other-group":
			return &madmin.GroupDesc{Name: name, Policy: "other"}, nil
		default:
			return &madmin.GroupDesc{Name: name}, nil
		}
	}
	userNames, groupNames, err := collectPolicyAttachments(UserGroupPol, UserGroup, users, groups, getGroup)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(userNames)
	if len(userNames) != 2 || userNames[0] != "alice" || userNames[1] != "bob" {
		t.Fatalf("users = %v", userNames)
	}
	if len(groupNames) != 2 || groupNames[0] != UserGroup || groupNames[1] != "extra" {
		t.Fatalf("groups = %v", groupNames)
	}

	_, _, err = collectPolicyAttachments(UserGroupPol, UserGroup, users, []string{"broken"}, func(string) (*madmin.GroupDesc, error) {
		return nil, errors.New("boom")
	})
	if err == nil {
		t.Fatal("期望组描述读取失败时返回错误")
	}
}

func TestSetStatementIPRangesValidatesCIDRBeforeAttachments(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{PolicyDir: dir, Policies: policies} // Admin 为 nil，模拟 RustFS 不可用
	path := filepath.Join(dir, policyUserGroupFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = m.SetStatementIPRanges(context.Background(), UserGroupPol, "UploadWrite", []string{"bad"}, IPRangeSet)
	if !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("err = %v, want ErrInvalidCIDR", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("非法 CIDR 不应修改模板")
	}
}

func TestPolicySids(t *testing.T) {
	m := &Manager{Policies: loadTestPolicies(t)}
	sids, err := m.PolicySids(UserGroupPol)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"HomeBucketMetadata", "ListOwnDirs", "UploadWrite", "UploadRead",
		"SharedBucketMetadata", "SharedRootList", "SharedDirList", "SharedRead",
	}
	if len(sids) != len(want) {
		t.Fatalf("sids = %v, want %v", sids, want)
	}
	for i := range want {
		if sids[i] != want[i] {
			t.Fatalf("sids = %v, want %v", sids, want)
		}
	}

	if _, err := m.PolicySids(DataPolicy); !errors.Is(err, ErrServicePolicy) {
		t.Fatalf("service policy err = %v", err)
	}
	if _, err := m.PolicySids("custom-policy"); !errors.Is(err, ErrUnsupportedPolicy) {
		t.Fatalf("unsupported policy err = %v", err)
	}
	if _, err := (&Manager{}).PolicySids(UserGroupPol); !errors.Is(err, ErrPolicyRead) {
		t.Fatalf("nil policies err = %v", err)
	}
}

func TestPolicySidsRejectsDuplicate(t *testing.T) {
	p := &Policies{Group: []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"A","Effect":"Allow"},{"Sid":"A","Effect":"Allow"}]}`)}
	m := &Manager{Policies: p}
	if _, err := m.PolicySids(UserGroupPol); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("err = %v, want ErrInvalidPolicy", err)
	}
}

func TestStatementIPRanges(t *testing.T) {
	doc := []byte(`{"Version":"2012-10-17","Statement":[` +
		`{"Sid":"UploadWrite","Effect":"Allow","Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8","192.168.1.1"]}}},` +
		`{"Sid":"DenyOne","Effect":"Deny"}]}`)
	m := &Manager{Policies: &Policies{Group: doc}}

	got, err := m.StatementIPRanges(UserGroupPol, "UploadWrite")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.1/32"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got = %v, want %v", got, want)
	}

	if _, err := m.StatementIPRanges(UserGroupPol, "DenyOne"); !errors.Is(err, ErrStatementNotAllow) {
		t.Fatalf("deny err = %v", err)
	}
	if _, err := m.StatementIPRanges(UserGroupPol, "Missing"); !errors.Is(err, ErrSIDNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if _, err := m.StatementIPRanges(DataPolicy, "UploadWrite"); !errors.Is(err, ErrServicePolicy) {
		t.Fatalf("service err = %v", err)
	}
}

func TestStatementIPRangesStringCondition(t *testing.T) {
	doc := []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow","Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`)
	m := &Manager{Policies: &Policies{Group: doc}}
	got, err := m.StatementIPRanges(UserGroupPol, "UploadWrite")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "10.0.0.0/8" {
		t.Fatalf("got = %v", got)
	}
}

func TestApplyIPRangeModeRejectsOverLimit(t *testing.T) {
	current := make([]string, 60)
	for i := range current {
		current[i] = fmt.Sprintf("10.%d.0.0/16", i)
	}
	changes := make([]string, 50)
	for i := range changes {
		changes[i] = fmt.Sprintf("172.%d.0.0/16", i)
	}
	if _, err := applyIPRangeMode(current, changes, IPRangeAdd); !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("err = %v, want ErrInvalidCIDR", err)
	}
}

func TestApplyIPRangeMode(t *testing.T) {
	current := []string{"10.0.0.0/8", "192.168.1.0/24"}
	tests := []struct {
		name    string
		current []string
		changes []string
		mode    string
		want    []string
		wantErr bool
	}{
		{"set", current, []string{"172.16.0.0/12"}, IPRangeSet, []string{"172.16.0.0/12"}, false},
		{"add", current, []string{"172.16.0.0/12", "10.0.0.0/8"}, IPRangeAdd, []string{"10.0.0.0/8", "192.168.1.0/24", "172.16.0.0/12"}, false},
		{"remove", current, []string{"10.0.0.0/8"}, IPRangeRemove, []string{"192.168.1.0/24"}, false},
		{"remove missing", current, []string{"172.16.0.0/12"}, IPRangeRemove, current, false},
		{"invalid", current, nil, "bad", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyIPRangeMode(tt.current, tt.changes, tt.mode)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidMode) {
					t.Fatalf("err = %v, want ErrInvalidMode", err)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSetStatementIPRangesAddRemove(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	oldDoc := []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow","Action":["s3:PutObject"],"Resource":["*"]}]}`)
	fake, adm := newFakeAdminServer(t, oldDoc)
	m := &Manager{Admin: adm, Policies: policies, PolicyDir: dir, Options: opts}

	if _, err := m.SetStatementIPRanges(context.Background(), UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, IPRangeSet); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetStatementIPRanges(context.Background(), UserGroupPol, "UploadWrite", []string{"192.168.1.0/24"}, IPRangeAdd); err != nil {
		t.Fatal(err)
	}
	assertStatementIPs(t, fake.lastAddedPolicy(t), "UploadWrite", []string{"10.0.0.0/8", "192.168.1.0/24"})

	if _, err := m.SetStatementIPRanges(context.Background(), UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, IPRangeRemove); err != nil {
		t.Fatal(err)
	}
	assertStatementIPs(t, fake.lastAddedPolicy(t), "UploadWrite", []string{"192.168.1.0/24"})
}

func assertStatementIPs(t *testing.T, policy []byte, sid string, want []string) {
	t.Helper()
	got := sourceIPs(t, parseStatements(t, policy)[sid])
	if len(got) != len(want) {
		t.Fatalf("aws:SourceIp = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("aws:SourceIp = %v, want %v", got, want)
		}
	}
}
