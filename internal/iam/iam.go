package iam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/madmin-go/v3"
	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/s3"
)

const (
	UserGroup    = "ft-user-group"
	UserGroupPol = "ft-user-policy"
	// AdminGroup 是工作网 Console 操作员组。
	AdminGroup = "ft-operator-group"
	// AdminGroupPol 是工作网 Console 操作员的组策略。
	AdminGroupPol = "ft-operator-policy"
	// DataUser 是 ft-server 的数据面服务身份，用于代理文件列表、下载和上传。
	DataUser = "ft-data"
	// DataPolicy 是数据面服务身份的策略。
	DataPolicy = "ft-data-policy"
	// ServerAdmin 是 ft-server 的管理面服务身份，用于用户/策略管理和数据清理。
	ServerAdmin = "ft-admin"
	// ServerAdminPol 是管理面服务身份的策略。
	ServerAdminPol = "ft-admin-policy"
	// ConsoleAdmin 是文件摆渡系统操作员账号，用于工作网 Console/Cyberduck。
	ConsoleAdmin = "ft-operator"
)

// systemUsers 是系统内置账号，不允许通过管理接口删除或重置密码。
// 同时包含旧版本和离线安装脚本可能创建的服务账号。
var systemUsers = map[string]struct{}{
	DataUser:          {},
	ServerAdmin:       {},
	ConsoleAdmin:      {},
	"ft-server-data":  {},
	"ft-server-admin": {},
	"ft-copy-out":     {},
	"ft-download-svc": {},
	"ft-copyout-svc":  {},
}

// IsSystemUser 返回用户名是否为系统内置账号。
func IsSystemUser(name string) bool {
	_, ok := systemUsers[name]
	return ok
}

type Manager struct {
	Admin         *madmin.AdminClient
	S3            *s3.Manager
	PlaceholderS3 *s3.Manager
	Policies      *Policies
	Bucket        string
}

func New(endpoint, ak, sk, region, bucket string) (*Manager, error) {
	host, secure := splitEndpoint(endpoint)
	adm, err := madmin.New(host, ak, sk, secure)
	if err != nil {
		return nil, err
	}
	sm, err := s3.New(endpoint, ak, sk, region, bucket)
	if err != nil {
		return nil, err
	}
	return &Manager{Admin: adm, S3: sm, Bucket: bucket}, nil
}

func splitEndpoint(endpoint string) (string, bool) {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host, u.Scheme == "https"
	}
	return endpoint, strings.HasPrefix(endpoint, "https://")
}

func UserPolicyName(user string) string { return "ft-user-" + user + "-policy" }

func (m *Manager) UserExists(ctx context.Context, name string) (bool, error) {
	_, err := m.Admin.GetUserInfo(ctx, name)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "does not exist") || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return false, nil
	}
	return false, err
}

func (m *Manager) GroupExists(ctx context.Context, name string) (bool, error) {
	_, err := m.Admin.GetGroupDescription(ctx, name)
	if err == nil {
		return true, nil
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "does not exist") || strings.Contains(low, "not found") {
		return false, nil
	}
	return false, err
}

func (m *Manager) EnsurePolicy(ctx context.Context, name string, policy []byte) error {
	err := m.Admin.AddCannedPolicy(ctx, name, policy)
	if err == nil {
		return nil
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "already exists") || strings.Contains(low, "exists") {
		return nil
	}
	return err
}

