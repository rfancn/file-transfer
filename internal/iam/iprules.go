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
	"sync"
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
			return nil, fmt.Errorf("%w: %s", ErrInvalidCIDR, value)
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

// 后续任务会在这里补充 SetStatementSourceIP 与模板写回逻辑；先保留占位导入。
var (
	_ = context.Background
	_ = json.Marshal
	_ = os.ReadFile
	_ = filepath.Join
	_ = sync.Mutex{}
)
