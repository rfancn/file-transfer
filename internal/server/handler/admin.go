package handler

import (
	"context"
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
	probe, err := iam.New(h.Cfg.S3.Endpoint, req.AccessKey, req.SecretKey, h.Cfg.S3.Region, h.Cfg.S3.Bucket)
	if err != nil {
		logging.Error("admin init 创建管理客户端失败", "err", err)
		response.Err(c, http.StatusInternalServerError, "internal_error", "创建 RustFS 管理客户端失败")
		return
	}
	if _, err := probe.UserExists(c.Request.Context(), iam.ConsoleAdmin); err != nil {
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

	res, err := bootstrap.Run(c.Request.Context(), h.Cfg, req.AccessKey, req.SecretKey)
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
	if res.ConsoleCreated {
		logging.Info("文件摆渡系统操作员已创建", "username", iam.ConsoleAdmin, "password", res.ConsolePassword)
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
	var req struct {
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "请求参数错误")
		return
	}
	username := strings.TrimSpace(req.Username)
	if err := auth.ValidateUsername(username); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_username", err.Error())
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
	created, err := h.IAM.SetupUser(c.Request.Context(), username, password)
	if err != nil {
		if created {
			_, _, _ = h.IAM.DeleteUser(c.Request.Context(), username)
		}
		logging.Error("admin add user SetupUser", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "创建用户失败")
		return
	}
	if !created {
		response.Err(c, http.StatusConflict, "already_exists", "用户已存在")
		return
	}
	response.JSON(c, http.StatusCreated, protocol.AddUserResponse{Username: username, Password: password})
}

// AdminResetPassword 为已存在的用户生成并设置新的 8 位密码。
func (h *Handler) AdminResetPassword(c *gin.Context) {
	username := c.Param("username")
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
