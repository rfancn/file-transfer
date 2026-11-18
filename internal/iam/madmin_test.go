package iam

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/minio/madmin-go/v3"
)

// fakeAdminServer 模拟 madmin 所需的 RustFS 管理端点。
type fakeAdminServer struct {
	mu      sync.Mutex
	current []byte
	addBody []byte
	failAdd bool
}

func newFakeAdminServer(t *testing.T, current []byte) (*fakeAdminServer, *madmin.AdminClient) {
	t.Helper()
	f := &fakeAdminServer{current: current}
	ts := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(ts.Close)
	adm, err := madmin.New(strings.TrimPrefix(ts.URL, "http://"), "ak", "sk", false)
	if err != nil {
		t.Fatal(err)
	}
	return f, adm
}

func (f *fakeAdminServer) setFailAdd(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAdd = fail
}

func (f *fakeAdminServer) capturedAddBody() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.addBody...)
}

func (f *fakeAdminServer) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/minio/admin/v3/info-canned-policy":
		f.mu.Lock()
		current := append([]byte(nil), f.current...)
		f.mu.Unlock()
		_, _ = w.Write(current)
	case "/minio/admin/v3/remove-canned-policy":
		w.WriteHeader(http.StatusOK)
	case "/minio/admin/v3/add-canned-policy":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		fail := f.failAdd
		if !fail {
			f.addBody = append([]byte(nil), body...)
		}
		f.mu.Unlock()
		if fail {
			http.Error(w, "add policy failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/minio/admin/v3/idp/builtin/policy/attach", "/minio/admin/v3/idp/builtin/policy/detach":
		w.WriteHeader(http.StatusNoContent)
	case "/minio/admin/v3/list-users":
		// ListUsers 的响应由 madmin 解密，这里用客户端相同的密钥加密。
		data, err := madmin.EncryptData("sk", []byte("{}"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(data)
	case "/minio/admin/v3/groups":
		_, _ = w.Write([]byte("[]"))
	case "/minio/admin/v3/group":
		_, _ = w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

const fakeOldPolicy = `{"Version":"2012-10-17","Statement":[{"Sid":"Old","Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::old/*"]}]}`

const fakeNewGroupPolicy = `{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::home/*"],"Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}}]}`

func TestReplacePolicyFromDocumentUsesLatestDocument(t *testing.T) {
	fake, adm := newFakeAdminServer(t, []byte(fakeOldPolicy))
	policies := &Policies{}
	if err := policies.SetDocument(UserGroupPol, []byte(fakeNewGroupPolicy)); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Admin: adm, Policies: policies}

	// 走 applyGroupPolicy 调用点，验证读取发生在写锁内。
	if err := m.applyGroupPolicy(context.Background(), UserGroup); err != nil {
		t.Fatal(err)
	}
	body := string(fake.capturedAddBody())
	if !strings.Contains(body, "aws:SourceIp") {
		t.Fatalf("下发的策略未包含最新内存文档: %s", body)
	}
}

func TestSetStatementIPRangesAppliesRenderedPolicy(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	fake, adm := newFakeAdminServer(t, []byte(fakeOldPolicy))
	m := &Manager{Admin: adm, PolicyDir: dir, Policies: policies}

	normalized, err := m.SetStatementIPRanges(context.Background(), UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != 1 || normalized[0] != "10.0.0.0/8" {
		t.Fatalf("normalized = %v", normalized)
	}
	body := string(fake.capturedAddBody())
	if !strings.Contains(body, "aws:SourceIp") {
		t.Fatalf("下发的策略未包含 IP 条件: %s", body)
	}
	if strings.Contains(body, "{{") {
		t.Fatalf("下发了未渲染的占位符: %s", body)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, policyUserGroupFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(onDisk), "{{SHARED_DIR_OBJECT_ARNS}}") {
		t.Fatalf("磁盘模板应为未渲染内容: %s", onDisk)
	}
	mem, err := policies.Document(UserGroupPol)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mem), "aws:SourceIp") {
		t.Fatalf("内存文档未更新: %s", mem)
	}
}

func TestSetStatementIPRangesApplyFailed(t *testing.T) {
	dir, opts := setupPolicyDir(t)
	policies, err := LoadPolicies(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	fake, adm := newFakeAdminServer(t, []byte(fakeOldPolicy))
	fake.setFailAdd(true)
	m := &Manager{Admin: adm, PolicyDir: dir, Policies: policies}

	if _, err := m.SetStatementIPRanges(context.Background(), UserGroupPol, "UploadWrite", []string{"10.0.0.0/8"}, opts); !errors.Is(err, ErrApplyFailed) {
		t.Fatalf("err = %v, want ErrApplyFailed", err)
	}
}
