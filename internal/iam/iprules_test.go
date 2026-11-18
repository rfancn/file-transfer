package iam

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
		{"plain ip", []string{"10.0.0.1"}, nil, true},
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

	want := map[string]struct {
		file  string
		group string
	}{
		UserGroupPol:   {policyUserGroupFile, UserGroup},
		AdminGroupPol:  {policyOperatorFile, AdminGroup},
		SharedAdminPol: {policySharedOperatorFile, SharedAdminGroup},
	}
	for name, w := range want {
		if got := editablePolicies[name]; got != w {
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
