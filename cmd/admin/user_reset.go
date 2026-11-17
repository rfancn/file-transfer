package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
)

var userResetCmd = &cobra.Command{
	Use:   "reset <username>",
	Short: "重置用户密码",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		resetUser(adminState.ac, args[0])
	},
}

func init() {
	userCmd.AddCommand(userResetCmd)
}

func resetUser(ac *adminapi.Client, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := ac.ResetPassword(ctx, name)
	if err != nil {
		if ae, ok := adminapi.AsAPIError(err); ok && ae.Code == "user_not_found" {
			fmt.Printf("用户不存在: %s\n", name)
			return
		}
		failf("重置密码失败: %v", err)
		return
	}
	cliui.PrintResetPassword(res.Username, res.Password)
}
