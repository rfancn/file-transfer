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
