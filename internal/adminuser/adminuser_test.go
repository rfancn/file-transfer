package adminuser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	p := Path()
	if !filepath.IsAbs(p) {
		t.Fatalf("path = %q, want absolute", p)
	}
	if filepath.Base(filepath.Dir(p)) != dirName || filepath.Base(p) != fileName {
		t.Fatalf("path = %q", p)
	}
}

func TestLoadOrCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), dirName, fileName)
	store, password, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(password) != passwordLen {
		t.Fatalf("password length = %d", len(password))
	}
	if !store.Verify(DefaultName, password) {
		t.Fatal("correct password should pass")
	}
	if store.Verify(DefaultName, "wrong") {
		t.Fatal("wrong password should fail")
	}
	if store.Verify("other", password) {
		t.Fatal("unknown user should fail")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %#o", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %#o", perm)
	}

	// 二次启动不应重置密码。
	store2, password2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if password2 != "" {
		t.Fatalf("password regenerated: %q", password2)
	}
	if !store2.Verify(DefaultName, password) {
		t.Fatal("existing password should still pass")
	}
}

func TestLoadOrCreateEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), dirName, fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, password, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if password == "" {
		t.Fatal("empty file should generate admin")
	}
}

func TestLoadOrCreateInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), dirName, fileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreate(path); err == nil {
		t.Fatal("invalid file should return error")
	}
}
