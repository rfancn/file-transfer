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

// localRelPath 计算远端相对路径 rel（含或不含 dir/ 前缀）相对下载目录的路径。
func localRelPath(dir, rel string) (string, error) {
	pathWithin := rel
	if strings.HasPrefix(pathWithin, dir+"/") {
		pathWithin = strings.TrimPrefix(pathWithin, dir+"/")
	}
	clean := filepath.Clean(filepath.FromSlash(pathWithin))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("非法的下载路径: %s", rel)
	}
	return clean, nil
}

// LocalPath 计算远端相对路径 rel（含或不含 dir/ 前缀）在 destDir 下的本地保存位置。
func LocalPath(dir, rel, destDir string) (string, error) {
	clean, err := localRelPath(dir, rel)
	if err != nil {
		return "", err
	}
	if destDir == "" {
		destDir = "."
	}
	return filepath.Join(destDir, clean), nil
}

// downloadPaths 保存下载目标及其临时文件的相对路径和用于提示的显示路径。
type downloadPaths struct {
	display string
	target  string
	part    string
	etag    string
}

func (c *Client) Download(ctx context.Context, dir, rel, destDir string, force bool, progress ProgressFunc) error {
	if destDir == "" {
		destDir = "."
	}
	// 下载目录只允许当前用户访问，避免续传临时文件被其他用户预置或读取。
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	relPath, err := localRelPath(dir, rel)
	if err != nil {
		return err
	}
	// os.Root 把后续所有文件操作限制在 destDir 内，符号链接不能逃出该目录树。
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("打开下载目录 %s: %w", destDir, err)
	}
	defer root.Close()
	if parent := filepath.Dir(relPath); parent != "." {
		if err := root.MkdirAll(parent, 0o700); err != nil {
			return err
		}
	}
	p := downloadPaths{
		display: filepath.Join(destDir, relPath),
		target:  relPath,
		part:    relPath + ".part",
		etag:    relPath + ".part.etag",
	}
	// 目标位置只接受普通文件；符号链接、目录和设备文件一律拒绝覆盖。
	if fi, err := root.Lstat(p.target); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("目标路径不是普通文件，拒绝覆盖: %s", p.display)
		}
		if !force {
			return fmt.Errorf("目标文件已存在: %s（使用 --force 覆盖）", p.display)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if fi, err := root.Lstat(p.etag); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("ETag 临时文件不是普通文件，拒绝使用: %s", filepath.Join(destDir, p.etag))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	existing, err := regularPartSize(root, p.part)
	if err != nil {
		return err
	}
	if existing > 0 {
		if _, err := root.Lstat(p.etag); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			if err := root.Remove(p.part); err != nil && !os.IsNotExist(err) {
				return err
			}
			existing = 0
		}
	}
	etag := ""
	if existing > 0 {
		if data, err := root.ReadFile(p.etag); err == nil {
			etag = strings.TrimSpace(string(data))
		}
	}
	remote := rel
	if !strings.HasPrefix(remote, dir+"/") {
		remote = dir + "/" + remote
	}
	return c.download(ctx, root, remote, p, etag, existing, progress, 0)
}

// regularPartSize 校验续传临时文件：必须是当前用户拥有的普通文件。
func regularPartSize(root *os.Root, name string) (int64, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("临时文件不是普通文件，拒绝使用: %s", name)
	}
	if uid, ok := fileOwnerUID(fi); ok && uid != uint32(os.Geteuid()) {
		return 0, fmt.Errorf("临时文件属主不是当前用户，拒绝使用: %s", name)
	}
	return fi.Size(), nil
}

func (c *Client) download(ctx context.Context, root *os.Root, remote string, p downloadPaths, etag string, existing int64, progress ProgressFunc, attempt int) error {
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
			// rename 原子替换目标，无需先删除，避免检查与删除之间的竞态。
			if err := root.Rename(p.part, p.target); err != nil {
				return err
			}
			_ = root.Remove(p.etag)
			return nil
		}
		_ = root.Remove(p.part)
		_ = root.Remove(p.etag)
		if attempt > 0 {
			return fmt.Errorf("下载范围无效: %s", remote)
		}
		return c.download(ctx, root, remote, p, "", 0, progress, attempt+1)
	default:
		return parseError(resp)
	}
	if newETag := resp.Header.Get("ETag"); newETag != "" {
		if err := writeRootFile(root, p.etag, []byte(newETag), 0o600); err != nil {
			return err
		}
	}
	total := int64(-1)
	if resp.StatusCode == http.StatusPartialContent {
		total = parseContentRangeTotal(resp.Header.Get("Content-Range"))
	} else if resp.ContentLength >= 0 {
		total = existing + resp.ContentLength
	}
	flags := os.O_CREATE | os.O_WRONLY | openNoFollow
	if existing > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := root.OpenFile(p.part, flags, 0o600)
	if err != nil {
		return err
	}
	var r io.Reader = resp.Body
	if progress != nil {
		r = &progressReader{r: resp.Body, base: existing, total: total, fn: progress}
	}
	n, copyErr := io.Copy(f, r)
	// 在关闭前通过文件句柄取大小，避免校验时路径被替换。
	var partSize int64
	if copyErr == nil {
		if st, err := f.Stat(); err == nil {
			partSize = st.Size()
		} else {
			copyErr = err
		}
	}
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if total >= 0 && partSize != total {
		return fmt.Errorf("下载不完整: %d/%d，可重新运行续传", partSize, total)
	}
	if err := root.Rename(p.part, p.target); err != nil {
		return err
	}
	_ = root.Remove(p.etag)
	_ = n
	return nil
}

// writeRootFile 在 os.Root 范围内覆盖写入文件，并拒绝最后一级符号链接。
func writeRootFile(root *os.Root, name string, data []byte, perm os.FileMode) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|openNoFollow, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