func (m *Manager) ReplacePolicy(ctx context.Context, name string, policy []byte, users, groups []string) error {
	existing, err := m.Admin.InfoCannedPolicy(ctx, name)
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "access denied") || strings.Contains(low, "not allowed") || strings.Contains(low, "does not exist") || strings.Contains(low, "not found") {
			if err := m.EnsurePolicy(ctx, name, policy); err != nil {
				return err
			}
			for _, u := range users {
				if err := m.Attach(ctx, name, u, ""); err != nil {
					return err
				}
			}
			for _, g := range groups {
				if err := m.Attach(ctx, name, "", g); err != nil {
					return err
				}
			}
			return nil
		}
		return err
	}
	if err == nil {
		var a, b any
		if json.Unmarshal(existing, &a) == nil && json.Unmarshal(policy, &b) == nil {
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if string(ja) == string(jb) {
				for _, u := range users {
					if err := m.Attach(ctx, name, u, ""); err != nil {
						return err
					}
				}
				for _, g := range groups {
					if err := m.Attach(ctx, name, "", g); err != nil {
						return err
					}
				}
				return nil
			}
		}
		for _, u := range users {
			_ = m.Detach(ctx, name, u, "")
		}
		for _, g := range groups {
			_ = m.Detach(ctx, name, "", g)
		}
		if err := m.Admin.RemoveCannedPolicy(ctx, name); err != nil {
			// 旧版本可能把同名策略绑定在未传入的用户/组上；
			// 先解除所有关联，再重试删除。
			if !strings.Contains(strings.ToLower(err.Error()), "in use") {
				return err
			}
			if err := m.detachPolicyFromAll(ctx, name); err != nil {
				return err
			}
			if err := m.Admin.RemoveCannedPolicy(ctx, name); err != nil {
				return err
			}
		}
	}
	if err := m.Admin.AddCannedPolicy(ctx, name, policy); err != nil {
		return err
	}
	for _, u := range users {
		if err := m.Attach(ctx, name, u, ""); err != nil {
			return err
		}
	}
	for _, g := range groups {
		if err := m.Attach(ctx, name, "", g); err != nil {
			return err
		}
	}
	return nil
}

// detachPolicyFromAll 解除策略在所有用户和组上的关联。
// 用于清理旧版本遗留的绑定，避免替换策略时出现 policy in use。
func (m *Manager) detachPolicyFromAll(ctx context.Context, name string) error {
	users, err := m.Admin.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("列出用户: %w", err)
	}
	for user, info := range users {
		if info.PolicyName == name {
			_ = m.Detach(ctx, name, user, "")
		}
	}

	groups, err := m.Admin.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("列出组: %w", err)
	}
	// 不逐一查询组策略，直接对所有组尝试解绑；未绑定时忽略错误即可。
	for _, group := range groups {
		_ = m.Detach(ctx, name, "", group)
	}
	return nil
}

func (m *Manager) Attach(ctx context.Context, policy, user, group string) error {
	_, err := m.Admin.AttachPolicy(ctx, madmin.PolicyAssociationReq{Policies: []string{policy}, User: user, Group: group})
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "already") {
		return nil
	}
	return err
}

func (m *Manager) Detach(ctx context.Context, policy, user, group string) error {
	_, err := m.Admin.DetachPolicy(ctx, madmin.PolicyAssociationReq{Policies: []string{policy}, User: user, Group: group})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not") {
		return nil
	}
	return err
}

func (m *Manager) AddToGroup(ctx context.Context, group, user string) error {
	err := m.Admin.UpdateGroupMembers(ctx, madmin.GroupAddRemove{Group: group, Members: []string{user}, Status: madmin.GroupEnabled})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already") {
		return nil
	}
	return err
}

func (m *Manager) RemoveFromGroup(ctx context.Context, group, user string) error {
	err := m.Admin.UpdateGroupMembers(ctx, madmin.GroupAddRemove{Group: group, Members: []string{user}, Status: madmin.GroupEnabled, IsRemove: true})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not") {
		return nil
	}
	return err
}

// ResetUserPassword 重置已存在用户的密码。
func (m *Manager) ResetUserPassword(ctx context.Context, user, password string) error {
	return m.Admin.SetUser(ctx, user, password, madmin.AccountEnabled)
}

func (m *Manager) CreateUser(ctx context.Context, user, password string) error {
	ok, err := m.UserExists(ctx, user)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return m.Admin.AddUser(ctx, user, password)
}

func (m *Manager) CreatePlaceholders(ctx context.Context, user string) error {
	sm := m.S3
	if m.PlaceholderS3 != nil {
		sm = m.PlaceholderS3
	}
	for _, d := range []string{"inbound", "outbound", "upload"} {
		if err := sm.PutPlaceholder(ctx, user+"/"+d+"/.keep"); err != nil {
			return fmt.Errorf("创建 %s/%s/ 占位失败: %w", user, d, err)
		}
	}
	return nil
}

func (m *Manager) DirExists(ctx context.Context, user, dir string) (bool, error) {
	return m.S3.PrefixExists(ctx, user+"/"+dir+"/")
}

