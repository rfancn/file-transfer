package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/iam"
)

var (
	userAddGroup string
	userAddYes   bool

	userAddCmd = &cobra.Command{
		Use:   "add <username>",
		Short: "添加用户",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			addUser(adminState.ac, args[0], userAddGroup, userAddYes)
		},
	}
)

func init() {
	userCmd.AddCommand(userAddCmd)
	userAddCmd.Flags().StringVar(&userAddGroup, "group", "", "用户组：user、operator 或 shared-operator")
	userAddCmd.Flags().BoolVar(&userAddYes, "yes", false, "跳过确认")
}

// groupOption 描述一个可分配的用户组及其在 CLI 中的展示信息。
type groupOption struct {
	name    string
	alias   string
	label   string
	role    string // 提示文案中的角色名
	warning string // 高权限警告，空表示普通组
}

var groupOptions = []groupOption{
	{iam.UserGroup, "user", "普通用户组 (ft-user-group)", "用户", ""},
	{iam.AdminGroup, "operator", "摆渡文件操作员组 (ft-operator-group)", "摆渡文件操作员",
		"警告：摆渡文件操作员可以读取所有用户的 outbound，并向所有用户 inbound 放件/删除文件。"},
	{iam.SharedAdminGroup, "shared-operator", "共享文件操作员组 (shared-operator-group)", "共享文件操作员",
		"警告：共享文件操作员可以读取、上传和删除共享桶中的全部内容。"},
}

// resolveGroup 把别名或组名解析为组选项；group 为空时交互选择。
func resolveGroup(group string) (groupOption, bool) {
	if group == "" {
		if !cliui.IsInteractive() {
			failf("非交互创建用户必须用 --group user、--group operator 或 --group shared-operator 指定组")
			return groupOption{}, false
		}
		labels := make([]string, len(groupOptions))
		for i, o := range groupOptions {
			labels[i] = o.label
		}
		idx, err := cliui.Select("选择用户组", labels)
		if canceledByUser(err) {
			return groupOption{}, false
		}
		if err != nil {
			failf("选择用户组失败: %v", err)
			return groupOption{}, false
		}
		return groupOptions[idx], true
	}
	for _, o := range groupOptions {
		if group == o.alias || group == o.name {
			return o, true
		}
	}
	failf("组只允许 %s、%s 或 %s", iam.UserGroup, iam.AdminGroup, iam.SharedAdminGroup)
	return groupOption{}, false
}

func addUser(ac *adminapi.Client, name, group string, yes bool) {
	opt, ok := resolveGroup(group)
	if !ok {
		return
	}
	highPrivilege := opt.warning != ""
	if highPrivilege {
		fmt.Println(opt.warning)
		if !yes {
			if !cliui.IsInteractive() {
				failf("非交互创建%s账号需要 --yes", opt.role)
				return
			}
			if !confirm("确认创建高权限账号") {
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := ac.AddUser(ctx, name, opt.name, highPrivilege)
	if err != nil {
		if ae, ok := adminapi.AsAPIError(err); ok && ae.Code == "already_exists" {
			fmt.Printf("用户已存在，不重置密码: %s\n", name)
			return
		}
		failf("创建用户失败: %v", err)
		return
	}
	cliui.PrintAccount(res.Username, res.Password)
}
