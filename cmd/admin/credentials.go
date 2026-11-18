package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/config"
)

const defaultAdminServerURL = "http://127.0.0.1:8080"

// promptCredential 交互读取一项凭据。ok 为 false 表示用户取消。
func promptCredential(label string, secret bool) (value string, ok bool) {
	var err error
	if secret {
		value, err = cliui.PasswordWithValidate(label, func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("密码不能为空")
			}
			return nil
		})
	} else {
		value, err = cliui.Input(label, "", nil)
	}
	if canceledByUser(err) {
		return "", false
	}
	if err != nil {
		log.Fatalf("读取 %s 失败: %v", label, err)
	}
	return strings.TrimSpace(value), true
}

// login 在显示菜单或执行命令前校验管理员凭证。
func login(cfg config.AdminConfig, user, pass string) *adminapi.Client {
	fmt.Printf("ft-server: %s\n", cfg.Server.URL)
	ac, err := adminapi.New(cfg.Server)
	if err != nil {
		log.Fatalf("连接 ft-server 失败: %v", err)
	}
	// 非交互提供的完整凭据重试没有意义：失败只会重复同一组错误凭据。
	attempts := 3
	if strings.TrimSpace(user) != "" && pass != "" {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		if i == 0 {
			user, pass = serverAdminCreds(user, pass)
		} else {
			fmt.Printf("管理员 %s 登录失败，请重新输入密码\n", user)
			pass = promptPassword()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = ac.Login(ctx, user, pass)
		cancel()
		if err == nil {
			fmt.Printf("管理员 %s 登录成功\n", user)
			return ac
		}
		if ae, ok := adminapi.AsAPIError(err); ok && ae.Status == http.StatusUnauthorized {
			fmt.Println("管理员用户名或密码错误")
			continue
		}
		if ae, ok := adminapi.AsAPIError(err); ok {
			log.Fatalf("校验管理员凭证失败（%s，HTTP %d %s）: %s", cfg.Server.URL, ae.Status, ae.Code, ae.Message)
		}
		log.Fatalf("校验管理员凭证失败（%s）: %v", cfg.Server.URL, err)
	}
	log.Fatal("管理员凭证校验失败，已退出")
	return nil
}

// promptPassword 读取管理员密码；密码不能为空。
func promptPassword() string {
	value, ok := promptCredential("ft-server 管理员密码", true)
	if !ok {
		if cliui.IsInteractive() {
			os.Exit(130)
		}
		log.Fatal("缺少 ft-server 管理员密码（可用 --admin-password 提供）")
	}
	return value
}

// promptAdminUsername 读取管理员用户名，默认 admin。
func promptAdminUsername() (string, bool) {
	value, err := cliui.Input("ft-server 管理员用户名", "admin", nil)
	if canceledByUser(err) {
		return "", false
	}
	if err != nil {
		log.Fatalf("读取 ft-server 管理员用户名 失败: %v", err)
	}
	return strings.TrimSpace(value), true
}

// serverAdminCreds 返回 ft-server 管理员凭证：命令行参数优先，否则交互输入。
func serverAdminCreds(user, pass string) (string, string) {
	if user = strings.TrimSpace(user); user == "" {
		if value, ok := promptAdminUsername(); ok && value != "" {
			user = value
		} else if !ok && cliui.IsInteractive() {
			os.Exit(130)
		} else {
			user = "admin"
		}
	}
	if pass == "" {
		pass = promptPassword()
	}
	if pass == "" {
		log.Fatal("缺少 ft-server 管理员密码（可用 --admin-password 提供）")
	}
	return user, pass
}

// ensureAdminCreds 只在内存中补齐 RustFS root 凭据：命令行参数优先，否则交互输入。
func ensureAdminCreds(cfg *config.AdminConfig, accessKey, secretKey string) bool {
	if accessKey != "" {
		cfg.S3.AccessKey = strings.TrimSpace(accessKey)
	}
	if secretKey != "" {
		cfg.S3.SecretKey = secretKey
	}
	for _, item := range []struct {
		label  string
		secret bool
		hint   string
		dst    *string
	}{
		{"RustFS Access Key", false, "--access-key", &cfg.S3.AccessKey},
		{"RustFS Secret Key", true, "--secret-key", &cfg.S3.SecretKey},
	} {
		if *item.dst != "" {
			continue
		}
		value, ok := promptCredential(item.label, item.secret)
		if !ok {
			if !cliui.IsInteractive() {
				failf("缺少 %s（可用 %s 提供）", item.label, item.hint)
			}
			return false
		}
		*item.dst = value
	}
	if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		failf("缺少 RustFS 管理凭据（可用 --access-key/--secret-key 提供）")
		return false
	}
	return true
}

func promptServerURL() string {
	value, ok := promptCredential("服务器地址 ["+defaultAdminServerURL+"]", false)
	if !ok {
		if cliui.IsInteractive() {
			os.Exit(130)
		}
		return defaultAdminServerURL
	}
	if value == "" {
		return defaultAdminServerURL
	}
	return value
}

func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取凭据文件 %s: %w", path, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
