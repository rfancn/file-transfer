package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/s3"
	"github.com/rfancn/file-transfer/internal/server/response"
)

func (h *Handler) MultipartInit(c *gin.Context) {
	username := currentUser(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxJSONBody)
	var req struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "请求参数错误")
		return
	}
	rel, err := validateUploadPath(req.Path)
	if err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if req.Size < 0 {
		response.Err(c, http.StatusBadRequest, "invalid_request", "size 参数无效")
		return
	}
	partSize, err := h.RT.PartSize(req.Size)
	if err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if partSize == 0 {
		response.Err(c, http.StatusBadRequest, "invalid_request", "小文件请直接 PUT /v1/files/{path}")
		return
	}
	key := username + "/" + rel
	s3ID, err := h.Data.NewMultipart(c.Request.Context(), key)
	if err != nil {
		logging.Error("multipart init", "key", key, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "初始化分片失败")
		return
	}
	now := time.Now()
	claims := auth.MultipartClaims{
		Sub: username, Path: key, PartSize: partSize, S3UploadID: s3ID,
		Iat: now.Unix(), Exp: now.Add(h.RT.MultipartTTL).Unix(), Kind: "multipart",
	}
	uploadID, err := auth.Sign(h.Cfg.Auth.UploadSecret, claims)
	if err != nil {
		response.Err(c, http.StatusInternalServerError, "internal_error", "签分片 ID 失败")
		return
	}
	response.JSON(c, http.StatusCreated, protocol.InitResponse{UploadID: uploadID, PartSize: partSize})
}

// verifyUploadID 校验路径中的 upload_id，并返回其中的对象 key。
func (h *Handler) verifyUploadID(c *gin.Context, username string) (*auth.MultipartClaims, string, bool) {
	raw := c.Param("upload_id")
	var claims auth.MultipartClaims
	if err := auth.Verify(h.Cfg.Auth.UploadSecret, raw, &claims); err != nil {
		response.Err(c, http.StatusUnauthorized, "token_invalid", "upload_id 无效")
		return nil, "", false
	}
	if claims.Kind != "multipart" || auth.Expired(claims.Exp) || claims.Sub != username {
		response.Err(c, http.StatusUnauthorized, "token_expired", "upload_id 已过期或与用户不匹配")
		return nil, "", false
	}
	rel, ok := strings.CutPrefix(claims.Path, username+"/")
	if !ok {
		response.Err(c, http.StatusUnauthorized, "token_invalid", "upload_id 与路径不匹配")
		return nil, "", false
	}
	if _, err := validateUploadPath(rel); err != nil {
		response.Err(c, http.StatusUnauthorized, "token_invalid", "upload_id 与路径不匹配")
		return nil, "", false
	}
	return &claims, rel, true
}

func (h *Handler) MultipartPart(c *gin.Context) {
	username := currentUser(c)
	claims, rel, ok := h.verifyUploadID(c, username)
	if !ok {
		return
	}
	partNum, err := strconv.Atoi(c.Param("part"))
	if err != nil || partNum < 1 || partNum > 10000 {
		response.Err(c, http.StatusBadRequest, "invalid_request", "part_number 无效")
		return
	}
	if c.Request.ContentLength <= 0 || c.Request.ContentLength > claims.PartSize {
		response.Err(c, http.StatusBadRequest, "invalid_request", fmt.Sprintf("分片大小必须在 1-%d 字节之间", claims.PartSize))
		return
	}
	etag, err := h.Data.PutPart(c.Request.Context(), claims.Path, claims.S3UploadID, partNum, c.Request.Body, c.Request.ContentLength)
	if err != nil {
		if s3.IsNotFound(err) {
			response.Err(c, http.StatusNotFound, "upload_not_found", "分片上传不存在或已过期，请重新上传")
			return
		}
		logging.Error("multipart part", "path", rel, "part", partNum, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "上传分片失败")
		return
	}
	response.JSON(c, http.StatusOK, protocol.PartInfo{Number: partNum, Size: c.Request.ContentLength, ETag: etag})
}

func (h *Handler) MultipartParts(c *gin.Context) {
	username := currentUser(c)
	claims, rel, ok := h.verifyUploadID(c, username)
	if !ok {
		return
	}
	parts, err := h.Data.ListParts(c.Request.Context(), claims.Path, claims.S3UploadID)
	if err != nil {
		if s3.IsNotFound(err) {
			response.Err(c, http.StatusNotFound, "upload_not_found", "分片上传不存在或已过期，请重新上传")
			return
		}
		logging.Error("multipart parts", "path", rel, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "查询分片失败")
		return
	}
	response.JSON(c, http.StatusOK, protocol.PartsResponse{Parts: parts})
}

func (h *Handler) MultipartComplete(c *gin.Context) {
	username := currentUser(c)
	claims, rel, ok := h.verifyUploadID(c, username)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var req protocol.CompleteRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_request", "complete 请求体无效")
		return
	}
	if len(req.Parts) == 0 {
		response.Err(c, http.StatusBadRequest, "invalid_request", "parts 不能为空")
		return
	}
	sort.Slice(req.Parts, func(i, j int) bool { return req.Parts[i].Number < req.Parts[j].Number })
	if err := h.Data.CompleteMultipart(c.Request.Context(), claims.Path, claims.S3UploadID, req.Parts); err != nil {
		if s3.IsNotFound(err) {
			response.Err(c, http.StatusNotFound, "upload_not_found", "分片上传不存在或已过期，请重新上传")
			return
		}
		logging.Error("multipart complete", "path", rel, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "完成分片上传失败")
		return
	}
	var total int64
	for _, p := range req.Parts {
		total += p.Size
	}
	response.JSON(c, http.StatusOK, map[string]any{"ok": true, "path": rel, "size": total})
}

func (h *Handler) MultipartAbort(c *gin.Context) {
	username := currentUser(c)
	claims, rel, ok := h.verifyUploadID(c, username)
	if !ok {
		return
	}
	if err := h.Data.AbortMultipart(c.Request.Context(), claims.Path, claims.S3UploadID); err != nil {
		if !s3.IsNotFound(err) {
			logging.Error("multipart abort", "path", rel, "err", err)
			response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "终止分片失败")
			return
		}
	}
	response.JSON(c, http.StatusOK, map[string]any{"ok": true})
}
