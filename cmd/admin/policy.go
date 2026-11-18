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
	"github.com/rfancn/file-transfer/internal/protocol"
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
		value, err := cliui.Input("IP 或网段（逗号分隔，留空表示清除）", "", nil)
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
	if clear {
		applyPolicyIPChange(ac, policyName, sid, nil, iam.IPRangeSet,
			fmt.Sprintf("将清除策略 %s 语句 %s 的 IP 白名单。", policyName, sid), yes)
		return
	}
	applyPolicyIPChange(ac, policyName, sid, cidrs, iam.IPRangeSet,
		fmt.Sprintf("将策略 %s 语句 %s 的 IP 白名单设置为：%s", policyName, sid, strings.Join(cidrs, ", ")), yes)
}

// applyPolicyIPChange 确认并执行一次 IP 白名单修改，返回是否成功。
func applyPolicyIPChange(ac *adminapi.Client, policyName, sid string, cidrs []string, mode, message string, yes bool) bool {
	fmt.Println(message)
	if !yes && !confirm("确认修改") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := ac.ChangeStatementIPRanges(ctx, policyName, sid, cidrs, mode)
	if err != nil {
		failf("修改 IP 白名单失败: %v", err)
		return false
	}
	printPolicyIPs(res)
	return true
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
	sid := strings.TrimSpace(sids[sidIdx])
	policyName := names[idx]

	if !cliui.IsInteractive() {
		runPolicyIPOperations(ac, policyName, sid)
		return
	}
	runPolicyIPWhitelist(ac, policyName, sid)
}

// runPolicyIPOperations 是非交互终端的退化菜单。
func runPolicyIPOperations(ac *adminapi.Client, policyName, sid string) {
	for {
		op, err := cliui.Select("选择操作", []string{
			"查看当前 IP 白名单",
			"添加 IP 或网段",
			"移除 IP 或网段",
			"返回",
		})
		if canceledByUser(err) {
			return
		}
		if err != nil {
			failf("选择操作失败: %v", err)
			return
		}
		switch op {
		case 0:
			listPolicyIPs(ac, policyName, sid)
		case 1:
			addPolicyIPsMenu(ac, policyName, sid)
		case 2:
			removePolicyIPsMenu(ac, policyName, sid)
		case 3:
			return
		}
	}
}

type policyIPAction struct {
	kind string
	cidr string
}

const (
	ipActionEntry  = "entry"
	ipActionDelete = "delete"
	ipActionAdd    = "add"
	ipActionReturn = "return"
)

// runPolicyIPWhitelist 直接展示当前 IP 白名单并用多选列表操作。
func runPolicyIPWhitelist(ac *adminapi.Client, policyName, sid string) {
	checked := map[string]bool{}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cidrs, err := ac.GetStatementIPRanges(ctx, policyName, sid)
		cancel()
		if err != nil {
			failf("读取 IP 白名单失败: %v", err)
			return
		}
		items, actions := buildPolicyIPItems(cidrs, checked)
		label := fmt.Sprintf("策略 %s 语句 %s 的 IP 白名单", policyName, sid)
		if len(cidrs) == 0 {
			label += "（当前为空）"
		}
		idx, err := cliui.MultiSelect(label, items)
		if canceledByUser(err) {
			return
		}
		if err != nil {
			failf("选择操作失败: %v", err)
			return
		}
		for i, action := range actions {
			if action.kind == ipActionEntry && items[i].Touched {
				checked[action.cidr] = items[i].Checked
			}
		}
		switch actions[idx].kind {
		case ipActionEntry:
			continue
		case ipActionDelete:
			selected := checkedIPs(items, actions)
			if len(selected) == 0 {
				fmt.Println("未选择任何 IP 或网段")
				continue
			}
			if removePolicyIPs(ac, policyName, sid, selected) {
				checked = map[string]bool{}
			}
		case ipActionAdd:
			if addPolicyIPsMenu(ac, policyName, sid) {
				checked = map[string]bool{}
			}
		case ipActionReturn:
			return
		}
	}
}

