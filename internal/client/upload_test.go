package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rfancn/file-transfer/internal/protocol"
)

func TestPlanUploadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanUpload(p, "renamed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Total != 5 {
		t.Fatalf("plan=%+v", plan)
	}
	if !strings.HasSuffix(plan.Files[0].Remote, "/renamed.txt") || !strings.HasPrefix(plan.Files[0].Remote, "outbound/") {
		t.Fatalf("remote=%s", plan.Files[0].Remote)
	}
}

func TestPlanUploadDirSkipsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("bb"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "link.txt"))
	plan, err := PlanUpload(dir, "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("files=%+v", plan.Files)
	}
	for _, f := range plan.Files {
		if strings.Contains(f.Remote, "link.txt") {
			t.Fatalf("symlink included: %s", f.Remote)
		}
		if !strings.Contains(f.Remote, "/root/") {
			t.Fatalf("remote=%s", f.Remote)
		}
	}
	if plan.Total != 3 {
		t.Fatalf("total=%d", plan.Total)
	}
}

func TestEscapePath(t *testing.T) {
	got := escapePath("inbound/a b/c#d.txt")
	want := "inbound/a%20b/c%23d.txt"
	if got != want {
		t.Fatalf("escapePath = %q, want %q", got, want)
	}
}

func TestInterrupted(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	local := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}

	c := &Client{username: "alice"}
	sp, err := statePath(c.username, local)
	if err != nil {
		t.Fatal(err)
	}
	st := &uploadState{
		Path: local, Size: info.Size(), ModTime: info.ModTime().Unix(),
		PartSize: 5 << 20, UploadID: "upload-id",
		Parts: []protocol.PartInfo{{Number: 1, Size: 3, ETag: "etag"}},
	}
	if err := saveState(sp, st); err != nil {
		t.Fatal(err)
	}

	plan := UploadPlan{Files: []UploadFile{{Local: local, Remote: "outbound/20261113/a.bin", Size: info.Size()}}}
	items := c.Interrupted(plan)
	if len(items) != 1 {
		t.Fatalf("items=%+v", items)
	}
	if got := items[0]; got.Parts != 1 || got.UploadedBytes != 3 || got.UploadID != "upload-id" {
		t.Fatalf("item=%+v", got)
	}

	// 文件变化后旧状态不再匹配。
	if err := os.WriteFile(local, []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	plan.Files[0].Size = updated.Size()
	if items := c.Interrupted(plan); len(items) != 0 {
		t.Fatalf("changed file should not match: %+v", items)
	}

	// 丢弃本地续传状态。
	c.DiscardInterrupted(context.Background(), []InterruptedUpload{{Local: local}})
	if _, err := os.Stat(sp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state should be removed, err=%v", err)
	}
}

func TestPutDirectZeroSizeUsesContentLength(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var gotLen int64
	var transferEncoding []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		transferEncoding = append([]string(nil), r.TransferEncoding...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	if err := c.putDirect(context.Background(), UploadFile{Local: p, Remote: "outbound/20261113/empty", Size: 0}, nil); err != nil {
		t.Fatal(err)
	}
	if gotLen != 0 {
		t.Fatalf("服务端 Content-Length = %d，期望 0", gotLen)
	}
	if len(transferEncoding) != 0 {
		t.Fatalf("0 字节文件不应使用 chunked: %v", transferEncoding)
	}
}

func TestUploadRecoversFromMissingMultipart(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := t.TempDir()
	local := filepath.Join(dir, "big.bin")
	const size = 5 << 20
	if err := os.WriteFile(local, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}

	c := &Client{username: "alice"}
	sp, err := statePath(c.username, local)
	if err != nil {
		t.Fatal(err)
	}
	st := &uploadState{
		Path: local, Size: size, ModTime: info.ModTime().Unix(),
		PartSize: size, UploadID: "old",
		Parts: []protocol.PartInfo{{Number: 1, Size: size, ETag: "old-etag"}},
	}
	if err := saveState(sp, st); err != nil {
		t.Fatal(err)
	}

	var initCount, partCount, completeCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/uploads":
			initCount++
			fmt.Fprintf(w, `{"upload_id":"new","part_size":%d}`, size)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/uploads/old/parts":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"upload_not_found","message":"分片上传不存在"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/uploads/new/parts":
			_, _ = io.WriteString(w, `{"parts":[]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/uploads/new/parts/1":
			partCount++
			_, _ = io.Copy(io.Discard, r.Body)
			fmt.Fprintf(w, `{"number":1,"size":%d,"etag":"new-etag"}`, size)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/uploads/new":
			completeCount++
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":"unexpected","message":"%s %s"}`, r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c.baseURL = srv.URL
	c.http = srv.Client()
	c.policy = protocol.UploadPolicy{DirectMax: 1}
	err = c.uploadFile(context.Background(), UploadFile{Local: local, Remote: "outbound/20261113/big.bin", Size: size}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if initCount != 1 || partCount != 1 || completeCount != 1 {
		t.Fatalf("init=%d part=%d complete=%d，期望各 1 次", initCount, partCount, completeCount)
	}
	if _, err := os.Stat(sp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("上传完成后状态文件应被清理, err=%v", err)
	}
}

func TestClientPartSize(t *testing.T) {
	c := &Client{policy: protocol.UploadPolicy{
		DirectMax: 100 << 20,
		MaxPart:   512 << 20,
		Tiers: []protocol.Tier{
			{Max: 5 << 30, Part: 16 << 20},
			{Max: 50 << 30, Part: 32 << 20},
			{Max: 625 << 30, Part: 64 << 20},
			{Max: 1280 << 30, Part: 128 << 20},
		},
	}}
	cases := []struct {
		size int64
		want int64
	}{
		{1 << 20, 0},
		{100 << 20, 16 << 20},
		{5 << 30, 32 << 20},
		{50 << 30, 64 << 20},
		{625 << 30, 128 << 20},
		{2 << 40, 512 << 20},
	}
	for _, tc := range cases {
		if got := c.partSize(tc.size); got != tc.want {
			t.Fatalf("size %d: got %d want %d", tc.size, got, tc.want)
		}
	}
}
