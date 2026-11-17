package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/config"
)

var (
	initAccessKey     string
	initSecretKey     string
	initSecretKeyFile string

	initCmd = &cobra.Command{
		Use:   "init",
		Short: "初始化 RustFS 桶、策略和系统身份",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if initSecretKey == "" && initSecretKeyFile != "" {
				value, err := readSecretFile(initSecretKeyFile)
				if err != nil {
					failf("读取 RustFS Secret Key 失败: %v", err)
					return
				}
				initSecretKey = value
			}
			runInit(&adminState.cfg, adminState.ac, initAccessKey, initSecretKey)
		},
	}
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&initAccessKey, "access-key", "", "RustFS Access Key")
	initCmd.Flags().StringVar(&initSecretKey, "secret-key", "", "RustFS Secret Key")
	initCmd.Flags().StringVar(&initSecretKeyFile, "secret-key-file", "", "从文件读取 RustFS Secret Key")
}

// runInit 收集 root 凭据并调用 ft-server 的初始化接口；凭据只在内存中使用。
func runInit(cfg *config.AdminConfig, ac *adminapi.Client, accessKey, secretKey string) {
	if !ensureAdminCreds(cfg, accessKey, secretKey) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ac.Init(ctx, cfg.S3.AccessKey, cfg.S3.SecretKey); err != nil {
		failf("初始化失败: %v（请确认 root AK/SK 正确，且 ft-server 能访问 s3.endpoint）", err)
		return
	}
	fmt.Println("初始化完成。首次初始化时 ft-server 会自动重启以加载配置。")
}
