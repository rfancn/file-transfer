package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/madmin-go/v3"
)

const (
	maxCIDRs = 100

	IPRangeSet    = "set"
	IPRangeAdd    = "add"
	IPRangeRemove = "remove"
)

var (
	ErrUnsupportedPolicy = errors.New("策略不支持此操作")
	ErrServicePolicy     = errors.New("服务身份策略不允许修改")
	ErrSIDNotFound       = errors.New("策略中不存在指定 Sid")
	ErrStatementNotAllow = errors.New("指定 Sid 不是 Allow 语句")
	ErrInvalidCIDR       = errors.New("无效的 IP 或网段")
	ErrInvalidPolicy     = errors.New("策略内容无效")
	ErrInvalidMode       = errors.New("无效的 IP 白名单操作")
	ErrPolicyRead        = errors.New("读取策略文件失败")
	ErrPolicyWrite       = errors.New("写入策略文件失败")
	ErrApplyFailed       = errors.New("策略已持久化但下发 RustFS 失败")
)

// EditablePolicies 返回允许设置 IP 白名单的策略名。
func EditablePolicies() []string {
	out := make([]string, 0, len(policyDefs))
	for _, def := range policyDefs {
		out = append(out, def.Policy)
	}
	return out
}

// IsEditablePolicy 返回 policyName 是否允许设置 IP 白名单。
func IsEditablePolicy(policyName string) bool {
	_, ok := policyDefForPolicy(policyName)
	return ok
}

// IsServicePolicy 返回 policyName 是否为服务身份策略。
func IsServicePolicy(policyName string) bool {
	return policyName == DataPolicy || policyName == ServerAdminPol
}

// editableDocument 校验策略并返回其定义和内存文档。
func (m *Manager) editableDocument(policyName string) (policyDef, []byte, error) {
	if IsServicePolicy(policyName) {
		return policyDef{}, nil, ErrServicePolicy
	}
	def, ok := policyDefForPolicy(policyName)
	if !ok {
		return policyDef{}, nil, ErrUnsupportedPolicy
	}
	if m.Policies == nil {
		return def, nil, ErrPolicyRead
	}
	doc, err := m.Policies.Document(policyName)
	if err != nil {
		return def, nil, err
	}
	return def, doc, nil
}

