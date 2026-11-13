package iam

import (
	"context"
	"encoding/json"
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

func (m *Manager) SetupUser(ctx context.Context, user, password string) (bool, error) {
	exists, err := m.UserExists(ctx, user)
	if err != nil {
		return false, err
	}
	if !exists {
		if err := m.Admin.AddUser(ctx, user, password); err != nil {
			return false, err
		}
	}
	polName := UserPolicyName(user)
	if err := m.ReplacePolicy(ctx, polName, UserPolicy(m.Bucket, user), []string{user}, nil); err != nil {
		return !exists, err
	}
	if err := m.AddToGroup(ctx, UserGroup, user); err != nil {
		return !exists, err
	}
	if err := m.ReplacePolicy(ctx, UserGroupPol, GroupPolicy(m.Bucket), nil, []string{UserGroup}); err != nil {
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
	pol := info.PolicyName
	if pol == "" {
		pol = UserPolicyName(user)
	}
	_ = m.Detach(ctx, pol, user, "")
	for _, g := range info.MemberOf {
		_ = m.RemoveFromGroup(ctx, g, user)
	}
	if err := m.Admin.RemoveUser(ctx, user); err != nil {
		return true, 0, err
	}
	_ = m.Admin.RemoveCannedPolicy(ctx, pol)
	n, err := m.S3.RemovePrefix(ctx, user+"/")
	return true, n, err
}

func (m *Manager) EnsureServiceIdentities(ctx context.Context, dataUser, dataPass, adminUser, adminPass string) error {
	if err := m.CreateUser(ctx, dataUser, dataPass); err != nil {
		return err
	}
	if err := m.CreateUser(ctx, adminUser, adminPass); err != nil {
		return err
	}
	if err := m.ReplacePolicy(ctx, DataPolicy, ServerDataPolicy(m.Bucket), []string{dataUser}, nil); err != nil {
		return err
	}
	if err := m.ReplacePolicy(ctx, ServerAdminPol, ServerAdminPolicy(m.Bucket), []string{adminUser}, nil); err != nil {
		return err
	}
	return nil
}

func (m *Manager) EnsureAdminConsole(ctx context.Context, password string) (bool, error) {
	exists, err := m.UserExists(ctx, ConsoleAdmin)
	if err != nil {
		return false, err
	}
	if !exists {
		if err := m.Admin.AddUser(ctx, ConsoleAdmin, password); err != nil {
			return false, err
		}
	}
	_ = m.AddToGroup(ctx, AdminGroup, ConsoleAdmin)
	if err := m.ReplacePolicy(ctx, AdminGroupPol, AdminPolicy(m.Bucket), []string{ConsoleAdmin}, []string{AdminGroup}); err != nil {
		return false, err
	}
	return !exists, nil
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

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func policy(actions []string, resource string) map[string]any {
	return map[string]any{"Effect": "Allow", "Action": actions, "Resource": []string{resource}}
}

func GroupPolicy(bucket string) []byte {
	return mustJSON(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			policy([]string{"s3:ListAllMyBuckets"}, "arn:aws:s3:::*"),
			policy([]string{"s3:HeadBucket", "s3:GetBucketLocation", "s3:GetBucketAcl", "s3:GetBucketPolicyStatus", "s3:GetBucketVersioning", "s3:GetBucketObjectLockConfiguration", "s3:GetBucketTagging"}, "arn:aws:s3:::"+bucket),
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{"StringLikeIfExists": map[string]any{"s3:prefix": []string{
					"", "${aws:username}/", "${aws:username}/*", "${aws:username}/inbound/", "${aws:username}/inbound/*",
					"${aws:username}/outbound/", "${aws:username}/outbound/*", "${aws:username}/upload/", "${aws:username}/upload/*",
				}}},
			},
		},
	})
}

func listPrefixStatement(bucket, user string) map[string]any {
	return map[string]any{
		"Effect":   "Allow",
		"Action":   []string{"s3:ListBucket"},
		"Resource": []string{"arn:aws:s3:::" + bucket},
		"Condition": map[string]any{"StringLike": map[string]any{"s3:prefix": []string{
			user + "/", user + "/*", user + "/inbound/", user + "/inbound/*",
			user + "/outbound/", user + "/outbound/*", user + "/upload/", user + "/upload/*",
		}}},
	}
}

func UserPolicy(bucket, user string) []byte {
	getActions := []string{"s3:GetObject", "s3:GetObjectAttributes", "s3:GetObjectTagging", "s3:GetObjectRetention", "s3:GetObjectLegalHold"}
	uploadActions := append(append([]string{}, getActions...), "s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:PutObjectTagging")
	return mustJSON(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			listPrefixStatement(bucket, user),
			policy(getActions, "arn:aws:s3:::"+bucket+"/"+user+"/inbound/*"),
			policy(uploadActions, "arn:aws:s3:::"+bucket+"/"+user+"/upload/*"),
		},
	})
}

