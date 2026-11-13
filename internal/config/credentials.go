package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

const clientCredentialsFileName = ".file-transfer"

// ClientCredentials 是保存在用户主目录下的客户端账号信息。
type ClientCredentials struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// ClientCredentialsPath 返回客户端账号信息文件路径。
func ClientCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	return filepath.Join(home, clientCredentialsFileName), nil
}

// LoadClientCredentials 读取保存的客户端账号信息。
func LoadClientCredentials() (ClientCredentials, error) {
	path, err := ClientCredentialsPath()
	if err != nil {
		return ClientCredentials{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ClientCredentials{}, err
	}
	var c ClientCredentials
	if err := toml.Unmarshal(body, &c); err != nil {
		return ClientCredentials{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	if c.Username == "" || c.Password == "" {
		return ClientCredentials{}, fmt.Errorf("%s 缺少 username 或 password", path)
	}
	return c, nil
}

// SaveClientCredentials 以 0600 权限写入客户端账号信息。
func SaveClientCredentials(c ClientCredentials) error {
	path, err := ClientCredentialsPath()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	// 已有文件可能保留了旧权限，显式收紧。
	return os.Chmod(path, 0o600)
}

// RemoveClientCredentials 删除保存的客户端账号信息。
func RemoveClientCredentials() error {
	path, err := ClientCredentialsPath()
	if err != nil {
		return err
	}
	return os.Remove(path)
}
