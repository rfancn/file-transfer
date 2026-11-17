package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/version"
)

type adminApp struct {
	cfg               config.AdminConfig
	ac                *adminapi.Client
	configPath        string
	adminUser         string
	adminPassword     string
	adminPasswordFile string
}

var adminState = &adminApp{}

var rootCmd = &cobra.Command{
	Use:           "ft-admin",
	Short:         "文件摆渡管理客户端",
	Version:       version.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch cmd.Name() {
		case "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return nil
		}
		return adminState.prepare()
	},
	Run: func(cmd *cobra.Command, args []string) {
		menu(adminState.cfg, adminState.ac)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&adminState.configPath, "config", "c", "", "管理端配置")
	rootCmd.PersistentFlags().StringVar(&adminState.adminUser, "admin-user", "", "ft-server 管理员用户名")
	rootCmd.PersistentFlags().StringVar(&adminState.adminPassword, "admin-password", "", "ft-server 管理员密码")
	rootCmd.PersistentFlags().StringVar(&adminState.adminPasswordFile, "admin-password-file", "", "从文件读取 ft-server 管理员密码")
	rootCmd.SetVersionTemplate("{{.Version}}\n")
}

func (a *adminApp) prepare() error {
	cfg, err := config.LoadAdmin(a.configPath)
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}
	a.cfg = cfg
	if a.cfg.Server.URL == "" {
		a.cfg.Server.URL = promptServerURL()
	}
	if a.adminPasswordFile != "" {
		pass, err := readSecretFile(a.adminPasswordFile)
		if err != nil {
			return err
		}
		a.adminPassword = pass
	}
	a.ac = login(a.cfg, a.adminUser, a.adminPassword)
	return nil
}
