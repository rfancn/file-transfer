package iam

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	policyUserGroupFile  = "user-group.json"
	policyOperatorFile   = "operator-group.json"
	policyServerDataFile = "server-data.json"
	policyServerAdmin    = "server-admin.json"

	placeholderBucket = "{{bucket}}"
)

// Policies 保存从 policy 目录加载并渲染后的 IAM 策略。
type Policies struct {
	Group    []byte
	Operator []byte
	Data     []byte
	Admin    []byte
}

// LoadPolicies 从 dir 读取策略文件，替换 {{bucket}} 并校验 JSON。
func LoadPolicies(dir, bucket string) (*Policies, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("policy 目录不能为空")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("bucket 不能为空")
	}

	group, err := loadPolicyFile(dir, policyUserGroupFile, bucket)
	if err != nil {
		return nil, err
	}
	operator, err := loadPolicyFile(dir, policyOperatorFile, bucket)
	if err != nil {
		return nil, err
	}
	data, err := loadPolicyFile(dir, policyServerDataFile, bucket)
	if err != nil {
		return nil, err
	}
	admin, err := loadPolicyFile(dir, policyServerAdmin, bucket)
	if err != nil {
		return nil, err
	}
	return &Policies{
		Group:    group,
		Operator: operator,
		Data:     data,
		Admin:    admin,
	}, nil
}

func loadPolicyFile(dir, name, bucket string) ([]byte, error) {
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取策略 %s: %w", path, err)
	}
	text := strings.ReplaceAll(string(raw), placeholderBucket, bucket)
	if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
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
