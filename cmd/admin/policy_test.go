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
