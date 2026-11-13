package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// UploadTask 记录一次上传任务的批次和文件清单，用于判断中断和续传。
type UploadTask struct {
	Source    string     `json:"source"`
	Dest      string     `json:"dest,omitempty"`
	Batch     string     `json:"batch"`
	Files     []TaskFile `json:"files"`
	Done      bool       `json:"done"`
	UpdatedAt int64      `json:"updated_at"`
}

// TaskFile 是上传任务中的一个文件。
type TaskFile struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Size   int64  `json:"size"`
}

// TaskProgress 是任务在远端和本地分片状态上的实际进度。
type TaskProgress struct {
	Task          UploadTask
	DoneFiles     int
	UploadedBytes int64
	TotalBytes    int64
	Pending       []InterruptedUpload
	// Uploaded 以本地绝对路径为键，标记该文件是否已完整上传到远端。
	Uploaded map[string]bool
}

// 文件变化类型。
const (
	ChangeAdded    = "added"
	ChangeMissing  = "missing"
	ChangeModified = "modified"
	ChangeSame     = "same"
)

// FileChange 描述上次任务和本次计划之间的单个文件差异。
type FileChange struct {
	Local    string
	Remote   string
	Name     string
	Kind     string
	OldSize  int64
	NewSize  int64
	Uploaded bool
}

// DiffTaskFiles 比较上次任务清单与本次计划，返回按名称排序的文件变化。
func DiffTaskFiles(previous []TaskFile, current []UploadFile, uploaded map[string]bool, source string) []FileChange {
	prev := make(map[string]TaskFile, len(previous))
	for _, f := range previous {
		prev[absPath(f.Local)] = f
	}
	cur := make(map[string]UploadFile, len(current))
	for _, f := range current {
		cur[absPath(f.Local)] = f
	}
	var changes []FileChange
	for key, old := range prev {
		if now, ok := cur[key]; ok {
			kind := ChangeSame
			if now.Size != old.Size {
				kind = ChangeModified
			}
			changes = append(changes, FileChange{
				Local: key, Remote: now.Remote, Name: changeName(source, key), Kind: kind,
				OldSize: old.Size, NewSize: now.Size, Uploaded: uploaded[key],
			})
			delete(cur, key)
			continue
		}
		changes = append(changes, FileChange{
			Local: key, Remote: old.Remote, Name: changeName(source, key), Kind: ChangeMissing,
			OldSize: old.Size, Uploaded: uploaded[key],
		})
	}
	for key, now := range cur {
		changes = append(changes, FileChange{
			Local: key, Remote: now.Remote, Name: changeName(source, key), Kind: ChangeAdded,
			NewSize: now.Size,
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes
}

func changeName(source, local string) string {
	rel, err := filepath.Rel(source, local)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(local)
	}
	return filepath.ToSlash(rel)
}

// NextBatch 返回当天新的批次号，格式为 YYYYMMDDNN，例如 2026111301。
func (c *Client) NextBatch(ctx context.Context) (string, error) {
	date := time.Now().Format("20060102")
	files, err := c.List(ctx, "outbound")
	if err != nil {
		return "", err
	}
	maxSeq := 0
	for _, f := range files {
		top := strings.SplitN(f.Path, "/", 2)[0]
		if len(top) <= len(date) || !strings.HasPrefix(top, date) {
			continue
		}
		seq, err := strconv.Atoi(top[len(date):])
		if err == nil && seq > maxSeq {
			maxSeq = seq
		}
	}
	return fmt.Sprintf("%s%02d", date, maxSeq+1), nil
}

func tasksDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "file-transfer", "tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func taskFilePath(username, source, dest string) (string, error) {
	dir, err := tasksDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(source + "\x00" + dest))
	return filepath.Join(dir, username+"-"+hex.EncodeToString(sum[:])+".json"), nil
}

// SaveUploadTask 写入任务状态；UpdatedAt 由本函数更新。
func SaveUploadTask(username string, t *UploadTask) error {
	path, err := taskFilePath(username, t.Source, t.Dest)
	if err != nil {
		return err
	}
	t.UpdatedAt = time.Now().Unix()
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadUploadTask 读取指定源路径和目标的未完成任务；不存在时返回 os.ErrNotExist。
func LoadUploadTask(username, source, dest string) (*UploadTask, error) {
	path, err := taskFilePath(username, source, dest)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t UploadTask
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// CompleteUploadTask 标记任务已完成。
func CompleteUploadTask(username, source, dest string) error {
	t, err := LoadUploadTask(username, source, dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	t.Done = true
	return SaveUploadTask(username, t)
}

// ListPendingUploadTasks 返回所有未完成的上传任务，按更新时间倒序。
func ListPendingUploadTasks(username string) ([]UploadTask, error) {
	dir, err := tasksDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	prefix := username + "-"
	var tasks []UploadTask
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var t UploadTask
		if json.Unmarshal(data, &t) != nil || t.Done {
			continue
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].UpdatedAt > tasks[j].UpdatedAt })
	return tasks, nil
}

// MissingFiles 返回上次任务里有、当前计划里没有的文件（绝对路径，已排序）。
func MissingFiles(previous []TaskFile, current []UploadFile) []string {
	currentSet := make(map[string]bool, len(current))
	for _, f := range current {
		currentSet[absPath(f.Local)] = true
	}
	seen := make(map[string]bool, len(previous))
	var missing []string
	for _, f := range previous {
		p := absPath(f.Local)
		if currentSet[p] || seen[p] {
			continue
		}
		seen[p] = true
		missing = append(missing, p)
	}
	sort.Strings(missing)
	return missing
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// InspectTask 结合远端文件和本地分片状态计算任务进度。
func (c *Client) InspectTask(ctx context.Context, task UploadTask) (TaskProgress, error) {
	p := TaskProgress{Task: task, Uploaded: make(map[string]bool, len(task.Files))}
	files, err := c.List(ctx, "outbound")
	if err != nil {
		return p, err
	}
	remote := make(map[string]int64, len(files))
	for _, f := range files {
		remote[f.Path] = f.Size
	}
	var remaining []UploadFile
	for _, f := range task.Files {
		p.TotalBytes += f.Size
		key := strings.TrimPrefix(f.Remote, "outbound/")
		if size, ok := remote[key]; ok && size == f.Size {
			p.DoneFiles++
			p.UploadedBytes += f.Size
			p.Uploaded[absPath(f.Local)] = true
			continue
		}
		remaining = append(remaining, UploadFile{Local: f.Local, Remote: f.Remote, Size: f.Size})
	}
	if len(remaining) > 0 {
		p.Pending = c.Interrupted(UploadPlan{Files: remaining})
		for _, item := range p.Pending {
			p.UploadedBytes += item.UploadedBytes
		}
	}
	return p, nil
}
