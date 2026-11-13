package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func testApp(ready bool) *app.App {
	return &app.App{
		Cfg:   config.ServerConfig{Auth: config.AuthSection{TokenSecret: "test-secret"}},
		Ready: ready,
	}
}

func TestRouteGroupsRequireAuth(t *testing.T) {
	r := New(testApp(true))
	tests := []struct {
		method, target string
		body           string
		want           int
	}{
		{http.MethodGet, "/v1/files", "", http.StatusUnauthorized},
		{http.MethodGet, "/v1/files/inbound/a.txt", "", http.StatusUnauthorized},
		{http.MethodPut, "/v1/files/outbound/a.txt", "x", http.StatusUnauthorized},
		{http.MethodPost, "/v1/uploads", "{}", http.StatusUnauthorized},
		{http.MethodPut, "/v1/uploads/up/parts/1", "x", http.StatusUnauthorized},
		{http.MethodGet, "/v1/uploads/up/parts", "", http.StatusUnauthorized},
		{http.MethodPut, "/v1/uploads/up", "{}", http.StatusUnauthorized},
		{http.MethodDelete, "/v1/uploads/up", "", http.StatusUnauthorized},
		{http.MethodGet, "/v1/admin/users", "", http.StatusUnauthorized},
		{http.MethodPut, "/v1/admin/users/alice/password", "", http.StatusUnauthorized},
		{http.MethodPost, "/v1/admin/bootstrap", "{}", http.StatusUnauthorized},
		{http.MethodDelete, "/v1/admin/data/inbound?days=7", "", http.StatusUnauthorized},
		// 登录接口公开；空用户名直接返回 401，不会访问管理员存储。
		{http.MethodPost, "/v1/admin/login", "{}", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		var body *strings.Reader
		if tt.body == "" {
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(tt.body)
		}
		req := httptest.NewRequest(tt.method, tt.target, body)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != tt.want {
			t.Fatalf("%s %s: status = %d, want %d", tt.method, tt.target, rr.Code, tt.want)
		}
	}
}

func TestClientGroupRequiresReady(t *testing.T) {
	r := New(testApp(false))
	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}
