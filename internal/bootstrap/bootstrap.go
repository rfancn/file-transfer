package bootstrap

import (
	"context"
	"fmt"

	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/iam"
	"github.com/rfancn/file-transfer/internal/s3"
)

const (
	// 服务身份密码长度。
	servicePasswordLen = 24
	// 文件摆渡系统操作员密码长度。
	operatorPasswordLen = 8
)

// Result 描述本次引导产生的结果。
type Result struct {
	ConsoleCreated     bool
	ConsolePassword    string
	ConfigChanged      bool
	DataPasswordReset  bool
	AdminPasswordReset bool
}

// Run 用 RustFS 管理凭据完成初始化，并把生成的服务凭据写入当前目录的 secrets/s3_credentials.json。
func Run(ctx context.Context, cfg config.ServerConfig, accessKey, secretKey string) (Result, error) {
	var res Result
	dataCreds, adminCreds, _ := config.LoadServiceSecrets()

	mgr, err := iam.New(cfg.S3.Endpoint, accessKey, secretKey, cfg.S3.Region, cfg.S3.Bucket)
	if err != nil {
		return res, fmt.Errorf("创建管理客户端: %w", err)
	}
	if err := mgr.S3.EnsureBucket(ctx); err != nil {
		return res, fmt.Errorf("创建桶 %s: %w", cfg.S3.Bucket, err)
	}

	dataExists, err := mgr.UserExists(ctx, iam.DataUser)
	if err != nil {
		return res, fmt.Errorf("检查 %s: %w", iam.DataUser, err)
	}
	adminExists, err := mgr.UserExists(ctx, iam.ServerAdmin)
	if err != nil {
		return res, fmt.Errorf("检查 %s: %w", iam.ServerAdmin, err)
	}
	if dataExists && (dataCreds.SecretKey == "" || !dataCredentialsValid(ctx, cfg, config.Credentials{AccessKey: iam.DataUser, SecretKey: dataCreds.SecretKey})) {
		p, err := auth.GeneratePassword(servicePasswordLen)
		if err != nil {
			return res, fmt.Errorf("生成 %s 密码: %w", iam.DataUser, err)
		}
		if err := mgr.ResetUserPassword(ctx, iam.DataUser, p); err != nil {
			return res, fmt.Errorf("重置 %s 密码: %w", iam.DataUser, err)
		}
		dataCreds.SecretKey = p
		res.DataPasswordReset = true
		res.ConfigChanged = true
	}
	if adminExists && (adminCreds.SecretKey == "" || !adminCredentialsValid(ctx, cfg, config.Credentials{AccessKey: iam.ServerAdmin, SecretKey: adminCreds.SecretKey})) {
		p, err := auth.GeneratePassword(servicePasswordLen)
		if err != nil {
			return res, fmt.Errorf("生成 %s 密码: %w", iam.ServerAdmin, err)
		}
		if err := mgr.ResetUserPassword(ctx, iam.ServerAdmin, p); err != nil {
			return res, fmt.Errorf("重置 %s 密码: %w", iam.ServerAdmin, err)
		}
		adminCreds.SecretKey = p
		res.AdminPasswordReset = true
		res.ConfigChanged = true
	}

	if dataCreds.AccessKey != iam.DataUser {
		dataCreds.AccessKey = iam.DataUser
		res.ConfigChanged = true
	}
	if adminCreds.AccessKey != iam.ServerAdmin {
		adminCreds.AccessKey = iam.ServerAdmin
		res.ConfigChanged = true
	}
	if dataCreds.SecretKey == "" {
		p, err := auth.GeneratePassword(servicePasswordLen)
		if err != nil {
			return res, fmt.Errorf("生成 %s 密码: %w", iam.DataUser, err)
		}
		dataCreds.SecretKey = p
		res.ConfigChanged = true
	}
	if adminCreds.SecretKey == "" {
		p, err := auth.GeneratePassword(servicePasswordLen)
		if err != nil {
			return res, fmt.Errorf("生成 %s 密码: %w", iam.ServerAdmin, err)
		}
		adminCreds.SecretKey = p
		res.ConfigChanged = true
	}

	if err := mgr.EnsureServiceIdentities(ctx, iam.DataUser, dataCreds.SecretKey, iam.ServerAdmin, adminCreds.SecretKey); err != nil {
		return res, fmt.Errorf("初始化服务身份: %w", err)
	}
	// 全新环境中还没有用户，ft-user-group 尚不存在，此时只能创建策略；
	// 首个用户注册时 SetupUser 会先 AddToGroup 再挂组策略。
	var userGroups []string
	groupExists, err := mgr.GroupExists(ctx, iam.UserGroup)
	if err != nil {
		return res, fmt.Errorf("检查用户组 %s: %w", iam.UserGroup, err)
	}
	if groupExists {
		userGroups = []string{iam.UserGroup}
	}
	if err := mgr.ReplacePolicy(ctx, iam.UserGroupPol, iam.GroupPolicy(cfg.S3.Bucket), nil, userGroups); err != nil {
		return res, fmt.Errorf("更新用户组策略: %w", err)
	}

	consolePass, err := auth.GeneratePassword(operatorPasswordLen)
	if err != nil {
		return res, fmt.Errorf("生成文件摆渡系统操作员密码: %w", err)
	}
	created, err := mgr.EnsureAdminConsole(ctx, consolePass)
	if err != nil {
		return res, fmt.Errorf("初始化文件摆渡系统操作员: %w", err)
	}
	res.ConsoleCreated = created
	if created {
		res.ConsolePassword = consolePass
	}

	if err := config.SaveServiceSecrets(dataCreds, adminCreds); err != nil {
		return res, fmt.Errorf("写入服务凭据: %w", err)
	}
	return res, nil
}

// dataCredentialsValid 用数据面身份执行一次允许的 S3 操作，验证已保存的凭据仍然有效。
func dataCredentialsValid(ctx context.Context, cfg config.ServerConfig, creds config.Credentials) bool {
	sm, err := s3.New(cfg.S3.Endpoint, creds.AccessKey, creds.SecretKey, cfg.S3.Region, cfg.S3.Bucket)
	if err != nil {
		return false
	}
	ok, err := sm.Client.BucketExists(ctx, cfg.S3.Bucket)
	return err == nil && ok
}

// adminCredentialsValid 用管理面身份查询自身信息，验证已保存的凭据仍然有效。
func adminCredentialsValid(ctx context.Context, cfg config.ServerConfig, creds config.Credentials) bool {
	m, err := iam.New(cfg.S3.Endpoint, creds.AccessKey, creds.SecretKey, cfg.S3.Region, cfg.S3.Bucket)
	if err != nil {
		return false
	}
	_, err = m.Admin.GetUserInfo(ctx, iam.ServerAdmin)
	return err == nil
}
