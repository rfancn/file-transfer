package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
)

var (
	userDelYes bool

	userDelCmd = &cobra.Command{
		Use:   "del <username>",
		Short: "删除用户及其数据",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			delUser(adminState.ac, args[0], userDelYes)
		},
	}
)

func init() {
	userCmd.AddCommand(userDelCmd)
	userDelCmd.Flags().BoolVar(&userDelYes, "yes", false, "跳过确认")
}

func delUser(ac *adminapi.Client, name string, yes bool) {
	if !yes {
		fmt.Printf("将永久删除用户 %s 及其全部数据，不可恢复。\n", name)
		if !confirm(fmt.Sprintf("确认永久删除用户 %s", name)) {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	n, err := ac.DeleteUser(ctx, name)
	if err != nil {
		failf("删除失败: %v", err)
		return
	}
	fmt.Printf("已删除用户 %s，删除对象 %d 个\n", name, n)
}
