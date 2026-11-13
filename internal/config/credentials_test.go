package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadClientCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := ClientCredentials{
		Username: "alice",
		Password: "secret",
	}
	if err := SaveClientCredentials(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(home, clientCredentialsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("权限 = %o, want 600", perm)
	}
	got, err := LoadClientCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadClientCredentialsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := LoadClientCredentials(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestRemoveClientCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := SaveClientCredentials(ClientCredentials{Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveClientCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, clientCredentialsFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("文件应已删除, err = %v", err)
	}
}
