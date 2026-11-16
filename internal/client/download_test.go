package client

import (
	"path/filepath"
	"testing"
)

func TestLocalPath(t *testing.T) {
	out := t.TempDir()
	cases := []struct {
		dir, rel, want string
	}{
		{"inbound", "inbound/2026111301/a.pdf", filepath.Join(out, "2026111301", "a.pdf")},
		{"upload", "project-a/a.zip", filepath.Join(out, "project-a", "a.zip")},
	}
	for _, tc := range cases {
		got, err := LocalPath(tc.dir, tc.rel, out)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.dir, tc.rel, err)
		}
		if got != tc.want {
			t.Fatalf("LocalPath(%q, %q) = %q, want %q", tc.dir, tc.rel, got, tc.want)
		}
	}
}

func TestLocalPathRejectsTraversal(t *testing.T) {
	for _, rel := range []string{"inbound/../x", "../x", "/abs/path"} {
		if _, err := LocalPath("inbound", rel, t.TempDir()); err == nil {
			t.Fatalf("%q should be rejected", rel)
		}
	}
}
