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

func TestBuildPolicyIPItems(t *testing.T) {
	cidrs := []string{"10.0.0.0/8", "192.168.1.0/24"}
	items, actions := buildPolicyIPItems(cidrs, map[string]bool{"192.168.1.0/24": true})
	if len(items) != 5 || len(actions) != 5 {
		t.Fatalf("items=%d actions=%d", len(items), len(actions))
	}
	if items[0].Text != "10.0.0.0/8" || !items[0].Selectable || items[0].Checked {
		t.Fatalf("items[0] = %+v", items[0])
	}
	if items[1].Text != "192.168.1.0/24" || !items[1].Checked {
		t.Fatalf("items[1] = %+v", items[1])
	}
	if actions[2].kind != ipActionDelete || items[2].Selectable {
		t.Fatalf("delete action = %+v item=%+v", actions[2], items[2])
	}
	for i, want := range []string{ipActionAdd, ipActionReturn} {
		if actions[3+i].kind != want {
			t.Fatalf("actions[%d] = %q, want %q", 3+i, actions[3+i].kind, want)
		}
	}

	got := checkedIPs(items, actions)
	if len(got) != 1 || got[0] != "192.168.1.0/24" {
		t.Fatalf("checkedIPs = %v", got)
	}
}

func TestPolicyIPLabelsMatchEditablePolicies(t *testing.T) {
	for _, name := range iam.EditablePolicies() {
		if policyIPLabels[name] == "" {
			t.Fatalf("策略 %s 缺少菜单标签", name)
		}
	}
}
