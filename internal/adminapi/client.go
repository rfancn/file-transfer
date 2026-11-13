package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rfancn/file-transfer/internal/protocol"
)

// APIError 是 ft-server 管理接口返回的错误。
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func AsAPIError(err error) (*APIError, bool) {
	e, ok := err.(*APIError)
	return e, ok
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL string) (*Client, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("server.url 不能为空")
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{},
	}, nil
}

func (c *Client) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// Login 用管理员用户名和密码换取管理接口 token。
func (c *Client) Login(ctx context.Context, username, password string) error {
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/admin/login", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var out protocol.AdminLoginResponse
	if err := c.doJSON(req, &out); err != nil {
		return err
	}
	c.token = out.Token
	return nil
}

// Init 把 RustFS 管理凭据交给 ft-server 执行初始化。
func (c *Client) Init(ctx context.Context, accessKey, secretKey string) error {
	payload, err := json.Marshal(map[string]string{
		"access_key": accessKey,
		"secret_key": secretKey,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/admin/bootstrap", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	return c.do(req)
}

func (c *Client) DeleteUser(ctx context.Context, username string) (int, error) {
	u := c.baseURL + "/v1/admin/users/" + url.PathEscape(username)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return 0, err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, responseError(resp)
	}
	var out struct {
		Deleted bool `json:"deleted"`
		Objects int  `json:"objects"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Objects, nil
}

// AddUser 请求 ft-server 创建用户并返回一次性密码。
func (c *Client) AddUser(ctx context.Context, username string) (protocol.AddUserResponse, error) {
	var out protocol.AddUserResponse
	payload, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/admin/users", bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	if err := c.doJSON(req, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ResetPassword 请求 ft-server 为指定用户生成并设置新的 8 位密码。
func (c *Client) ResetPassword(ctx context.Context, username string) (protocol.AddUserResponse, error) {
	var out protocol.AddUserResponse
	u := c.baseURL + "/v1/admin/users/" + url.PathEscape(username) + "/password"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return out, err
	}
	c.setAuth(req)
	if err := c.doJSON(req, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ListUsers 请求 ft-server 返回用户列表。
func (c *Client) ListUsers(ctx context.Context) ([]protocol.AdminUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/admin/users", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	var out protocol.ListUsersResponse
	if err := c.doJSON(req, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// CleanResult 是服务端清理接口的返回结果。
type CleanResult struct {
	Count  int      `json:"count"`
	Items  []string `json:"items"`
	DryRun bool     `json:"dry_run"`
}

// Clean 请求 ft-server 清理指定目录或未完成分片。
func (c *Client) Clean(ctx context.Context, dir string, days int, dryRun bool) (CleanResult, error) {
	var out CleanResult
	query := url.Values{"dry_run": {strconv.FormatBool(dryRun)}}
	if days > 0 {
		query.Set("days", strconv.Itoa(days))
	}
	u := c.baseURL + "/v1/admin/data/" + url.PathEscape(dir) + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return out, err
	}
	c.setAuth(req)
	if err := c.doJSON(req, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) do(req *http.Request) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return responseError(resp)
	}
	return nil
}

func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return responseError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func responseError(resp *http.Response) error {
	var e protocol.ErrorResponse
	if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
		return &APIError{Status: resp.StatusCode, Code: e.Error, Message: e.Message}
	}
	return &APIError{Status: resp.StatusCode, Code: "http_error", Message: fmt.Sprintf("HTTP %d", resp.StatusCode)}
}
