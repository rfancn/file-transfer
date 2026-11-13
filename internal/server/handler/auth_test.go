package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/adminuser"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestAdminLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, password, err := adminuser.LoadOrCreate(filepath.Join(t.TempDir(), "secrets", "user.json"))
	if err != nil {
		t.Fatal(err)
	}
	secret := "test-secret"
	a := &app.App{
		Admins: store,
		Cfg:    config.ServerConfig{Auth: config.AuthSection{TokenSecret: secret}},
		RT:     config.Runtime{TokenTTL: time.Hour},
	}
	h := New(a)
	r := gin.New()
	r.POST("/v1/admin/login", h.AdminLogin)

	body := fmt.Sprintf(`{"username":"admin","password":%q}`, password)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/login", strings.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out protocol.AdminLoginResponse
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	var claims auth.AdminClaims
	if err := auth.Verify(secret, out.Token, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "admin" || claims.Kind != "admin" {
		t.Fatalf("claims = %+v", claims)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/admin/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAdminLoginFailureDoesNotSetAuditActor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, _, err := adminuser.LoadOrCreate(filepath.Join(t.TempDir(), "secrets", "user.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := New(&app.App{
		Admins: store,
		Cfg:    config.ServerConfig{Auth: config.AuthSection{TokenSecret: "test-secret"}},
		RT:     config.Runtime{TokenTTL: time.Hour},
	})
	r := gin.New()
	var actor string
	r.POST("/v1/admin/login", func(c *gin.Context) {
		h.AdminLogin(c)
		actor = c.GetString("admin")
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/login", strings.NewReader(`{"username":"attacker","password":"wrong"}`))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if actor != "" {
		t.Fatalf("认证失败的请求不应写入审计身份，得到 %q", actor)
	}
}
