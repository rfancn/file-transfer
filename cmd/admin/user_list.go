package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
)

var userListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出用户",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		listUsers(adminState.ac)
	},
}

func init() {
	userCmd.AddCommand(userListCmd)
}

func listUsers(ac *adminapi.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	users, err := ac.ListUsers(ctx)
	if err != nil {
		failf("列出用户失败: %v", err)
		return
	}
	fmt.Printf("%-24s %-6s %-10s %-32s %s\n", "用户名", "类型", "状态", "策略", "组")
	for _, u := range users {
		kind := "普通"
		if u.System {
			kind = "系统"
		}
		fmt.Printf("%-24s %-6s %-10s %-32s %s\n", u.Username, kind, u.Status, u.PolicyName, strings.Join(u.Groups, ","))
	}
}
