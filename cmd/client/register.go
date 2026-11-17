package main

import (
	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/config"
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "注册用户",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runRegister(clientState.cfg, clientState.g.username)
	},
}

func init() {
	rootCmd.AddCommand(registerCmd)
}

func runRegister(cfg config.ClientConfig, name string) {
	if _, _, _, err := registerAccount(cfg, name); err != nil {
		fail(exitInterrupt, "已取消")
	}
}