func (m *Manager) SetupUser(ctx context.Context, user, password, group string) (bool, error) {
	if m.Policies == nil {
		return false, errors.New("IAM 策略未加载")
	}
	if group == "" {
		group = UserGroup
	}
	if group != UserGroup && group != AdminGroup {
		return false, fmt.Errorf("不支持的组: %s", group)
	}
	exists, err := m.UserExists(ctx, user)
	if err != nil {
		return false, err
	}
	if !exists {
		if err := m.Admin.AddUser(ctx, user, password); err != nil {
			return false, err
		}
	}
	if err := m.AddToGroup(ctx, group, user); err != nil {
		return !exists, err
	}
	if group == AdminGroup {
		if err := m.ReplacePolicy(ctx, AdminGroupPol, m.Policies.Operator, nil, []string{AdminGroup}); err != nil {
			return !exists, err
		}
		return !exists, nil
	}
	if err := m.ReplacePolicy(ctx, UserGroupPol, m.Policies.Group, nil, []string{UserGroup}); err != nil {
		return !exists, err
	}
	if err := m.CreatePlaceholders(ctx, user); err != nil {
		return !exists, err
	}
	return !exists, nil
}

func (m *Manager) DeleteUser(ctx context.Context, user string) (bool, int, error) {
	info, err := m.Admin.GetUserInfo(ctx, user)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return false, 0, nil
		}
		return false, 0, err
	}
	// 先清理数据；失败时保留用户，避免同名重建后继承旧数据。
	n, err := m.S3.RemovePrefix(ctx, user+"/")
	if err != nil {
		return false, n, fmt.Errorf("清理用户数据 %s: %w", user, err)
	}
	oldPolicy := UserPolicyName(user)
	_ = m.Detach(ctx, oldPolicy, user, "")
	for _, g := range info.MemberOf {
		_ = m.RemoveFromGroup(ctx, g, user)
	}
	if err := m.Admin.RemoveUser(ctx, user); err != nil {
		return true, n, err
	}
	_ = m.Admin.RemoveCannedPolicy(ctx, oldPolicy)
	return true, n, nil
}

func (m *Manager) EnsureServiceIdentities(ctx context.Context, dataUser, dataPass, adminUser, adminPass string) error {
	if m.Policies == nil {
		return errors.New("IAM 策略未加载")
	}
	if err := m.CreateUser(ctx, dataUser, dataPass); err != nil {
		return err
	}
	if err := m.CreateUser(ctx, adminUser, adminPass); err != nil {
		return err
	}
	if err := m.ReplacePolicy(ctx, DataPolicy, m.Policies.Data, []string{dataUser}, nil); err != nil {
		return err
	}
	if err := m.ReplacePolicy(ctx, ServerAdminPol, m.Policies.Admin, []string{adminUser}, nil); err != nil {
		return err
	}
	return nil
}

// EnsureGroup 保证组存在。RustFS 允许通过空的成员列表创建空组。
func (m *Manager) EnsureGroup(ctx context.Context, group string) error {
	exists, err := m.GroupExists(ctx, group)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return m.Admin.UpdateGroupMembers(ctx, madmin.GroupAddRemove{
		Group: group, Members: []string{}, Status: madmin.GroupEnabled,
	})
}

// EnsureOperatorGroup 创建操作员组并绑定 operator 策略，不创建默认操作员用户。
func (m *Manager) EnsureOperatorGroup(ctx context.Context) error {
	if m.Policies == nil {
		return errors.New("IAM 策略未加载")
	}
	if err := m.EnsureGroup(ctx, AdminGroup); err != nil {
		return err
	}
	return m.ReplacePolicy(ctx, AdminGroupPol, m.Policies.Operator, nil, []string{AdminGroup})
}

func (m *Manager) ListUsers(ctx context.Context) (map[string]madmin.UserInfo, error) {
	return m.Admin.ListUsers(ctx)
}

func (m *Manager) RemoveIncomplete(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	count := 0
	for info := range m.S3.IncompleteUploads(ctx) {
		if info.Err != nil {
			return count, info.Err
		}
		if !info.Initiated.Before(cutoff) {
			continue
		}
		if err := m.S3.AbortMultipart(ctx, info.Key, info.UploadID); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func GeneratePassword(n int) (string, error) { return auth.GeneratePassword(n) }
