package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestAdminAddUserRejectsInvalidGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{App: &app.App{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"username":"alice","group":"bad-group"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.AdminAddUser(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_group") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestAdminAddUserOperatorRequiresConfirm(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{App: &app.App{}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/admin/users",
		strings.NewReader(`{"username":"alice","group":"ft-operator-group"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.AdminAddUser(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d，期望 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "operator_confirm_required") {
		t.Fatalf("body = %s", w.Body.String())
	}
}
