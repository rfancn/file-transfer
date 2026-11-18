package main

import (
	"reflect"
	"testing"

	"github.com/rfancn/file-transfer/internal/iam"
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
		{"mixed", []string{"a,b", "c"}, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitCIDRs(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got = %#v, want = %#v", got, tt.want)
			}
		})
	}
}

func TestValidatePolicyIPArgs(t *testing.T) {
	tests := []struct {
		name       string
		policyName string
		sid        string
		cidrs      []string
		clear      bool
		wantErr    bool
	}{
		{"valid set", iam.UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, false, false},
		{"valid clear", iam.UserGroupPol, "UploadWrite", nil, true, false},
		{"trimmed policy", " " + iam.UserGroupPol + " ", "UploadWrite", []string{"10.0.0.0/8"}, false, false},
		{"clear with cidrs", iam.UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, true, true},
		{"service policy", iam.DataPolicy, "UploadWrite", []string{"10.0.0.0/8"}, false, true},
		{"unsupported policy", "custom-policy", "UploadWrite", []string{"10.0.0.0/8"}, false, true},
		{"empty sid", iam.UserGroupPol, "  ", []string{"10.0.0.0/8"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePolicyIPArgs(tt.policyName, tt.sid, tt.cidrs, tt.clear)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestPolicyIPLabelsMatchEditablePolicies(t *testing.T) {
	for _, name := range iam.EditablePolicies() {
		if policyIPLabels[name] == "" {
			t.Fatalf("策略 %s 缺少菜单标签", name)
		}
	}
}
