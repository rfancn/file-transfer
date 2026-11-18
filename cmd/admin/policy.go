package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/iam"
)

var (
	policyIPClear bool
	policyIPYes   bool

	policyCmd = &cobra.Command{
		Use:   "policy",
		Short: "管理 IAM 策略",
	}

	policyIPCmd = &cobra.Command{
		Use:   "ip <policy> <sid> [cidr...]",
		Short: "设置策略语句的 IP 白名单",
		Args:  cobra.MinimumNArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			cidrs := splitCIDRs(args[2:])
			setPolicyIPs(adminState.ac, args[0], args[1], cidrs, policyIPClear, policyIPYes)
		},
	}
)

func init() {
	rootCmd.AddCommand(policyCmd)
	policyCmd.AddCommand(policyIPCmd)
	policyIPCmd.Flags().BoolVar(&policyIPClear, "clear", false, "清除 IP 白名单")
	policyIPCmd.Flags().BoolVar(&policyIPYes, "yes", false, "跳过确认")
}

// splitCIDRs 拆分位置参数中的逗号，返回去空白后的非空网段。
func splitCIDRs(args []string) []string {
	var out []string
	for _, arg := range args {
		for _, part := range strings.Split(arg, ",") {
			if v := strings.TrimSpace(part); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func setPolicyIPs(ac *adminapi.Client, policyName, sid string, cidrs []string, clear, yes bool) {
	if clear && len(cidrs) > 0 {
		failf("--clear 不能与网段参数同时使用")
		return
	}
	if iam.IsServicePolicy(policyName) {
		failf("服务身份策略不允许修改")
		return
	}
	if !iam.IsEditablePolicy(policyName) {
		failf("策略只允许 %s", strings.Join(iam.EditablePolicies(), "、"))
		return
	}
	sid = strings.TrimSpace(sid)
	if sid == "" {
		failf("Sid 不能为空")
		return
	}
	if !clear && len(cidrs) == 0 {
		if !cliui.IsInteractive() {
			failf("请提供至少一个网段，或使用 --clear 清除")
			return
		}
		value, err := cliui.Input("IP 网段（逗号分隔，留空表示清除）", "", nil)
		if canceledByUser(err) {
			return
		}
		if err != nil {
			failf("读取网段失败: %v", err)
			return
		}
		cidrs = splitCIDRs([]string{value})
		clear = len(cidrs) == 0
	}
	if !yes {
		if clear {
			fmt.Printf("将清除策略 %s 语句 %s 的 IP 白名单。\n", policyName, sid)
		} else {
			fmt.Printf("将策略 %s 语句 %s 的 IP 白名单设置为：%s\n", policyName, sid, strings.Join(cidrs, ", "))
		}
		if !confirm("确认修改") {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := ac.SetStatementIPRanges(ctx, policyName, sid, cidrs)
	if err != nil {
		failf("设置 IP 白名单失败: %v", err)
		return
	}
	if len(res.Cidrs) == 0 {
		fmt.Printf("已清除策略 %s 语句 %s 的 IP 白名单\n", res.Policy, res.Sid)
		return
	}
	fmt.Printf("已设置策略 %s 语句 %s 的 IP 白名单：%s\n", res.Policy, res.Sid, strings.Join(res.Cidrs, ", "))
}

func runPolicyIPMenu(ac *adminapi.Client) {
	names := iam.EditablePolicies()
	labels := []string{
		"ft-user-policy（普通用户）",
		"ft-operator-policy（摆渡操作员）",
		"shared-operator-policy（共享操作员）",
	}
	idx, err := cliui.Select("选择策略", labels)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "选择策略失败: %v\n", err)
		return
	}
	sid, err := cliui.Input("语句 Sid（例如 UploadWrite）", "", nil)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 Sid 失败: %v\n", err)
		return
	}
	value, err := cliui.Input("IP 网段（逗号分隔，留空表示清除）", "", nil)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取网段失败: %v\n", err)
		return
	}
	cidrs := splitCIDRs([]string{value})
	setPolicyIPs(ac, names[idx], strings.TrimSpace(sid), cidrs, len(cidrs) == 0, false)
}
