package iam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

const maxCIDRs = 100

var (
	ErrUnsupportedPolicy = errors.New("策略不支持 IP 白名单设置")
	ErrServicePolicy     = errors.New("服务身份策略不允许修改")
	ErrSIDNotFound       = errors.New("策略中不存在指定 Sid")
	ErrStatementNotAllow = errors.New("指定 Sid 不是 Allow 语句")
	ErrInvalidCIDR       = errors.New("无效的 IP 网段")
	ErrInvalidPolicy     = errors.New("策略内容无效")
	ErrPolicyWrite       = errors.New("写入策略文件失败")
)

// editablePolicies 是允许设置 IP 白名单的策略到模板文件和绑定组的映射。
var editablePolicies = map[string]struct {
	file  string
	group string
}{
	UserGroupPol:   {policyUserGroupFile, UserGroup},
	AdminGroupPol:  {policyOperatorFile, AdminGroup},
	SharedAdminPol: {policySharedOperatorFile, SharedAdminGroup},
}

// EditablePolicies 返回允许设置 IP 白名单的策略名。
func EditablePolicies() []string {
	return []string{UserGroupPol, AdminGroupPol, SharedAdminPol}
}

// IsEditablePolicy 返回 policyName 是否允许设置 IP 白名单。
func IsEditablePolicy(policyName string) bool {
	_, ok := editablePolicies[policyName]
	return ok
}

// IsServicePolicy 返回 policyName 是否为服务身份策略。
func IsServicePolicy(policyName string) bool {
	return policyName == DataPolicy || policyName == ServerAdminPol
}

// NormalizeCIDRs 校验并规范化网段列表，去掉重复项。
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
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidCIDR, value)
		}
		normalized := prefix.Masked().String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

// SetStatementSourceIP 为指定 Sid 的 Allow 语句设置或清除 aws:SourceIp 条件。
// cidrs 为空时清除条件。
func SetStatementSourceIP(policy []byte, sid string, cidrs []string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(policy, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	rawStatements, ok := doc["Statement"].([]any)
	if !ok {
		return nil, fmt.Errorf("%w: Statement 必须是数组", ErrInvalidPolicy)
	}
	matches := 0
	for _, raw := range rawStatements {
		statement, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: Statement 项必须是对象", ErrInvalidPolicy)
		}
		statementSID, _ := statement["Sid"].(string)
		if statementSID != sid {
			continue
		}
		matches++
		effect, _ := statement["Effect"].(string)
		if !strings.EqualFold(effect, "Allow") {
			return nil, ErrStatementNotAllow
		}
		if err := setSourceIPCondition(statement, cidrs); err != nil {
			return nil, err
		}
	}
	if matches == 0 {
		return nil, ErrSIDNotFound
	}
	if matches > 1 {
		return nil, fmt.Errorf("%w: 重复 Sid %q", ErrInvalidPolicy, sid)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	return append(out, '\n'), nil
}

func setSourceIPCondition(statement map[string]any, cidrs []string) error {
	condition := map[string]any{}
	if raw, ok := statement["Condition"]; ok {
		condition, ok = raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: Condition 必须是对象", ErrInvalidPolicy)
		}
	}
	ipAddress := map[string]any{}
	if raw, ok := condition["IpAddress"]; ok {
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
