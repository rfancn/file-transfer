package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

// newDownloadTestClient 启动只返回固定内容的测试服务端。
func newDownloadTestClient(t *testing.T, remote string, data []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files/"+remote {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"test-etag"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return &Client{baseURL: srv.URL, http: srv.Client()}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
}

// 临时文件 *.part 若被预置为符号链接，不能跟随写入目标文件。
func TestDownloadRejectsSymlinkedPart(t *testing.T) {
	out := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	mustWrite(t, victim, []byte("original"))
	mustSymlink(t, victim, filepath.Join(out, "a.txt.part"))

	c := newDownloadTestClient(t, "inbound/a.txt", []byte("malicious"))
	if err := c.Download(context.Background(), "inbound", "inbound/a.txt", out, false, nil); err == nil {
		t.Fatal("期望拒绝符号链接形式的临时文件")
	}
	if data, _ := os.ReadFile(victim); string(data) != "original" {
		t.Fatalf("符号链接目标被改写: %q", data)
	}
}

// ETag 临时文件若被预置为符号链接，同样不能跟随写入。
func TestDownloadRejectsSymlinkedETagFile(t *testing.T) {
	out := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	mustWrite(t, victim, []byte("original"))
	mustSymlink(t, victim, filepath.Join(out, "a.txt.part.etag"))

	c := newDownloadTestClient(t, "inbound/a.txt", []byte("malicious"))
	if err := c.Download(context.Background(), "inbound", "inbound/a.txt", out, false, nil); err == nil {
		t.Fatal("期望拒绝符号链接形式的 ETag 文件")
	}
	if data, _ := os.ReadFile(victim); string(data) != "original" {
		t.Fatalf("符号链接目标被改写: %q", data)
	}
}

// --force 也不能覆盖符号链接目标。
func TestDownloadRejectsSymlinkedTarget(t *testing.T) {
	out := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	mustWrite(t, victim, []byte("original"))
	mustSymlink(t, victim, filepath.Join(out, "a.txt"))

	c := newDownloadTestClient(t, "inbound/a.txt", []byte("malicious"))
	if err := c.Download(context.Background(), "inbound", "inbound/a.txt", out, true, nil); err == nil {
		t.Fatal("期望拒绝覆盖符号链接目标")
	}
	if data, _ := os.ReadFile(victim); string(data) != "original" {
		t.Fatalf("符号链接目标被改写: %q", data)
	}
}

// 目标位置是目录时拒绝覆盖，不能删除或替换目录。
func TestDownloadRejectsDirectoryTarget(t *testing.T) {
	out := t.TempDir()
	dir := filepath.Join(out, "a.txt")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	c := newDownloadTestClient(t, "inbound/a.txt", []byte("malicious"))
	if err := c.Download(context.Background(), "inbound", "inbound/a.txt", out, true, nil); err == nil {
		t.Fatal("期望拒绝覆盖目录")
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		t.Fatalf("目录被替换或删除: %v", err)
	}
}

// 下载路径中的目录符号链接不能把文件写到输出目录之外。
func TestDownloadRejectsSymlinkedDirEscape(t *testing.T) {
	out := t.TempDir()
	outside := t.TempDir()
	mustSymlink(t, outside, filepath.Join(out, "sub"))

	c := newDownloadTestClient(t, "inbound/sub/a.txt", []byte("malicious"))
	if err := c.Download(context.Background(), "inbound", "inbound/sub/a.txt", out, false, nil); err == nil {
		t.Fatal("期望拒绝逃出下载目录的符号链接")
	}
	if _, err := os.Lstat(filepath.Join(outside, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("文件被写入下载目录之外: %v", err)
	}
}

// --force 对普通文件仍应正常原子覆盖。
func TestDownloadForceOverwritesRegularFile(t *testing.T) {
	out := t.TempDir()
	target := filepath.Join(out, "a.txt")
	mustWrite(t, target, []byte("old"))

	c := newDownloadTestClient(t, "inbound/a.txt", []byte("new content"))
	if err := c.Download(context.Background(), "inbound", "inbound/a.txt", out, true, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new content" {
		t.Fatalf("内容 = %q，期望 new content", data)
	}
}

// 已有合法 .part 时继续续传。
func TestDownloadResumesRegularPart(t *testing.T) {
	out := t.TempDir()
	mustWrite(t, filepath.Join(out, "a.txt.part"), []byte("hello "))
	mustWrite(t, filepath.Join(out, "a.txt.part.etag"), []byte(`"test-etag"`))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=6-" {
			t.Errorf("Range = %q，期望 bytes=6-", r.Header.Get("Range"))
		}
		w.Header().Set("ETag", `"test-etag"`)
		w.Header().Set("Content-Range", "bytes 6-10/11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("world"))
	}))
	defer srv.Close()
	c := &Client{baseURL: srv.URL, http: srv.Client()}

	if err := c.Download(context.Background(), "inbound", "inbound/a.txt", out, false, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(out, "a.txt")); string(data) != "hello world" {
		t.Fatalf("内容 = %q，期望 hello world", data)
	}
}
