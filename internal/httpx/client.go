// Package httpx 提供按客户端配置构建 HTTP 客户端的能力。
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/rfancn/file-transfer/internal/config"
)

// NewClient 根据 TLS 配置构建 HTTP 客户端；未配置 TLS 选项时使用默认客户端。
func NewClient(server config.ClientSection) (*http.Client, error) {
	if server.CACert != "" && server.InsecureSkipVerify {
		return nil, errors.New("server.ca_cert 与 server.insecure_skip_verify 不能同时设置")
	}
	if server.CACert == "" && !server.InsecureSkipVerify {
		return &http.Client{}, nil
	}

	// 克隆默认 Transport 以保留代理、连接池等默认行为。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg := &tls.Config{}
	if server.InsecureSkipVerify {
		// #nosec G402 -- 用户显式开启，仅用于自签名证书的调试场景。
		tlsCfg.InsecureSkipVerify = true
	} else {
		data, err := os.ReadFile(server.CACert)
		if err != nil {
			return nil, fmt.Errorf("读取 server.ca_cert 失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("server.ca_cert %s 未包含有效的 PEM 证书", server.CACert)
		}
		tlsCfg.RootCAs = pool
	}
	transport.TLSClientConfig = tlsCfg
	return &http.Client{Transport: transport}, nil
}
