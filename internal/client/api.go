package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	osuser "os/user"
	"strings"
	"sync"
	"time"

	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/protocol"
)

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Message
}

func AsAPIError(err error) (*APIError, bool) {
	e, ok := err.(*APIError)
	return e, ok
}

type Client struct {
	baseURL    string
	http       *http.Client
	username   string
	password   string
	token      string
	tokenExp   time.Time
	policy     protocol.UploadPolicy
	clientUser string
	clientHost string
	mu         sync.Mutex
}

func localClientInfo() (string, string) {
	name := ""
	if u, err := osuser.Current(); err == nil {
		name = u.Username
	}
	if name == "" {
		name = os.Getenv("USER")
	}
	host, _ := os.Hostname()
	return name, host
}

func New(cfg config.ClientConfig) (*Client, error) {
	u, err := url.Parse(cfg.Server.URL)
	if err != nil {
		return nil, fmt.Errorf("server.url 无效: %w", err)
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("server.url 必须是 http://（客户端与服务端在同一局域网，不使用 HTTPS）")
	}
	clientUser, clientHost := localClientInfo()
	return &Client{
		baseURL:    strings.TrimRight(cfg.Server.URL, "/"),
		http:       &http.Client{},
		clientUser: clientUser,
		clientHost: clientHost,
	}, nil
}

func (c *Client) Username() string              { return c.username }
func (c *Client) Policy() protocol.UploadPolicy { return c.policy }

func (c *Client) Login(ctx context.Context, username, password string) error {
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/sessions", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return parseError(resp)
	}
	var out protocol.SessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	c.mu.Lock()
	c.username = username
	c.password = password
	c.token = out.Token
	c.tokenExp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	c.policy = out.UploadPolicy
	c.mu.Unlock()
	return nil
}

func (c *Client) Register(ctx context.Context, username, password string) (*protocol.RegisterResponse, error) {
	body := map[string]string{"username": username}
	if password != "" {
		body["password"] = password
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/users", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, parseError(resp)
	}
	var out protocol.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) List(ctx context.Context, dir string) ([]protocol.FileInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/files", url.Values{"dir": {dir}}, nil, 0, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, parseError(resp)
	}
	var out protocol.ListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, size int64, contentType string) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, query, body, size, contentType)
	if err != nil {
		return nil, err
	}
	return c.execute(req, req.GetBody)
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body io.Reader, size int64, contentType string) (*http.Request, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.clientUser != "" {
		req.Header.Set("X-Client-User", c.clientUser)
	}
	if c.clientHost != "" {
		req.Header.Set("X-Client-Host", c.clientHost)
	}
	return req, nil
}

func (c *Client) execute(req *http.Request, getBody func() (io.ReadCloser, error)) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	// token 失效时用内存中的密码自动重登并重试一次。
	c.mu.Lock()
	username, password := c.username, c.password
	c.mu.Unlock()
	if username == "" || password == "" {
		return resp, nil
	}
	resp.Body.Close()
	if err := c.Login(req.Context(), username, password); err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	retry.Header.Set("Authorization", "Bearer "+c.token)
	if getBody != nil {
		rc, err := getBody()
		if err != nil {
			return nil, err
		}
		retry.Body = rc
	} else if req.Body != nil {
		return nil, fmt.Errorf("请求体不可重放，请重试")
	}
	return c.http.Do(retry)
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body io.Reader, size int64, contentType string, out any) error {
	resp, err := c.do(ctx, method, path, query, body, size, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return parseError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func parseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e protocol.ErrorResponse
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		return &APIError{Status: resp.StatusCode, Code: e.Error, Message: e.Message}
	}
	return &APIError{Status: resp.StatusCode, Code: "http_error", Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(data))}
}
