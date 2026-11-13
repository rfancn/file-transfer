package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/s3"
	"github.com/rfancn/file-transfer/internal/server/response"
)

func (h *Handler) List(c *gin.Context) {
	username := currentUser(c)
	dir := c.Query("dir")
	if dir != "inbound" && dir != "upload" && dir != "outbound" {
		response.Err(c, http.StatusBadRequest, "invalid_request", "dir 只允许 inbound、outbound 或 upload")
		return
	}
	files, err := h.Data.List(c.Request.Context(), username+"/"+dir+"/")
	if err != nil {
		logging.Error("list", "username", username, "dir", dir, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	response.JSON(c, http.StatusOK, protocol.ListResponse{Files: files})
}

func (h *Handler) Download(c *gin.Context) {
	username := currentUser(c)
	rel, err := validateDownloadPath(wildcardPath(c))
	if err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	key := username + "/" + rel
	stat, err := h.Data.Stat(c.Request.Context(), key)
	if err != nil {
		if s3.IsNotFound(err) {
			response.Err(c, http.StatusNotFound, "object_not_found", "文件不存在")
			return
		}
		logging.Error("download stat", "key", key, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
		return
	}
	startByte, endByte, partial, err := parseRangeHeader(c.Request, stat.Size, stat.ETag, stat.LastModified)
	if err != nil {
		c.Writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", stat.Size))
		response.Err(c, http.StatusRequestedRangeNotSatisfiable, "invalid_request", "Range 不合法")
		return
	}
	obj, err := h.Data.Get(c.Request.Context(), key, startByte, endByteIfPartial(partial, endByte))
	if err != nil {
		if s3.IsNotFound(err) {
			response.Err(c, http.StatusNotFound, "object_not_found", "文件不存在")
			return
		}
		logging.Error("download get", "key", key, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
		return
	}
	defer obj.Close()
	// 在写响应头前发起首次读取，确保对象仍可访问（缩小 Stat 与 Get 之间的竞态窗口）。
	if _, err := obj.Stat(); err != nil {
		if s3.IsNotFound(err) {
			response.Err(c, http.StatusNotFound, "object_not_found", "文件不存在")
			return
		}
		logging.Error("download stat object", "key", key, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "RustFS 服务不可用")
		return
	}

	w := c.Writer
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", formatETag(stat.ETag))
	w.Header().Set("Last-Modified", stat.LastModified.UTC().Format(http.TimeFormat))
	ctype := stat.ContentType
	if ctype == "" || ctype == "application/x-directory" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startByte, endByte, stat.Size))
		w.Header().Set("Content-Length", strconv.FormatInt(endByte-startByte+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size, 10))
		w.WriteHeader(http.StatusOK)
	}
	_, err = io.Copy(w, obj)
	if err != nil && !errors.Is(err, context.Canceled) {
		logging.Error("download copy", "key", key, "err", err)
	}
}

func (h *Handler) Upload(c *gin.Context) {
	username := currentUser(c)
	rel, err := validateUploadPath(wildcardPath(c))
	if err != nil {
		response.Err(c, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	if c.Request.ContentLength < 0 {
		response.Err(c, http.StatusBadRequest, "invalid_request", "上传需要 Content-Length")
		return
	}
	key := username + "/" + rel
	if err := h.Data.Put(c.Request.Context(), key, c.Request.Body, c.Request.ContentLength); err != nil {
		logging.Error("upload", "key", key, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "上传失败，RustFS 服务不可用")
		return
	}
	response.JSON(c, http.StatusOK, map[string]any{"ok": true, "path": rel, "size": c.Request.ContentLength})
}

func endByteIfPartial(partial bool, end int64) int64 {
	if !partial {
		// 负数表示不限制范围，与 bytes=0-N 区分开。
		return -1
	}
	return end
}

func parseRangeHeader(r *http.Request, size int64, etag string, lastModified time.Time) (int64, int64, bool, error) {
	rangeH := r.Header.Get("Range")
	if rangeH == "" {
		return 0, size - 1, false, nil
	}
	if ifr := r.Header.Get("If-Range"); ifr != "" {
		// If-Range 与当前对象不匹配时忽略 Range，返回完整对象。
		if !ifRangeMatch(ifr, etag, lastModified) {
			return 0, size - 1, false, nil
		}
	}
	if !strings.HasPrefix(rangeH, "bytes=") || strings.Contains(rangeH, ",") {
		return 0, 0, false, errors.New("invalid range")
	}
	spec := strings.TrimPrefix(rangeH, "bytes=")
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, errors.New("invalid range")
	}
	var start, end int64
	var err error
	if parts[0] == "" {
		// bytes=-N 表示最后 N 字节
		n, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || n <= 0 {
			return 0, 0, false, errors.New("invalid range")
		}
		if n > size {
			n = size
		}
		start = size - n
		end = size - 1
	} else {
		start, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, 0, false, errors.New("invalid range")
		}
		if parts[1] == "" {
			end = size - 1
		} else {
			end, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return 0, 0, false, errors.New("invalid range")
			}
		}
	}
	if start < 0 || start >= size || end < start {
		return 0, 0, false, errors.New("range out of bounds")
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true, nil
}

func ifRangeMatch(ifr, etag string, lastModified time.Time) bool {
	if t, err := http.ParseTime(ifr); err == nil {
		return !lastModified.After(t)
	}
	// 弱校验器不能用于 If-Range。
	if strings.HasPrefix(ifr, "W/") {
		return false
	}
	// minio-go 返回的 ETag 不带引号；客户端可能原样回传，比较前统一去掉引号。
	return etag != "" && normalizeETag(ifr) == normalizeETag(etag)
}

func normalizeETag(tag string) string {
	return strings.Trim(strings.TrimSpace(tag), `"`)
}

// formatETag 按 HTTP 语法给裸 ETag 补上引号；minio-go 返回的 ETag 通常不带引号。
func formatETag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" || strings.HasPrefix(tag, `"`) || strings.HasPrefix(tag, "W/") {
		return tag
	}
	return `"` + tag + `"`
}
