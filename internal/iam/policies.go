package iam

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rfancn/file-transfer/internal/auth"
)

const (
	policyUserGroupFile  = "user-group.json"
	policyUserTemplate   = "user-template.json"
	policyOperatorFile   = "operator-group.json"
	policyServerDataFile = "server-data.json"
	policyServerAdmin    = "server-admin.json"

	placeholderBucket = "{{bucket}}"
	placeholderUser   = "{{user}}"
)

// Policies 保存从 policy 目录加载并渲染后的 IAM 策略。
// UserTemplate 仍保留 {{user}} 占位符，在创建用户时渲染。
type Policies struct {
	Group        []byte
	UserTemplate []byte
	Operator     []byte
	Data         []byte
	Admin        []byte
}

// LoadPolicies 从 dir 读取策略文件，替换 {{bucket}} 并校验 JSON。
func LoadPolicies(dir, bucket string) (*Policies, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("policy 目录不能为空")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("bucket 不能为空")
	}

	group, err := loadPolicyFile(dir, policyUserGroupFile, bucket, false)
	if err != nil {
		return nil, err
	}
	userTemplate, err := loadPolicyFile(dir, policyUserTemplate, bucket, true)
	if err != nil {
		return nil, err
	}
	operator, err := loadPolicyFile(dir, policyOperatorFile, bucket, false)
	if err != nil {
		return nil, err
	}
	data, err := loadPolicyFile(dir, policyServerDataFile, bucket, false)
	if err != nil {
		return nil, err
	}
	admin, err := loadPolicyFile(dir, policyServerAdmin, bucket, false)
	if err != nil {
		return nil, err
	}
	return &Policies{
		Group:        group,
		UserTemplate: userTemplate,
		Operator:     operator,
		Data:         data,
		Admin:        admin,
	}, nil
}

// UserPolicy 用用户名渲染每用户策略模板。
func (p *Policies) UserPolicy(user string) ([]byte, error) {
	if p == nil || len(p.UserTemplate) == 0 {
		return nil, errors.New("用户策略模板未加载")
	}
	if err := auth.ValidateUsername(user); err != nil {
		return nil, fmt.Errorf("用户名无效: %w", err)
	}
	raw := strings.ReplaceAll(string(p.UserTemplate), placeholderUser, user)
	if strings.Contains(raw, "{{") || strings.Contains(raw, "}}") {
		return nil, fmt.Errorf("用户策略模板包含未知占位符: %s", policyUserTemplate)
	}
	if err := validatePolicyJSON([]byte(raw)); err != nil {
		return nil, fmt.Errorf("用户策略 JSON 无效: %w", err)
	}
	return []byte(raw), nil
}

func loadPolicyFile(dir, name, bucket string, allowUser bool) ([]byte, error) {
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取策略 %s: %w", path, err)
	}
	text := strings.ReplaceAll(string(raw), placeholderBucket, bucket)
	if allowUser {
		if !strings.Contains(text, placeholderUser) {
			return nil, fmt.Errorf("策略 %s 缺少 %s 占位符", path, placeholderUser)
		}
		// 移除合法的 {{user}} 后再检查是否还有未知占位符。
		check := strings.ReplaceAll(text, placeholderUser, "")
		if strings.Contains(check, "{{") || strings.Contains(check, "}}") {
			return nil, fmt.Errorf("策略 %s 包含未知占位符", path)
		}
	} else if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
		return nil, fmt.Errorf("策略 %s 包含未知占位符", path)
	}
	if err := validatePolicyJSON([]byte(text)); err != nil {
		return nil, fmt.Errorf("策略 %s JSON 无效: %w", path, err)
	}
	return []byte(text), nil
}

func validatePolicyJSON(raw []byte) error {
	var doc struct {
		Version   string            `json:"Version"`
		Statement []json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if doc.Version == "" {
		return errors.New("缺少 Version")
	}
	if len(doc.Statement) == 0 {
		return errors.New("Statement 不能为空")
	}
	return nil
}
