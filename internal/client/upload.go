package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rfancn/file-transfer/internal/protocol"
)

type ProgressFunc func(done, total int64)

type UploadFile struct {
	Local  string
	Remote string
	Size   int64
}

type UploadPlan struct {
	Root  string
	IsDir bool
	Files []UploadFile
	Total int64
}

type UploadResult struct {
	Uploaded int
	Skipped  int
	Bytes    int64
	Failed   []string
}

func PlanUpload(localPath, dest string) (UploadPlan, error) {
	return PlanUploadTo(localPath, dest, "outbound/"+time.Now().Format("20060102"))
}

// PlanUploadTo 使用指定远端前缀生成上传计划，例如 outbound/2026111301。
func PlanUploadTo(localPath, dest, outboundPrefix string) (UploadPlan, error) {
	prefix := strings.TrimRight(outboundPrefix, "/")
	st, err := os.Stat(localPath)
	if err != nil {
		return UploadPlan{}, err
	}
	var plan UploadPlan
	if !st.IsDir() {
		name := dest
		if name == "" {
			name = filepath.Base(localPath)
		}
		name = filepath.ToSlash(name)
		plan.Files = append(plan.Files, UploadFile{Local: localPath, Remote: prefix + "/" + name, Size: st.Size()})
		plan.Total = st.Size()
		return plan, nil
	}
	rootName := dest
	if rootName == "" {
		rootName = filepath.Base(localPath)
	}
	rootName = filepath.ToSlash(rootName)
	plan.IsDir = true
	plan.Root = rootName
	err = filepath.WalkDir(localPath, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(localPath, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		plan.Files = append(plan.Files, UploadFile{
			Local:  p,
			Remote: prefix + "/" + rootName + "/" + rel,
			Size:   info.Size(),
		})
		plan.Total += info.Size()
		return nil
	})
	if err != nil {
		return UploadPlan{}, err
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Remote < plan.Files[j].Remote })
	return plan, nil
}

func (c *Client) Upload(ctx context.Context, plan UploadPlan, progress ProgressFunc, force bool) UploadResult {
	var res UploadResult
	remoteSizes := map[string]int64{}
	if !force {
		if files, err := c.List(ctx, "outbound"); err == nil {
			for _, f := range files {
				remoteSizes[f.Path] = f.Size
			}
		}
	}
	for _, f := range plan.Files {
		if !force {
			if size, ok := remoteSizes[strings.TrimPrefix(f.Remote, "outbound/")]; ok && size == f.Size {
				res.Skipped++
				continue
			}
		}
		if err := c.uploadFile(ctx, f, progress, force); err != nil {
			res.Failed = append(res.Failed, f.Remote+": "+err.Error())
			continue
		}
		res.Uploaded++
		res.Bytes += f.Size
	}
	return res
}

type uploadState struct {
	Path     string              `json:"path"`
	Size     int64               `json:"size"`
	ModTime  int64               `json:"mtime"`
	PartSize int64               `json:"part_size"`
	UploadID string              `json:"upload_id"`
	Parts    []protocol.PartInfo `json:"parts"`
}

// InterruptedUpload 描述一个仍保有续传状态的文件。
type InterruptedUpload struct {
	Local         string
	Remote        string
	Size          int64
	PartSize      int64
	Parts         int
	UploadedBytes int64
	UploadID      string
}

// Interrupted 返回上传计划中仍有中断续传记录的文件。
func (c *Client) Interrupted(plan UploadPlan) []InterruptedUpload {
	var items []InterruptedUpload
	for _, f := range plan.Files {
		info, err := os.Stat(f.Local)
		if err != nil {
			continue
		}
		sp, err := statePath(c.username, f.Local)
		if err != nil {
			continue
		}
		st, err := loadState(sp)
		if err != nil {
			continue
		}
		if st.Path != f.Local || st.Size != f.Size || st.ModTime != info.ModTime().Unix() ||
			st.PartSize < 5<<20 || st.PartSize > 5<<30 {
			continue
		}
		var uploaded int64
		for _, p := range st.Parts {
			uploaded += p.Size
		}
		items = append(items, InterruptedUpload{
			Local:         f.Local,
			Remote:        f.Remote,
			Size:          f.Size,
			PartSize:      st.PartSize,
			Parts:         len(st.Parts),
			UploadedBytes: uploaded,
			UploadID:      st.UploadID,
		})
	}
	return items
}

