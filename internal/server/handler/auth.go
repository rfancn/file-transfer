package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/s3"
	"github.com/rfancn/file-transfer/internal/server/response"
)

func (h *Handler) Register(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
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
		logging.Error("register UserExists", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 管理服务不可用")
		return
	}
	password := req.Password
	generated := false
	if password == "" {
		password, err = auth.GeneratePassword(userPasswordLen)
		if err != nil {
			response.Err(c, http.StatusInternalServerError, "internal_error", "生成密码失败")
			return
		}
		generated = true
	} else if err := auth.ValidatePassword(password); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	created, err := h.IAM.SetupUser(c.Request.Context(), username, password)
	if err != nil {
		if created {
			_, _, _ = h.IAM.DeleteUser(c.Request.Context(), username)
		}
		logging.Error("register SetupUser", "username", username, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "创建用户失败，请稍后重试")
		return
	}
	if exists || !created {
		response.Err(c, http.StatusConflict, "already_exists", "用户已存在，请联系管理员")
		return
	}
	c.Set("username", username)
	out := protocol.RegisterResponse{Username: username}
	if generated {
		out.Password = password
	}
	response.JSON(c, http.StatusCreated, out)
}

func (h *Handler) Session(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "请求参数错误")
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	if err := auth.ValidateUsername(username); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_username", err.Error())
		return
	}
	if password == "" {
		response.Err(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	if err := h.verifyUser(c.Request.Context(), username, password); err != nil {
		exists, existsErr := h.IAM.UserExists(c.Request.Context(), username)
		if existsErr != nil {
			logging.Error("session UserExists", "username", username, "err", existsErr)
			response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
			return
		}
		if !exists {
			response.Err(c, http.StatusConflict, "not_registered", "用户未注册，请先注册")
			return
		}
		logging.Warn("session 认证失败", "username", username, "err", err)
		response.Err(c, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	missing := ""
	for _, d := range []string{"inbound", "outbound", "upload"} {
		ok, err := h.IAM.DirExists(c.Request.Context(), username, d)
		if err != nil {
			logging.Error("session 检查目录", "username", username, "dir", d, "err", err)
			response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
			return
		}
		if !ok {
			missing = d
			break
		}
	}
	if missing != "" {
		// 已注册用户如果占位对象被删除（例如清理任务误删），自动补齐。
		if h.isRegisteredUser(c.Request.Context(), username) {
			if err := h.IAM.CreatePlaceholders(c.Request.Context(), username); err != nil {
				logging.Error("session 补齐占位", "username", username, "dir", missing, "err", err)
				response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
				return
			}
		} else {
			response.Err(c, http.StatusConflict, "not_registered", "用户未注册，请先运行 ft-client register")
			return
		}
	}
	c.Set("username", username)
	now := time.Now()
	claims := auth.SessionClaims{Sub: username, Iat: now.Unix(), Exp: now.Add(h.RT.TokenTTL).Unix(), Kind: "session"}
	token, err := auth.Sign(h.Cfg.Auth.TokenSecret, claims)
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "internal_error", "签发 token 失败")
		return
	}
	response.JSON(c, http.StatusCreated, protocol.SessionResponse{
		Token:        token,
		ExpiresIn:    int64(h.RT.TokenTTL.Seconds()),
		UploadPolicy: policyFromRuntime(h.RT),
	})
}

// AdminLogin 用本地管理员账号换取管理接口 token。
func (h *Handler) AdminLogin(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "请求参数错误")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" || !h.Admins.Verify(username, req.Password) {
		response.Err(c, http.StatusUnauthorized, "invalid_credentials", "管理员用户名或密码错误")
		return
	}
	c.Set("admin", username)
	now := time.Now()
	token, err := auth.Sign(h.Cfg.Auth.TokenSecret, auth.AdminClaims{
		Sub: username, Kind: "admin", Iat: now.Unix(), Exp: now.Add(h.RT.TokenTTL).Unix(),
	})
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "internal_error", "签发 token 失败")
		return
	}
	response.JSON(c, http.StatusCreated, protocol.AdminLoginResponse{
		Token:     token,
		ExpiresIn: int64(h.RT.TokenTTL.Seconds()),
	})
}

func (h *Handler) isRegisteredUser(ctx context.Context, username string) bool {
	info, err := h.IAM.Admin.GetUserInfo(ctx, username)
	if err != nil {
		return false
	}
	if info.PolicyName == iam.UserPolicyName(username) {
		return true
	}
	for _, g := range info.MemberOf {
		if g == iam.UserGroup {
			return true
		}
	}
	return false
}

func (h *Handler) verifyUser(ctx context.Context, username, password string) error {
	uc, err := s3.New(h.Cfg.S3.Endpoint, username, password, h.Cfg.S3.Region, h.Cfg.S3.Bucket)
	if err != nil {
		return err
	}
	ch := uc.Client.ListObjects(ctx, h.Cfg.S3.Bucket, minio.ListObjectsOptions{
		Prefix: username + "/inbound/", Recursive: true, MaxKeys: 1,
	})
	for obj := range ch {
		if obj.Err != nil {
			return obj.Err
		}
		break
	}
	return nil
}

func policyFromRuntime(rt config.Runtime) protocol.UploadPolicy {
	p := protocol.UploadPolicy{DirectMax: rt.DirectMax, MaxPart: rt.MaxPart}
	for _, t := range rt.Tiers {
		p.Tiers = append(p.Tiers, protocol.Tier{Max: t.Max, Part: t.Part})
	}
	return p
}