func AdminPolicy(bucket string) []byte {
	getActions := []string{"s3:GetObject", "s3:GetObjectAttributes", "s3:GetObjectTagging", "s3:GetObjectRetention", "s3:GetObjectLegalHold"}
	return mustJSON(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			policy([]string{"s3:ListAllMyBuckets"}, "arn:aws:s3:::*"),
			policy([]string{"s3:GetBucketLocation", "s3:ListBucketMultipartUploads", "s3:GetBucketPolicyStatus", "s3:GetBucketPolicy", "s3:GetBucketVersioning", "s3:GetBucketObjectLockConfiguration", "s3:GetBucketTagging", "s3:GetBucketAcl"}, "arn:aws:s3:::"+bucket),
			// 操作员只能按目录浏览 inbound/outbound，不能进入 upload。
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{
					"StringLikeIfExists": map[string]any{"s3:prefix": []string{"", "*/", "*/inbound/", "*/inbound/*", "*/outbound/", "*/outbound/*"}},
					"StringEquals":       map[string]any{"s3:delimiter": "/"},
				},
			},
			// 允许递归列出 inbound 前缀，Console 删除目录时需要枚举全部对象。
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{
					"StringLike": map[string]any{"s3:prefix": []string{"*/inbound/", "*/inbound/*"}},
				},
			},
			// 显式禁止进入 upload 前缀。
			map[string]any{
				"Effect":   "Deny",
				"Action":   []string{"s3:ListBucket"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{
					"StringLike": map[string]any{"s3:prefix": []string{"*/upload/", "*/upload/*"}},
				},
			},
			// 除 inbound 外，禁止不带 delimiter 的递归列表，避免绕过 upload 限制。
			map[string]any{
				"Effect":   "Deny",
				"Action":   []string{"s3:ListBucket"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{
					"Null":          map[string]any{"s3:delimiter": "true"},
					"StringNotLike": map[string]any{"s3:prefix": []string{"*/inbound/", "*/inbound/*"}},
				},
			},
			// 操作员可以写入和删除所有用户 inbound 下的对象。
			policy([]string{
				"s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:PutObjectTagging",
				"s3:DeleteObject", "s3:DeleteObjectVersion", "s3:DeleteObjectTagging",
			}, "arn:aws:s3:::"+bucket+"/*/inbound/*"),
			// 保留 inbound 目录占位，避免删除后目录消失。
			map[string]any{
				"Effect":   "Deny",
				"Action":   []string{"s3:DeleteObject", "s3:DeleteObjectVersion"},
				"Resource": []string{"arn:aws:s3:::" + bucket + "/*/inbound/.keep"},
			},
			policy(getActions, "arn:aws:s3:::"+bucket+"/*/outbound/*"),
			policy([]string{"admin:CreateUser", "admin:AddUserToGroup", "admin:GetUser", "admin:GetGroup", "admin:ListUsers", "admin:ListGroups", "admin:AttachUserOrGroupPolicy", "admin:ListUserPolicies"}, "arn:aws:s3:::*"),
		},
	})
}

func ServerDataPolicy(bucket string) []byte {
	getActions := []string{"s3:GetObject", "s3:GetObjectAttributes", "s3:GetObjectTagging", "s3:GetObjectRetention", "s3:GetObjectLegalHold"}
	return mustJSON(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket"},
				"Resource": []string{"arn:aws:s3:::" + bucket},
				"Condition": map[string]any{"StringLike": map[string]any{"s3:prefix": []string{
					"", "*/", "*/inbound/", "*/inbound/*", "*/outbound/", "*/outbound/*", "*/upload/", "*/upload/*",
				}}},
			},
			policy(getActions, "arn:aws:s3:::"+bucket+"/*/inbound/*"),
			policy(getActions, "arn:aws:s3:::"+bucket+"/*/upload/*"),
			policy([]string{"s3:PutObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts", "s3:PutObjectTagging"}, "arn:aws:s3:::"+bucket+"/*/outbound/*"),
			policy([]string{"s3:PutObject"}, "arn:aws:s3:::"+bucket+"/*/inbound/.keep"),
			policy([]string{"s3:PutObject"}, "arn:aws:s3:::"+bucket+"/*/outbound/.keep"),
			policy([]string{"s3:PutObject"}, "arn:aws:s3:::"+bucket+"/*/upload/.keep"),
			policy([]string{"s3:HeadBucket", "s3:GetBucketLocation"}, "arn:aws:s3:::"+bucket),
		},
	})
}

func ServerAdminPolicy(bucket string) []byte {
	return mustJSON(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			policy([]string{"admin:CreateUser", "admin:DeleteUser", "admin:GetUser", "admin:ListUsers", "admin:AddUserToGroup", "admin:RemoveUserFromGroup", "admin:CreatePolicy", "admin:DeletePolicy", "admin:AttachUserOrGroupPolicy", "admin:ListUserPolicies", "admin:GetGroup", "admin:ListGroups"}, "arn:aws:s3:::*"),
			policy([]string{"s3:ListBucket"}, "arn:aws:s3:::"+bucket),
			policy([]string{"s3:ListBucketMultipartUploads"}, "arn:aws:s3:::"+bucket),
			policy([]string{"s3:ListMultipartUploadParts", "s3:AbortMultipartUpload"}, "arn:aws:s3:::"+bucket+"/*"),
			policy([]string{"s3:DeleteObject"}, "arn:aws:s3:::"+bucket+"/*/inbound/*"),
			policy([]string{"s3:DeleteObject"}, "arn:aws:s3:::"+bucket+"/*/outbound/*"),
			policy([]string{"s3:DeleteObject"}, "arn:aws:s3:::"+bucket+"/*/upload/*"),
			policy([]string{"s3:DeleteObject"}, "arn:aws:s3:::"+bucket+"/*/inbound/"),
			policy([]string{"s3:DeleteObject"}, "arn:aws:s3:::"+bucket+"/*/outbound/"),
			policy([]string{"s3:DeleteObject"}, "arn:aws:s3:::"+bucket+"/*/upload/"),
		},
	})
}

func GeneratePassword(n int) (string, error) { return auth.GeneratePassword(n) }