// buildPolicyIPItems 把当前 IP 白名单和操作项转为多选列表。
func buildPolicyIPItems(cidrs []string, checked map[string]bool) ([]cliui.MultiItem, []policyIPAction) {
	items := make([]cliui.MultiItem, 0, len(cidrs)+3)
	actions := make([]policyIPAction, 0, len(cidrs)+3)
	for _, cidr := range cidrs {
		items = append(items, cliui.MultiItem{Text: cidr, Selectable: true, Checked: checked[cidr]})
		actions = append(actions, policyIPAction{kind: ipActionEntry, cidr: cidr})
	}
	for _, action := range []struct {
		text string
		kind string
	}{
		{"[删除选中项]", ipActionDelete},
		{"[添加 IP 或网段]", ipActionAdd},
		{"[返回]", ipActionReturn},
	} {
		items = append(items, cliui.MultiItem{Text: action.text})
		actions = append(actions, policyIPAction{kind: action.kind})
	}
	return items, actions
}

// checkedIPs 返回当前勾选的 IP 或网段。
func checkedIPs(items []cliui.MultiItem, actions []policyIPAction) []string {
	out := make([]string, 0, len(items))
	for i, action := range actions {
		if action.kind == ipActionEntry && items[i].Checked {
			out = append(out, action.cidr)
		}
	}
	return out
}

func listPolicyIPs(ac *adminapi.Client, policyName, sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cidrs, err := ac.GetStatementIPRanges(ctx, policyName, sid)
	if err != nil {
		failf("读取 IP 白名单失败: %v", err)
		return
	}
	printPolicyIPs(protocol.SetStatementIPRangesResponse{Policy: policyName, Sid: sid, Cidrs: cidrs})
}

func promptIPs() ([]string, bool) {
	value, err := cliui.Input("IP 或网段（逗号分隔）", "", nil)
	if canceledByUser(err) {
		return nil, false
	}
	if err != nil {
		failf("读取 IP 或网段失败: %v", err)
		return nil, false
	}
	cidrs := splitCIDRs([]string{value})
	if len(cidrs) == 0 {
		fmt.Println("未输入 IP 或网段，已取消")
		return nil, false
	}
	return cidrs, true
}

// removePolicyIPsMenu 在非交互菜单中读取要移除的 IP 或网段。
func removePolicyIPsMenu(ac *adminapi.Client, policyName, sid string) {
	cidrs, ok := promptIPs()
	if !ok {
		return
	}
	removePolicyIPs(ac, policyName, sid, cidrs)
}

// removePolicyIPs 确认并批量移除指定的 IP 或网段。
func removePolicyIPs(ac *adminapi.Client, policyName, sid string, cidrs []string) bool {
	return applyPolicyIPChange(ac, policyName, sid, cidrs, iam.IPRangeRemove,
		fmt.Sprintf("将从策略 %s 语句 %s 移除：%s", policyName, sid, strings.Join(cidrs, ", ")), false)
}

func addPolicyIPsMenu(ac *adminapi.Client, policyName, sid string) bool {
	cidrs, ok := promptIPs()
	if !ok {
		return false
	}
	if err := validatePolicyIPArgs(policyName, sid, cidrs, false); err != nil {
		failf("%v", err)
		return false
	}
	return applyPolicyIPChange(ac, policyName, sid, cidrs, iam.IPRangeAdd,
		fmt.Sprintf("将向策略 %s 语句 %s 添加：%s", policyName, sid, strings.Join(cidrs, ", ")), false)
}

func printPolicyIPs(res protocol.SetStatementIPRangesResponse) {
	if len(res.Cidrs) == 0 {
		fmt.Printf("策略 %s 语句 %s 当前没有 IP 白名单\n", res.Policy, res.Sid)
		return
	}
	fmt.Printf("策略 %s 语句 %s 当前 IP 白名单：%s\n", res.Policy, res.Sid, strings.Join(res.Cidrs, ", "))
}
