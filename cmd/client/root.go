package main

import (
	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/version"
)

type globals struct {
	configPath    string
	server        string
	debug         bool
	username      string
	passwordStdin bool
}

type clientApp struct {
	g   globals
	cfg config.ClientConfig
}

var clientState = &clientApp{}

var rootCmd = &cobra.Command{
	Use:           "ft-client",
	Short:         "文件摆渡客户端",
	Version:       version.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.NoArgs,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		switch cmd.Name() {
		case "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			return nil
		}
		cfg, err := config.LoadClient(clientState.g.configPath)
		if err != nil {
			fail(exitUsage, "加载配置失败: %v", err)
		}
		if clientState.g.server != "" {
			cfg.Server.URL = clientState.g.server
		}
		if cfg.Server.URL == "" {
			cfgPath := clientState.g.configPath
			if cfgPath == "" {
				cfgPath = config.DefaultClientConfig
			}
			fail(exitUsage, "未配置 server.url：请在 %s 的 [server] 段设置，或使用 --server", cfgPath)
		}
		clientState.cfg = cfg
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		runMenu(clientState.g, clientState.cfg)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&clientState.g.configPath, "config", "c", "", "客户端配置")
	rootCmd.PersistentFlags().StringVar(&clientState.g.server, "server", "", "覆盖服务器地址")
	rootCmd.PersistentFlags().BoolVar(&clientState.g.debug, "debug", false, "输出调试信息")
	rootCmd.PersistentFlags().StringVar(&clientState.g.username, "username", "", "账号")
	rootCmd.PersistentFlags().BoolVar(&clientState.g.passwordStdin, "password-stdin", false, "从标准输入读取密码")
	rootCmd.SetVersionTemplate("{{.Version}}\n")
}
