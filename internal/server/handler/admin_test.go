package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
