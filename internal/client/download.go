package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (c *Client) Download(ctx context.Context, dir, rel, destDir string, force bool, progress ProgressFunc) error {
	if destDir == "" {
		destDir = "."
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	pathWithin := rel
	if strings.HasPrefix(pathWithin, dir+"/") {
		pathWithin = strings.TrimPrefix(pathWithin, dir+"/")
	}
	clean := filepath.Clean(filepath.FromSlash(pathWithin))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("非法的下载路径: %s", rel)
	}
	target := filepath.Join(destDir, clean)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if st, err := os.Stat(target); err == nil && !st.IsDir() && !force {
		return fmt.Errorf("目标文件已存在: %s（使用 --force 覆盖）", target)
	}
	part := target + ".part"
	etagFile := part + ".etag"
	var existing int64
	if st, err := os.Stat(part); err == nil {
		existing = st.Size()
	} else if !os.IsNotExist(err) {
		return err
	}
	if existing > 0 {
		if _, err := os.Stat(etagFile); err != nil {
			_ = os.Remove(part)
			existing = 0
		}
	}
	etag, _ := os.ReadFile(etagFile)
	remote := rel
	if !strings.HasPrefix(remote, dir+"/") {
		remote = dir + "/" + remote
	}
	return c.download(ctx, remote, target, part, etagFile, strings.TrimSpace(string(etag)), existing, force, progress, 0)
}

func (c *Client) download(ctx context.Context, remote, target, part, etagFile, etag string, existing int64, force bool, progress ProgressFunc, attempt int) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/files/"+escapePath(remote), nil, nil, 0, "")
	if err != nil {
		return err
	}
	if existing > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
		if etag != "" {
			req.Header.Set("If-Range", etag)
		}
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		existing = 0
	case http.StatusPartialContent:
		// append
	case http.StatusRequestedRangeNotSatisfiable:
		total := parseContentRangeTotal(resp.Header.Get("Content-Range"))
		if total > 0 && existing == total {
			if force {
				_ = os.Remove(target)
			}
			if err := os.Rename(part, target); err != nil {
				return err
			}
			_ = os.Remove(etagFile)
			return nil
		}
		_ = os.Remove(part)
		_ = os.Remove(etagFile)
		if attempt > 0 {
			return fmt.Errorf("下载范围无效: %s", remote)
		}
		return c.download(ctx, remote, target, part, etagFile, "", 0, force, progress, attempt+1)
	default:
		return parseError(resp)
	}
	if newETag := resp.Header.Get("ETag"); newETag != "" {
		_ = os.WriteFile(etagFile, []byte(newETag), 0o600)
	}
	total := int64(-1)
	if resp.StatusCode == http.StatusPartialContent {
		total = parseContentRangeTotal(resp.Header.Get("Content-Range"))
	} else if resp.ContentLength >= 0 {
		total = existing + resp.ContentLength
	}
	flags := os.O_CREATE | os.O_WRONLY
	if existing > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	var r io.Reader = resp.Body
	if progress != nil {
		r = &progressReader{r: resp.Body, base: existing, total: total, fn: progress}
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if total >= 0 {
		st, err := os.Stat(part)
		if err != nil {
			return err
		}
		if st.Size() != total {
			return fmt.Errorf("下载不完整: %d/%d，可重新运行续传", st.Size(), total)
		}
	}
	if force {
		_ = os.Remove(target)
	}
	if err := os.Rename(part, target); err != nil {
		return err
	}
	_ = os.Remove(etagFile)
	_ = n
	return nil
}

func parseContentRangeTotal(v string) int64 {
	if v == "" {
		return -1
	}
	if i := strings.LastIndex(v, "/"); i >= 0 {
		s := v[i+1:]
		if s == "*" {
			return -1
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return n
		}
	}
	return -1
}