// PolicySids 返回策略中可设置 IP 白名单的 Allow 语句 Sid（按模板顺序）。
func (m *Manager) PolicySids(policyName string) ([]string, error) {
	_, doc, err := m.editableDocument(policyName)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Statement []struct {
			Sid    string `json:"Sid"`
			Effect string `json:"Effect"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	sids := make([]string, 0, len(parsed.Statement))
	seen := make(map[string]struct{}, len(parsed.Statement))
	for _, st := range parsed.Statement {
		if st.Sid == "" || st.Effect != "Allow" {
			continue
		}
		if _, ok := seen[st.Sid]; ok {
			return nil, fmt.Errorf("%w: 重复 Sid %q", ErrInvalidPolicy, st.Sid)
		}
		seen[st.Sid] = struct{}{}
		sids = append(sids, st.Sid)
	}
	return sids, nil
}

// NormalizeCIDRs 校验并规范化 IP/网段列表，去掉重复项。
// 单个 IP 会规范化为 /32（IPv4）或 /128（IPv6），网段会清零主机位。
func NormalizeCIDRs(cidrs []string) ([]string, error) {
	if len(cidrs) > maxCIDRs {
		return nil, fmt.Errorf("%w: 最多 %d 条", ErrInvalidCIDR, maxCIDRs)
	}
	out := make([]string, 0, len(cidrs))
	seen := make(map[string]struct{}, len(cidrs))
	for _, raw := range cidrs {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, fmt.Errorf("%w: 不能为空", ErrInvalidCIDR)
		}
		normalized, err := normalizeSourceIP(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidCIDR, value)
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

// normalizeSourceIP 接受单个 IP 或 CIDR 网段，返回规范化后的 CIDR。
func normalizeSourceIP(value string) (string, error) {
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return "", err
		}
		return prefix.Masked().String(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return "", err
	}
	if addr.Zone() != "" {
		return "", errors.New("不支持 IPv6 zone")
	}
	return netip.PrefixFrom(addr, addr.BitLen()).String(), nil
}

// SetStatementSourceIP 为指定 Sid 的 Allow 语句设置或清除 aws:SourceIp 条件。
// cidrs 必须已经过 NormalizeCIDRs 规范化；为空时清除条件。
// 写回会重新序列化整篇策略（键按字母排序、两空格缩进）。
func SetStatementSourceIP(policy []byte, sid string, cidrs []string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(policy, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	target, err := findAllowStatement(doc, sid)
	if err != nil {
		return nil, err
	}
	if err := setSourceIPCondition(target, cidrs); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	return append(out, '\n'), nil
}

// findAllowStatement 定位 Sid 唯一匹配的 Allow 语句。
func findAllowStatement(doc map[string]any, sid string) (map[string]any, error) {
	if sid == "" {
		return nil, ErrSIDNotFound
	}
	rawStatements, ok := doc["Statement"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: Statement 必须是数组", ErrInvalidPolicy)
	}
	var target map[string]any
	for i, raw := range rawStatements {
		statement, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: 第 %d 条 Statement 不是对象", ErrInvalidPolicy, i+1)
		}
		rawSID, exists := statement["Sid"]
		if !exists {
			continue
		}
		statementSID, ok := rawSID.(string)
		if !ok {
			return nil, fmt.Errorf("%w: 第 %d 条 Statement 的 Sid 不是字符串", ErrInvalidPolicy, i+1)
		}
		if statementSID != sid {
			continue
		}
		if target != nil {
			return nil, fmt.Errorf("%w: 重复 Sid %q", ErrInvalidPolicy, sid)
		}
		target = statement
	}
	if target == nil {
		return nil, fmt.Errorf("%w: %q", ErrSIDNotFound, sid)
	}
	effect, _ := target["Effect"].(string)
	if effect != "Allow" {
		return nil, fmt.Errorf("%w: %q", ErrStatementNotAllow, sid)
	}
	return target, nil
}

// StatementIPRanges 返回指定策略语句当前的 aws:SourceIp 列表。
func (m *Manager) StatementIPRanges(policyName, sid string) ([]string, error) {
	_, doc, err := m.editableDocument(policyName)
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(doc, &parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	target, err := findAllowStatement(parsed, sid)
	if err != nil {
		return nil, err
	}
	condition, _ := target["Condition"].(map[string]any)
	ipAddress, _ := condition["IpAddress"].(map[string]any)
	rawIPs, ok := ipAddress["aws:SourceIp"]
	if !ok || rawIPs == nil {
		return []string{}, nil
	}
	values, ok := rawIPs.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: aws:SourceIp 必须是数组", ErrInvalidPolicy)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%w: aws:SourceIp 值必须是字符串", ErrInvalidPolicy)
		}
		out = append(out, s)
	}
	return NormalizeCIDRs(out)
}

// applyIPRangeMode 根据 set/add/remove 计算最终列表。
func applyIPRangeMode(current, changes []string, mode string) ([]string, error) {
	switch mode {
	case IPRangeSet:
		return changes, nil
	case IPRangeAdd:
		seen := make(map[string]struct{}, len(current)+len(changes))
		out := make([]string, 0, len(current)+len(changes))
		for _, v := range current {
			seen[v] = struct{}{}
			out = append(out, v)
		}
		for _, v := range changes {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
		return out, nil
	case IPRangeRemove:
		remove := make(map[string]struct{}, len(changes))
		for _, v := range changes {
			remove[v] = struct{}{}
		}
		out := make([]string, 0, len(current))
		for _, v := range current {
			if _, ok := remove[v]; ok {
				continue
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, ErrInvalidMode
	}
}

// SetStatementIPRanges 修改指定策略模板中的语句并立即下发到 RustFS。
// 所有策略写操作共享 policyWriteMu（定义在 iam.go）。
func (m *Manager) SetStatementIPRanges(ctx context.Context, policyName, sid string, cidrs []string, mode string) ([]string, error) {
	if mode == "" {
		mode = IPRangeSet
	}
	switch mode {
	case IPRangeSet, IPRangeAdd, IPRangeRemove:
	default:
		return nil, ErrInvalidMode
	}
	normalized, err := NormalizeCIDRs(cidrs)
	if err != nil {
		return nil, err
	}
	def, _, err := m.editableDocument(policyName)
	if err != nil {
		return nil, err
	}
	// 先做内存校验，非法策略/Sid 不发起 RustFS RPC。
	if _, err := m.StatementIPRanges(policyName, sid); err != nil {
		return nil, err
	}

	policyWriteMu.Lock()
	defer policyWriteMu.Unlock()

	// 先发现绑定关系，RustFS 列举失败时不修改本地模板。
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	users, groups, err := m.policyAttachments(listCtx, policyName, def.Group)
	if err != nil {
		return nil, err
	}

	var current []string
	if mode != IPRangeSet {
		current, err = m.StatementIPRanges(policyName, sid)
		if err != nil {
			return nil, err
		}
	}
	final, err := applyIPRangeMode(current, normalized, mode)
	if err != nil {
		return nil, err
	}

	rendered, err := m.renderUpdatedPolicy(def, sid, final)
	if err != nil {
		return nil, err
	}

	// 文件已落盘，后续下发不应因客户端断开而中途取消。
	applyCtx, applyCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer applyCancel()
	if err := m.replacePolicy(applyCtx, policyName, rendered, users, groups); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrApplyFailed, err)
	}
	return final, nil
}

// renderUpdatedPolicy 修改模板、原子写回并更新内存文档。
func (m *Manager) renderUpdatedPolicy(def policyDef, sid string, cidrs []string) ([]byte, error) {
	if m.PolicyDir == "" {
		return nil, fmt.Errorf("%w: policy 目录未配置", ErrPolicyRead)
	}
	path := filepath.Join(m.PolicyDir, def.File)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: 读取策略 %s: %w", ErrPolicyRead, path, err)
	}
	updated, err := SetStatementSourceIP(raw, sid, cidrs)
	if err != nil {
		return nil, err
	}
	rendered, err := renderPolicy(path, updated, m.Options)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if err := validatePolicyJSON(rendered); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if err := writeFileAtomic(path, updated); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrPolicyWrite, path, err)
	}
	if err := m.Policies.SetDocument(def.Policy, rendered); err != nil {
		return nil, err
	}
	return rendered, nil
}

// hasPolicy 判断逗号分隔的策略列表是否包含 name。
func hasPolicy(value, name string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == name {
			return true
		}
	}
	return false
}

// collectPolicyAttachments 从用户/组信息中筛出绑定指定策略的实体。
func collectPolicyAttachments(policyName, boundGroup string, users map[string]madmin.UserInfo, groups []string, getGroup func(string) (*madmin.GroupDesc, error)) ([]string, []string, error) {
	var userNames []string
	for name, info := range users {
		if hasPolicy(info.PolicyName, policyName) {
			userNames = append(userNames, name)
		}
	}
	groupNames := []string{boundGroup}
	for _, name := range groups {
		if name == boundGroup {
			continue
		}
		desc, err := getGroup(name)
		if err != nil {
			return nil, nil, fmt.Errorf("读取组 %s: %w", name, err)
		}
		if hasPolicy(desc.Policy, policyName) {
			groupNames = append(groupNames, name)
		}
	}
	return userNames, groupNames, nil
}

// policyAttachments 返回绑定指定策略的用户和组，并保证绑定组在列表中。
func (m *Manager) policyAttachments(ctx context.Context, policyName, group string) ([]string, []string, error) {
	if m.Admin == nil {
		return nil, nil, errors.New("RustFS 管理客户端未初始化")
	}
	allUsers, err := m.Admin.ListUsers(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("列出用户: %w", err)
	}
	allGroups, err := m.Admin.ListGroups(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("列出组: %w", err)
	}
	return collectPolicyAttachments(policyName, group, allUsers, allGroups, func(name string) (*madmin.GroupDesc, error) {
		return m.Admin.GetGroupDescription(ctx, name)
	})
}

// writeFileAtomic 先写同目录临时文件再 rename，避免中途失败留下半个文件。
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func setSourceIPCondition(statement map[string]any, cidrs []string) error {
	condition := map[string]any{}
	if raw, ok := statement["Condition"]; ok && raw != nil {
		condition, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: Condition 必须是对象", ErrInvalidPolicy)
		}
	}
	ipAddress := map[string]any{}
	if raw, ok := condition["IpAddress"]; ok && raw != nil {
		ipAddress, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: IpAddress 必须是对象", ErrInvalidPolicy)
		}
	}
	if len(cidrs) == 0 {
		delete(ipAddress, "aws:SourceIp")
		if len(ipAddress) == 0 {
			delete(condition, "IpAddress")
		}
	} else {
		values := make([]any, len(cidrs))
		for i, cidr := range cidrs {
			values[i] = cidr
		}
		ipAddress["aws:SourceIp"] = values
		condition["IpAddress"] = ipAddress
	}
	if len(condition) == 0 {
		delete(statement, "Condition")
		return nil
	}
	statement["Condition"] = condition
	return nil
}
