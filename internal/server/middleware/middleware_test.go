package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestAdminAuth(t *testing.T) {
	secret := "test-secret"
	a := &app.App{Cfg: config.ServerConfig{Auth: config.AuthSection{TokenSecret: secret}}}
	r := gin.New()
	r.Use(AdminAuth(a))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	now := time.Now()
	valid, _ := auth.Sign(secret, auth.AdminClaims{Sub: "admin", Kind: "admin", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})
	expired, _ := auth.Sign(secret, auth.AdminClaims{Sub: "admin", Kind: "admin", Iat: now.Add(-2 * time.Hour).Unix(), Exp: now.Add(-time.Hour).Unix()})
	wrongKind, _ := auth.Sign(secret, auth.AdminClaims{Sub: "admin", Kind: "session", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})

	tests := []struct {
		name  string
		token string
		want  int
	}{
		{name: "missing", want: http.StatusUnauthorized},
		{name: "invalid", token: "bad-token", want: http.StatusUnauthorized},
		{name: "expired", token: expired, want: http.StatusUnauthorized},
		{name: "wrong kind", token: wrongKind, want: http.StatusUnauthorized},
		{name: "valid", token: valid, want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d", rr.Code, tt.want)
			}
		})
	}
}

func TestUserAuth(t *testing.T) {
	secret := "test-secret"
	a := &app.App{Cfg: config.ServerConfig{Auth: config.AuthSection{TokenSecret: secret}}}
	r := gin.New()
	r.Use(UserAuth(a))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, c.GetString("username")) })

	now := time.Now()
	valid, _ := auth.Sign(secret, auth.SessionClaims{Sub: "alice", Kind: "session", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})
	adminToken, _ := auth.Sign(secret, auth.AdminClaims{Sub: "admin", Kind: "admin", Iat: now.Unix(), Exp: now.Add(time.Hour).Unix()})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+valid)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "alice" {
		t.Fatalf("status = %d body = %q", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("admin token should not access client routes: %d", rr.Code)
	}
}

func TestRequireReady(t *testing.T) {
	for _, ready := range []bool{false, true} {
		a := &app.App{Ready: ready}
		r := gin.New()
		r.Use(RequireReady(a))
		r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
		want := http.StatusServiceUnavailable
		if ready {
			want = http.StatusOK
		}
		if rr.Code != want {
			t.Fatalf("ready=%v: status = %d, want %d", ready, rr.Code, want)
		}
	}
}
