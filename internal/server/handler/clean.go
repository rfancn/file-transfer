package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/minio/minio-go/v7"
	"github.com/rfancn/file-transfer/internal/logging"
	"github.com/rfancn/file-transfer/internal/server/response"
)

type cleanResponse struct {
	Count  int      `json:"count"`
	Items  []string `json:"items"`
	DryRun bool     `json:"dry_run"`
}

func (h *Handler) AdminClean(c *gin.Context) {
	dir := c.Param("dir")
	switch dir {
	case "inbound", "outbound", "upload", "incomplete":
	default:
		response.Err(c, http.StatusBadRequest, "invalid_request", "dir 只允许 inbound、outbound、upload 或 incomplete")
		return
	}
	days := 0
	if raw := c.Query("days"); raw != "" {
		d, err := strconv.Atoi(raw)
		if err != nil || d <= 0 {
			response.Err(c, http.StatusBadRequest, "invalid_request", "days 必须大于 0")
			return
		}
		days = d
	} else {
		days = h.retentionDays(dir)
		if days == 0 {
			response.Err(c, http.StatusBadRequest, "invalid_request", "该目录未配置保留天数，请指定 days")
			return
		}
	}
	dryRun, _ := strconv.ParseBool(c.Query("dry_run"))
	items, err := h.clean(c.Request.Context(), dir, days, dryRun)
	if err != nil {
		logging.Error("admin clean", "dir", dir, "err", err)
		response.Err(c, http.StatusBadGateway, "rustfs_unavailable", "清理失败")
		return
	}
	response.JSON(c, http.StatusOK, cleanResponse{Count: len(items), Items: items, DryRun: dryRun})
}

func (h *Handler) retentionDays(dir string) int {
	switch dir {
	case "inbound":
		return h.Cfg.Retention.InboundDays
	case "outbound":
		return h.Cfg.Retention.OutboundDays
	case "upload":
		return h.Cfg.Retention.UploadDays
	case "incomplete":
		return h.Cfg.Retention.IncompleteDays
	}
	return 0
}

// clean 用 ft-admin 服务身份清理指定目录或未完成分片。
func (h *Handler) clean(ctx context.Context, dir string, days int, dryRun bool) ([]string, error) {
	items := []string{}
	cutoff := time.Now().AddDate(0, 0, -days)
	if dir == "incomplete" {
		for info := range h.AdminS3.IncompleteUploads(ctx) {
			if info.Err != nil {
				return nil, info.Err
			}
			if info.Initiated.After(cutoff) {
				continue
			}
			items = append(items, info.Key)
			if !dryRun {
				if err := h.AdminS3.AbortMultipart(ctx, info.Key, info.UploadID); err != nil {
					return nil, err
				}
			}
		}
		return items, nil
	}

	cutoffDate := cutoff.Format("20060102")
	for obj := range h.AdminS3.Client.ListObjects(ctx, h.Cfg.S3.Bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		if strings.HasSuffix(obj.Key, "/") || strings.HasSuffix(obj.Key, "/.keep") {
			continue
		}
		parts := strings.SplitN(obj.Key, "/", 3)
		if len(parts) < 3 || parts[1] != dir {
			continue
		}
		remove := false
		if dir == "outbound" {
			date := parts[2]
			remove = len(date) >= 8 && date[:8] < cutoffDate
		} else {
			remove = obj.LastModified.Before(cutoff)
		}
		if !remove {
			continue
		}
		items = append(items, obj.Key)
		if !dryRun {
			if err := h.AdminS3.Remove(ctx, obj.Key); err != nil {
				return nil, err
			}
		}
	}
	return items, nil
}
