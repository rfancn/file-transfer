package main

import (
	"context"
	"errors"
	"fmt"
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

// policyIPLabels 是菜单展示文案；策略名仍以 iam.EditablePolicies() 为准。
var policyIPLabels = map[string]string{
	iam.UserGroupPol:   "ft-user-policy（普通用户）",
	iam.AdminGroupPol:  "ft-operator-policy（摆渡操作员）",
	iam.SharedAdminPol: "shared-operator-policy（共享操作员）",
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

// validatePolicyIPArgs 校验策略名、Sid 与清空/网段组合。
func validatePolicyIPArgs(policyName, sid string, cidrs []string, clear bool) error {
	policyName = strings.TrimSpace(policyName)
	sid = strings.TrimSpace(sid)
	if clear && len(cidrs) > 0 {
		return errors.New("--clear 不能与网段参数同时使用")
	}
	if iam.IsServicePolicy(policyName) {
		return errors.New("服务身份策略不允许修改")
	}
	if !iam.IsEditablePolicy(policyName) {
		return fmt.Errorf("策略只允许 %s", strings.Join(iam.EditablePolicies(), "、"))
	}
	if sid == "" {
		return errors.New("Sid 不能为空")
	}
	return nil
}

func setPolicyIPs(ac *adminapi.Client, policyName, sid string, cidrs []string, clear, yes bool) {
	policyName = strings.TrimSpace(policyName)
	sid = strings.TrimSpace(sid)
	if err := validatePolicyIPArgs(policyName, sid, cidrs, clear); err != nil {
		failf("%v", err)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	labels := make([]string, 0, len(names))
	for _, name := range names {
		label, ok := policyIPLabels[name]
		if !ok {
			failf("策略 %s 缺少菜单标签", name)
			return
		}
		labels = append(labels, label)
	}
	idx, err := cliui.Select("选择策略", labels)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		failf("选择策略失败: %v", err)
		return
	}
	if idx < 0 || idx >= len(names) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	sids, err := ac.ListPolicySids(ctx, names[idx])
	cancel()
	if err != nil {
		failf("获取策略语句失败: %v", err)
		return
	}
	if len(sids) == 0 {
		failf("策略 %s 没有可设置的 Allow 语句", names[idx])
		return
	}
	sidIdx, err := cliui.Select("选择语句 Sid", sids)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		failf("选择语句失败: %v", err)
		return
	}
	sid := sids[sidIdx]
	value, err := cliui.Input("IP 网段（逗号分隔，留空表示清除）", "", nil)
	if canceledByUser(err) {
		return
	}
	if err != nil {
		failf("读取网段失败: %v", err)
		return
	}
	cidrs := splitCIDRs([]string{value})
	setPolicyIPs(ac, names[idx], strings.TrimSpace(sid), cidrs, len(cidrs) == 0, false)
}