// DiscardInterrupted 终止服务端旧的分片上传并删除本地续传状态。
// 终止失败不影响重传，残留分片会由服务端的未完成分片清理任务回收。
func (c *Client) DiscardInterrupted(ctx context.Context, items []InterruptedUpload) {
	for _, item := range items {
		if item.UploadID != "" {
			_ = c.doJSON(ctx, "DELETE", "/v1/uploads/"+url.PathEscape(item.UploadID), nil, nil, 0, "", nil)
		}
		if sp, err := statePath(c.username, item.Local); err == nil {
			_ = os.Remove(sp)
		}
	}
}

func statePath(username, abs string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "file-transfer")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, username+"-"+hex.EncodeToString(sum[:])+".json"), nil
}

func loadState(path string) (*uploadState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st uploadState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func saveState(path string, st *uploadState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Client) partSize(size int64) int64 {
	if size < c.policy.DirectMax {
		return 0
	}
	part := c.policy.MaxPart
	for _, t := range c.policy.Tiers {
		if size < t.Max {
			part = t.Part
			break
		}
	}
	if part < 5<<20 {
		part = 5 << 20
	}
	if part > 5<<30 {
		part = 5 << 30
	}
	if need := (size + 9999) / 10000; need > part {
		part = ((need + (1 << 20) - 1) / (1 << 20)) << 20
	}
	return part
}

func (c *Client) uploadFile(ctx context.Context, f UploadFile, progress ProgressFunc, force bool) error {
	st, err := os.Stat(f.Local)
	if err != nil {
		return err
	}
	part := c.partSize(f.Size)
	if part == 0 {
		return c.putDirect(ctx, f, progress)
	}
	if err := c.putMultipart(ctx, f, st.ModTime().Unix(), part, progress, force); err != nil {
		if ae, ok := AsAPIError(err); ok && (ae.Code == "token_expired" || ae.Code == "upload_not_found") {
			// upload_id 已过期或服务端分片已清理，丢弃本地状态后重新初始化。
			sp, _ := statePath(c.username, f.Local)
			_ = os.Remove(sp)
			return c.putMultipart(ctx, f, st.ModTime().Unix(), part, progress, true)
		}
		return err
	}
	return nil
}

func (c *Client) putDirect(ctx context.Context, f UploadFile, progress ProgressFunc) error {
	file, err := os.Open(f.Local)
	if err != nil {
		return err
	}
	defer file.Close()
	body := io.Reader(file)
	getBody := func() (io.ReadCloser, error) { return os.Open(f.Local) }
	if f.Size == 0 {
		// 非空 io.Reader + ContentLength 0 会让 Transport 改用 chunked，服务端会拒绝。
		body = http.NoBody
		getBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
	} else if progress != nil {
		body = &progressReader{r: file, total: f.Size, fn: progress}
	}
	req, err := c.newRequest(ctx, "PUT", "/v1/files/"+escapePath(f.Remote), nil, body, f.Size, "application/octet-stream")
	if err != nil {
		return err
	}
	req.GetBody = getBody
	resp, err := c.execute(req, req.GetBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return parseError(resp)
	}
	return nil
}

func (c *Client) putMultipart(ctx context.Context, f UploadFile, mtime, partSize int64, progress ProgressFunc, force bool) error {
	sp, err := statePath(c.username, f.Local)
	if err != nil {
		return err
	}
	var st *uploadState
	if !force {
		if old, err := loadState(sp); err == nil && old.Path == f.Local && old.Size == f.Size && old.ModTime == mtime && old.PartSize >= 5<<20 && old.PartSize <= 5<<30 {
			st = old
		}
	}
	if st == nil {
		var initResp protocol.InitResponse
		initBody := jsonBody(map[string]any{"path": f.Remote, "size": f.Size})
		if err := c.doJSON(ctx, "POST", "/v1/uploads", nil, initBody, -1, "application/json", &initResp); err != nil {
			return err
		}
		st = &uploadState{Path: f.Local, Size: f.Size, ModTime: mtime, PartSize: initResp.PartSize, UploadID: initResp.UploadID}
		if err := saveState(sp, st); err != nil {
			return err
		}
	}
	var listed protocol.PartsResponse
	uploadPath := "/v1/uploads/" + url.PathEscape(st.UploadID)
	if err := c.doJSON(ctx, "GET", uploadPath+"/parts", nil, nil, 0, "", &listed); err != nil {
		return err
	}
	have := map[int]protocol.PartInfo{}
	for _, p := range listed.Parts {
		have[p.Number] = p
	}
	for _, p := range st.Parts {
		if _, ok := have[p.Number]; !ok && p.ETag != "" {
			have[p.Number] = p
		}
	}
	total := f.Size
	partCount := int((total + st.PartSize - 1) / st.PartSize)
	done := int64(0)
	for i := 1; i <= partCount; i++ {
		expected := st.PartSize
		if i == partCount {
			expected = total - int64(i-1)*st.PartSize
		}
		if p, ok := have[i]; ok && p.Size == expected && p.ETag != "" {
			done += expected
			if progress != nil {
				progress(done, total)
			}
			continue
		}
		file, err := os.Open(f.Local)
		if err != nil {
			return err
		}
		sec := io.NewSectionReader(file, int64(i-1)*st.PartSize, expected)
		body := io.Reader(sec)
		if progress != nil {
			body = &progressReader{r: sec, base: done, total: total, fn: progress}
		}
		req, err := c.newRequest(ctx, "PUT", uploadPath+"/parts/"+strconv.Itoa(i), nil, body, expected, "application/octet-stream")
		if err != nil {
			file.Close()
			return err
		}
		offset := int64(i-1) * st.PartSize
		req.GetBody = func() (io.ReadCloser, error) {
			fh, err := os.Open(f.Local)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(io.NewSectionReader(fh, offset, expected)), nil
		}
		resp, err := c.execute(req, req.GetBody)
		if err != nil {
			file.Close()
			return err
		}
		var p protocol.PartInfo
		if resp.StatusCode/100 != 2 {
			err := parseError(resp)
			resp.Body.Close()
			file.Close()
			return err
		}
		err = json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		file.Close()
		if err != nil {
			return err
		}
		have[i] = p
		st.Parts = mergeParts(st.Parts, p)
		if err := saveState(sp, st); err != nil {
			return err
		}
		done += expected
		if progress != nil {
			progress(done, total)
		}
	}
	parts := make([]protocol.PartInfo, 0, len(have))
	for _, p := range have {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
	if err := c.doJSON(ctx, "PUT", uploadPath, nil, jsonBody(protocol.CompleteRequest{Parts: parts}), -1, "application/json", nil); err != nil {
		return err
	}
	_ = os.Remove(sp)
	return nil
}

func mergeParts(parts []protocol.PartInfo, p protocol.PartInfo) []protocol.PartInfo {
	for i := range parts {
		if parts[i].Number == p.Number {
			parts[i] = p
			return parts
		}
	}
	return append(parts, p)
}

// escapePath 按段转义对象路径，保留层级分隔符。
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func jsonBody(v any) io.Reader {
	data, _ := json.Marshal(v)
	return bytes.NewReader(data)
}

type progressReader struct {
	r     io.Reader
	base  int64
	done  int64
	total int64
	fn    ProgressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.fn != nil {
		p.fn(p.base+p.done, p.total)
	}
	return n, err
}
