package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestWritePolicyError(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{iam.ErrInvalidCIDR, http.StatusBadRequest, "invalid_cidr"},
		{iam.ErrServicePolicy, http.StatusForbidden, "service_policy"},
		{iam.ErrUnsupportedPolicy, http.StatusBadRequest, "unsupported_policy"},
		{iam.ErrSIDNotFound, http.StatusBadRequest, "sid_not_found"},
		{iam.ErrStatementNotAllow, http.StatusBadRequest, "statement_not_allow"},
		{iam.ErrInvalidPolicy, http.StatusInternalServerError, "invalid_policy"},
		{iam.ErrPolicyRead, http.StatusInternalServerError, "policy_read_failed"},
		{iam.ErrPolicyWrite, http.StatusInternalServerError, "policy_write_failed"},
		{iam.ErrApplyFailed, http.StatusBadGateway, "apply_failed"},
		{errors.New("other"), http.StatusBadGateway, "rustfs_unavailable"},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		writePolicyError(c, tt.err)
		if w.Code != tt.status {
			t.Fatalf("%v: status = %d, want %d", tt.err, w.Code, tt.status)
		}
		if !strings.Contains(w.Body.String(), tt.code) {
			t.Fatalf("%v: body = %s", tt.err, w.Body.String())
		}
	}
}

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
		{"null cidrs", "ft-user-policy", "UploadWrite", `{"cidrs":null}`, http.StatusBadRequest, "invalid_request"},
		{"malformed json", "ft-user-policy", "UploadWrite", `{"cidrs":`, http.StatusBadRequest, "invalid_request"},
		{"empty body", "ft-user-policy", "UploadWrite", ``, http.StatusBadRequest, "invalid_request"},
		// 空数组表示清除，应通过绑定与 nil 校验并进入 IAM 层；测试中 Admin 为 nil，
		// 因此最终返回 rustfs_unavailable，而不是 invalid_request。
		{"empty cidrs reaches iam", "ft-user-policy", "UploadWrite", `{"cidrs":[]}`, http.StatusBadGateway, "rustfs_unavailable"},
		{"invalid cidr", "ft-user-policy", "UploadWrite", `{"cidrs":["bad"]}`, http.StatusBadRequest, "invalid_cidr"},
		{"service policy", "ft-admin-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"]}`, http.StatusForbidden, "service_policy"},
		{"unsupported policy", "custom-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"]}`, http.StatusBadRequest, "unsupported_policy"},
		{"invalid mode", "ft-user-policy", "UploadWrite", `{"cidrs":["10.0.0.0/8"],"mode":"bad"}`, http.StatusBadRequest, "invalid_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{App: &app.App{IAM: &iam.Manager{Policies: &iam.Policies{
				Group: []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow"}]}`),
			}}}}
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

func TestAdminGetStatementIPRanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doc := []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow","Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}}]}`)
	h := &Handler{App: &app.App{IAM: &iam.Manager{Policies: &iam.Policies{Group: doc}}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/policies/ft-user-policy/statements/UploadWrite/ip-ranges", nil)
	c.Params = gin.Params{
		{Key: "name", Value: iam.UserGroupPol},
		{Key: "sid", Value: "UploadWrite"},
	}

	h.AdminGetStatementIPRanges(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var out protocol.SetStatementIPRangesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Cidrs) != 1 || out.Cidrs[0] != "10.0.0.0/8" {
		t.Fatalf("out = %+v", out)
	}
}

func TestAdminListPolicySids(t *testing.T) {
	gin.SetMode(gin.TestMode)
	doc := []byte(`{"Version":"2012-10-17","Statement":[{"Sid":"UploadWrite","Effect":"Allow"},{"Sid":"DenyOne","Effect":"Deny"}]}`)
	h := &Handler{App: &app.App{IAM: &iam.Manager{Policies: &iam.Policies{Group: doc}}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/admin/policies/ft-user-policy/sids", nil)
	c.Params = gin.Params{{Key: "name", Value: iam.UserGroupPol}}

	h.AdminListPolicySids(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var out protocol.ListPolicySidsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Policy != iam.UserGroupPol || len(out.Sids) != 1 || out.Sids[0] != "UploadWrite" {
		t.Fatalf("out = %+v", out)
	}
}
