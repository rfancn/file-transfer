package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePolicyDir(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wd, "policy")
	if got := resolvePolicyDir(); got != want {
		t.Fatalf("resolvePolicyDir = %q, want %q", got, want)
	}
}
