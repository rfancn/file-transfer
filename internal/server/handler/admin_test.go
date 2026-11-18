package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestAdminAddUserRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body string
		code string
	}{
		{"invalid group", `{"username":"alice","group":"bad-group"}`, "invalid_group"},
		{"operator requires confirm", `{"username":"alice","group":"ft-operator-group"}`, "operator_confirm_required"},
		{"shared operator requires confirm", `{"username":"alice","group":"shared-operator-group"}`, "operator_confirm_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{App: &app.App{}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/users", strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")

			h.AdminAddUser(c)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d，期望 400", w.Code)
			}
			if !strings.Contains(w.Body.String(), tt.code) {
				t.Fatalf("body = %s", w.Body.String())
			}
		})
	}
}

func TestAdminSetStatementIPRangesRejects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name   string
		policy string
		sid    string
		body   string
		status int
		code   string
	}{
		{"missing cidrs", "ft-user-policy", "UploadWrite", `{}`, http.StatusBadRequest, "invalid_request"},
		{"invalid cidr", "ft-user-policy", "UploadWrite", `{"cidrs":["bad"]}`, http.StatusBadRequest, "invalid_cidr"},
		{"service policy", "ft-admin-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"]}`, http.StatusForbidden, "service_policy"},
		{"unsupported policy", "custom-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"]}`, http.StatusBadRequest, "unsupported_policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{App: &app.App{IAM: &iam.Manager{}}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPut, "/v1/admin/policies/x/statements/y/ip-ranges", strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Params = gin.Params{
				{Key: "name", Value: tt.policy},
				{Key: "sid", Value: tt.sid},
			}

			h.AdminSetStatementIPRanges(c)

			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", w.Code, tt.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tt.code) {
				t.Fatalf("body = %s", w.Body.String())
			}
		})
	}
}
