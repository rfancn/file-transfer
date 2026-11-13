package adminuser

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rfancn/file-transfer/internal/auth"
)

const (
	// DefaultName 是启动时自动生成的管理员用户名。
	DefaultName = "admin"
	passwordLen = 10
	dirName     = "secrets"
	fileName    = "user.json"
)

// Path 返回当前工作目录下的 secrets/user.json。
func Path() string {
	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join(dirName, fileName)
	}
	return filepath.Join(wd, dirName, fileName)
}

// Store 是 ft-server 自己的极简管理员账号表：用户名 -> 明文密码。
// 文件权限 0600、目录 0700，仅保存在服务端本地。
type Store struct {
	path  string
	users map[string]string
}

// LoadOrCreate 读取账号文件；不存在或没有账号时生成 admin 并写入。
// 返回新生成的密码；已有账号时返回空字符串。
func LoadOrCreate(path string) (*Store, string, error) {
	s := &Store{path: path, users: map[string]string{}}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &s.users); err != nil {
			return nil, "", fmt.Errorf("解析 %s: %w", path, err)
		}
		if len(s.users) > 0 {
			return s, "", nil
		}
	case errors.Is(err, os.ErrNotExist):
		// 首次启动，继续生成。
	default:
		return nil, "", err
	}

	password, err := auth.GeneratePassword(passwordLen)
	if err != nil {
		return nil, "", err
	}
	s.users[DefaultName] = password
	if err := s.save(); err != nil {
		return nil, "", err
	}
	return s, password, nil
}

// Verify 校验管理员用户名和密码。
func (s *Store) Verify(username, password string) bool {
	want, ok := s.users[username]
	if !ok {
		// 未知用户也执行一次比较，避免明显的时序差异。
		subtle.ConstantTimeCompare([]byte(password), []byte("invalid-password-000"))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(want)) == 1
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(s.users, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".user.json.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
