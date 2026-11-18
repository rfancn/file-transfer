package handler

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/bootstrap"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/server/response"
)

// AdminInit 用 RustFS root 凭据完成系统初始化。
func (h *Handler) AdminInit(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req struct {
		AccessKey string `json:"access_key"`
		SecretKey string `json:"secret_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AccessKey == "" || req.SecretKey == "" {
		response.Err(c, http.StatusBadRequest, "invalid_request", "access_key/secret_key 不能为空")
		return
	}

	// 用 root 凭据实际访问一次 RustFS，验证其管理权限。
	probe, err := iam.New(h.Cfg.S3.Endpoint, req.AccessKey, req.SecretKey, h.Cfg.S3.Region, h.Cfg.S3.HomeBucket)
	if err != nil {
		logging.Error("admin init 创建管理客户端失败", "err", err)
		response.Err(c, http.StatusInternalServerError, "internal_error", "创建 RustFS 管理客户端失败")
		return
	}
	if _, err := probe.UserExists(c.Request.Context(), iam.DataUser); err != nil {
		logging.Warn("admin init 凭据验证失败", "endpoint", h.Cfg.S3.Endpoint, "access_key", req.AccessKey, "err", err)
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "signature") {
			response.Err(c, http.StatusUnauthorized, "invalid_credentials", "root Access Key / Secret Key 不匹配，请确认使用的是 RustFS S3 root 凭据")
			return
		}
		if strings.Contains(low, "access") || strings.Contains(low, "denied") ||
			strings.Contains(low, "invalid") || strings.Contains(low, "not allowed") {
			response.Err(c, http.StatusUnauthorized, "invalid_credentials", "root 凭据无效")
			return
		}
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "无法连接 RustFS，请检查 ft-server 的 s3.endpoint")
		return
	}

	res, err := bootstrap.Run(c.Request.Context(), h.Cfg, h.Policies, req.AccessKey, req.SecretKey)
	if err != nil {
		logging.Error("admin init", "err", err)
		response.Err(c, http.StatusBadGateway, "init_failed", "初始化失败")
		return
	}
	if _, err := config.WriteServerConfig(h.RT.ConfigPath, h.Cfg); err != nil {
		logging.Error("保存服务端配置", "err", err)
		response.Err(c, http.StatusInternalServerError, "internal_error", "写入配置失败")
		return
	}
	if res.DataPasswordReset {
		logging.Warn("服务身份密码缺失，已重置并写入 secrets/s3_credentials.json", "username", iam.DataUser, "path", config.ServiceSecretsPath())
	}
	if res.AdminPasswordReset {
		logging.Warn("服务身份密码缺失，已重置并写入 secrets/s3_credentials.json", "username", iam.ServerAdmin, "path", config.ServiceSecretsPath())
	}
	response.JSON(c, http.StatusOK, map[string]any{"ok": true, "restart": res.ConfigChanged})

	if !res.ConfigChanged {
		return
	}
	// 首次初始化后退出，由 systemd 重新拉起并加载完整配置。
	go func() {
		time.Sleep(500 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.Shutdown(ctx)
	}()
}

// AdminAddUser 由 ft-server 用服务身份创建用户。
func (h *Handler) AdminAddUser(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req protocol.AddUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "请求参数错误")
		return
	}
	username := strings.TrimSpace(req.Username)
	c.Set("audit_target", username)
	if err := auth.ValidateUsername(username); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	group := strings.TrimSpace(req.Group)
	if group == "" {
		group = iam.UserGroup
	}
	if !iam.IsValidGroup(group) {
		response.Err(c, http.StatusBadRequest, "invalid_group", "组只允许 ft-user-group、ft-operator-group 或 shared-operator-group")
		return
	}
	if iam.IsPrivilegedGroup(group) && !req.ConfirmOperator {
		message := "创建操作员需要二次确认"
		if group == iam.SharedAdminGroup {
			message = "创建共享操作员需要二次确认"
		}
		response.Err(c, http.StatusBadRequest, "operator_confirm_required", message)
		return
	}
	exists, err := h.IAM.UserExists(c.Request.Context(), username)
	if err != nil {
		logging.Error("admin add user UserExists", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 管理服务不可用")
		return
	}
	if exists {
		response.Err(c, http.StatusConflict, "already_exists", "用户已存在")
		return
	}
	password, err := auth.GeneratePassword(userPasswordLen)
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "internal_error", "生成密码失败")
		return
	}
	created, err := h.IAM.SetupUser(c.Request.Context(), username, password, group)
	if err != nil {
		if created {
			_, _, _ = h.IAM.DeleteUser(c.Request.Context(), username)
		}
		logging.Error("admin add user SetupUser", "username", username, "group", group, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "创建用户失败")
		return
	}
	if !created {
		response.Err(c, http.StatusConflict, "already_exists", "用户已存在")
		return
	}
	var role, action string
	switch group {
	case iam.AdminGroup:
		role, action = "操作员", "admin.user.add.operator"
	case iam.SharedAdminGroup:
		role, action = "共享操作员", "admin.user.add.shared_operator"
	}
	if action != "" {
		logging.Warn("已创建"+role+"用户", "username", username, "group", group)
		logging.Audit(logging.AuditEvent{
			Action:    action,
			Actor:     c.GetString("admin"),
			ActorType: "admin",
			Target:    username,
			Result:    "success",
			RequestID: c.GetString("request_id"),
			RemoteIP:  c.ClientIP(),
		})
		c.Set("audit_logged", true)
	}
	response.JSON(c, http.StatusCreated, protocol.AddUserResponse{Username: username, Password: password})
}

// AdminResetPassword 为已存在的用户生成并设置新的 8 位密码。
func (h *Handler) AdminResetPassword(c *gin.Context) {
	username := c.Param("username")
	c.Set("audit_target", username)
	if iam.IsSystemUser(username) {
		response.Err(c, http.StatusForbidden, "system_user", "系统用户不能重置密码")
		return
	}
	if err := auth.ValidateUsername(username); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	exists, err := h.IAM.UserExists(c.Request.Context(), username)
	if err != nil {
		logging.Error("admin reset password UserExists", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 管理服务不可用")
		return
	}
	if !exists {
		response.Err(c, http.StatusNotFound, "user_not_found", "用户不存在")
		return
	}
	password, err := auth.GeneratePassword(userPasswordLen)
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "internal_error", "生成密码失败")
		return
	}
	if err := h.IAM.ResetUserPassword(c.Request.Context(), username, password); err != nil {
		logging.Error("admin reset password", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "重置密码失败")
		return
	}
	response.JSON(c, http.StatusOK, protocol.AddUserResponse{Username: username, Password: password})
}

// AdminListUsers 返回 RustFS 中的用户列表。
func (h *Handler) AdminListUsers(c *gin.Context) {
	users, err := h.IAM.ListUsers(c.Request.Context())
	if err != nil {
		logging.Error("admin list users", "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 管理服务不可用")
		return
	}
	names := make([]string, 0, len(users))
	for name := range users {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]protocol.AdminUserInfo, 0, len(names))
	for _, name := range names {
		u := users[name]
		out = append(out, protocol.AdminUserInfo{
			Username:   name,
			System:     iam.IsSystemUser(name),
			Status:     string(u.Status),
			PolicyName: u.PolicyName,
			Groups:     u.MemberOf,
		})
	}
	response.JSON(c, http.StatusOK, protocol.ListUsersResponse{Users: out})
}

// AdminDelete 删除用户及其全部数据。
func (h *Handler) AdminDelete(c *gin.Context) {
	username := c.Param("username")
	c.Set("audit_target", username)
	if iam.IsSystemUser(username) {
		response.Err(c, http.StatusForbidden, "system_user", "系统用户不能删除")
		return
	}
	if err := auth.ValidateUsername(username); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	deleted, n, err := h.IAM.DeleteUser(c.Request.Context(), username)
	if err != nil {
		logging.Error("admin delete", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "删除用户失败")
		return
	}
	if !deleted {
		response.Err(c, http.StatusNotFound, "user_not_found", "用户不存在")
		return
	}
	response.JSON(c, http.StatusOK, map[string]any{"deleted": true, "objects": n})
}

// AdminListPolicySids 返回策略中可设置 IP 白名单的 Allow 语句 Sid。
func (h *Handler) AdminListPolicySids(c *gin.Context) {
	policyName := c.Param("name")
	sids, err := h.IAM.PolicySids(policyName)
	if err != nil {
		logging.Error("admin list policy sids", "policy", policyName, "err", err)
		writePolicyError(c, err)
		return
	}
	response.JSON(c, http.StatusOK, protocol.ListPolicySidsResponse{Policy: policyName, Sids: sids})
}

// AdminGetStatementIPRanges 返回指定策略语句当前的 IP 白名单。
func (h *Handler) AdminGetStatementIPRanges(c *gin.Context) {
	policyName := c.Param("name")
	sid := c.Param("sid")
	c.Set("audit_target", policyName+"/"+sid)
	cidrs, err := h.IAM.StatementIPRanges(policyName, sid)
	if err != nil {
		logging.Error("admin get statement ip ranges", "policy", policyName, "sid", sid, "err", err)
		writePolicyError(c, err)
		return
	}
	response.JSON(c, http.StatusOK, protocol.SetStatementIPRangesResponse{
		Policy: policyName,
		Sid:    sid,
		Cidrs:  cidrs,
	})
}

// AdminSetStatementIPRanges 设置或清除指定策略语句的 IP 白名单。
func (h *Handler) AdminSetStatementIPRanges(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req protocol.SetStatementIPRangesRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Cidrs == nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "请求参数错误")
		return
	}
	policyName := c.Param("name")
	sid := c.Param("sid")
	c.Set("audit_target", policyName+"/"+sid)
	opts := iam.PolicyOptions{
		HomeBucket:   h.Cfg.S3.HomeBucket,
		SharedBucket: h.Cfg.S3.SharedBucket,
		SharedDirs:   h.Cfg.S3.SharedDirs,
	}
	cidrs, err := h.IAM.SetStatementIPRanges(c.Request.Context(), policyName, sid, *req.Cidrs, strings.TrimSpace(req.Mode), opts)
	if err != nil {
		logging.Error("admin set statement ip ranges", "policy", policyName, "sid", sid, "err", err)
		writePolicyError(c, err)
		return
	}
	logging.Audit(logging.AuditEvent{
		Action:    "admin.policy.ip.set",
		Actor:     c.GetString("admin"),
		ActorType: "admin",
		Target:    policyName + "/" + sid,
		Result:    "success",
		RequestID: c.GetString("request_id"),
		RemoteIP:  c.ClientIP(),
	})
	c.Set("audit_logged", true)
	logging.Info("admin policy ip set",
		"policy", policyName,
		"sid", sid,
		"cidrs", cidrs,
		"actor", c.GetString("admin"),
		"request_id", c.GetString("request_id"))
	response.JSON(c, http.StatusOK, protocol.SetStatementIPRangesResponse{
		Policy: policyName,
		Sid:    sid,
		Cidrs:  cidrs,
	})
}

func writePolicyError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, iam.ErrInvalidCIDR):
		response.Err(c, http.StatusBadRequest, "invalid_cidr", err.Error())
	case errors.Is(err, iam.ErrInvalidMode):
		response.Err(c, http.StatusBadRequest, "invalid_mode", "mode 只允许 set、add 或 remove")
	case errors.Is(err, iam.ErrServicePolicy):
		response.Err(c, http.StatusForbidden, "service_policy", err.Error())
	case errors.Is(err, iam.ErrUnsupportedPolicy):
		response.Err(c, http.StatusBadRequest, "unsupported_policy", "只允许 ft-user-policy、ft-operator-policy、shared-operator-policy")
	case errors.Is(err, iam.ErrSIDNotFound):
		response.Err(c, http.StatusBadRequest, "sid_not_found", "策略中不存在指定 Sid，请确认 policy/*.json 包含该 Sid，并执行 ft-admin init 更新策略")
	case errors.Is(err, iam.ErrStatementNotAllow):
		response.Err(c, http.StatusBadRequest, "statement_not_allow", "只能给 Allow 语句设置 IP 白名单")
	case errors.Is(err, iam.ErrInvalidPolicy):
		response.Err(c, http.StatusInternalServerError, "invalid_policy", "策略内容无效")
	case errors.Is(err, iam.ErrPolicyRead):
		response.Err(c, http.StatusInternalServerError, "policy_read_failed", "读取策略文件失败，请检查 policy 目录和配置")
	case errors.Is(err, iam.ErrPolicyWrite):
		response.Err(c, http.StatusInternalServerError, "policy_write_failed", "写入策略文件失败，请检查 policy 目录权限")
	case errors.Is(err, iam.ErrApplyFailed):
		response.Err(c, http.StatusBadGateway, "apply_failed", "规则已写入模板但下发 RustFS 失败，请重试或执行 ft-admin init")
	default:
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 管理服务不可用")
	}
}
