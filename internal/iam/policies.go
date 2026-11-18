package iam

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	policyUserGroupFile      = "user-group.json"
	policyOperatorFile       = "operator-group.json"
	policySharedOperatorFile = "shared-operator-group.json"
	policyServerDataFile     = "server-data.json"
	policyServerAdmin        = "server-admin.json"
)

// PolicyOptions 是策略模板渲染所需的桶和目录配置。
type PolicyOptions struct {
	HomeBucket   string
	SharedBucket string
	SharedDirs   []string
}

// Policies 保存从 policy 目录加载的 IAM 策略。
// mu 保护 Group/Operator/SharedOperator 的运行时读写；Data/Admin 在 LoadPolicies 后只读。
// 调用方应通过 Document/SetDocument 访问三个组策略，不要直接写这些字段。
type Policies struct {
	mu             sync.RWMutex
	Group          []byte
	Operator       []byte
	SharedOperator []byte
	Data           []byte
	Admin          []byte
}

// Document 返回指定组策略当前的渲染文档副本。
func (p *Policies) Document(name string) ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	switch name {
	case UserGroupPol:
		return cloneBytes(p.Group), nil
	case AdminGroupPol:
		return cloneBytes(p.Operator), nil
	case SharedAdminPol:
		return cloneBytes(p.SharedOperator), nil
	default:
		return nil, ErrUnsupportedPolicy
	}
}

// SetDocument 更新指定组策略的渲染文档。
// 调用方需保证 doc 已渲染且 JSON 有效。
func (p *Policies) SetDocument(name string, doc []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch name {
	case UserGroupPol:
		p.Group = cloneBytes(doc)
	case AdminGroupPol:
		p.Operator = cloneBytes(doc)
	case SharedAdminPol:
		p.SharedOperator = cloneBytes(doc)
	default:
		return ErrUnsupportedPolicy
	}
	return nil
}

func cloneBytes(b []byte) []byte {
	return append([]byte(nil), b...)
}

// LoadPolicies 从 dir 读取策略文件，按 opts 渲染占位符并校验 JSON。
func LoadPolicies(dir string, opts PolicyOptions) (*Policies, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("policy 目录不能为空")
	}
	if strings.TrimSpace(opts.HomeBucket) == "" || strings.TrimSpace(opts.SharedBucket) == "" {
		return nil, errors.New("策略渲染需要 home/shared 桶名")
	}

	group, err := loadPolicyFile(dir, policyUserGroupFile, opts)
	if err != nil {
		return nil, err
	}
	operator, err := loadPolicyFile(dir, policyOperatorFile, opts)
	if err != nil {
		return nil, err
	}
	sharedOperator, err := loadPolicyFile(dir, policySharedOperatorFile, opts)
	if err != nil {
		return nil, err
	}
	data, err := loadPolicyFile(dir, policyServerDataFile, opts)
	if err != nil {
		return nil, err
	}
	admin, err := loadPolicyFile(dir, policyServerAdmin, opts)
	if err != nil {
		return nil, err
	}
	return &Policies{
		Group:          group,
		Operator:       operator,
		SharedOperator: sharedOperator,
		Data:           data,
		Admin:          admin,
	}, nil
}

func loadPolicyFile(dir, name string, opts PolicyOptions) ([]byte, error) {
	path := filepath.Join(dir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取策略 %s: %w", path, err)
	}
	rendered, err := renderPolicy(path, raw, opts)
	if err != nil {
		return nil, err
	}
	if err := validatePolicyJSON(rendered); err != nil {
		return nil, fmt.Errorf("策略 %s JSON 无效: %w", path, err)
	}
	return rendered, nil
}

// renderPolicy 替换策略模板中的桶名和共享目录占位符。
// 目录占位符必须作为独立的 JSON 字符串元素使用，替换结果自带引号。
func renderPolicy(path string, raw []byte, opts PolicyOptions) ([]byte, error) {
	text := string(raw)
	if (strings.Contains(text, "{{SHARED_DIR_PREFIXES}}") || strings.Contains(text, "{{SHARED_DIR_OBJECT_ARNS}}")) && len(opts.SharedDirs) == 0 {
		return nil, fmt.Errorf("策略 %s 需要至少一个共享目录", path)
	}

	prefixes := make([]string, 0, len(opts.SharedDirs)*2)
	arns := make([]string, 0, len(opts.SharedDirs))
	for _, dir := range opts.SharedDirs {
		prefixes = append(prefixes, strconv.Quote(dir+"/"), strconv.Quote(dir+"/*"))
		arns = append(arns, strconv.Quote("arn:aws:s3:::"+opts.SharedBucket+"/"+dir+"/*"))
	}
	replacer := strings.NewReplacer(
		`"{{SHARED_DIR_PREFIXES}}"`, strings.Join(prefixes, ", "),
		`"{{SHARED_DIR_OBJECT_ARNS}}"`, strings.Join(arns, ", "),
		"{{HOME_BUCKET}}", opts.HomeBucket,
		"{{SHARED_BUCKET}}", opts.SharedBucket,
	)
	rendered := replacer.Replace(text)
	if strings.Contains(rendered, "{{") || strings.Contains(rendered, "}}") {
		return nil, fmt.Errorf("策略 %s 包含未知占位符", path)
	}
	return []byte(rendered), nil
}

// RenderPolicy 渲染策略模板，供运行时修改策略后重新生成内存文档。
func RenderPolicy(path string, raw []byte, opts PolicyOptions) ([]byte, error) {
	return renderPolicy(path, raw, opts)
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
