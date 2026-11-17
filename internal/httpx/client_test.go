package httpx

import (
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rfancn/file-transfer/internal/config"
)

// newTLSTestServer 启动一个使用自签名证书的测试服务器，并静音握手失败日志。
func newTLSTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	return srv
}

// writeServerCert 把测试服务器的证书写成 PEM 文件，模拟内部 CA。
func writeServerCert(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	if cert == nil {
		t.Fatal("测试服务器没有证书")
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func getWithClient(c *http.Client, url string) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestNewClientTrustsCustomCA(t *testing.T) {
	srv := newTLSTestServer(t)
	defer srv.Close()

	// 基线：默认配置应拒绝自签名证书。
	plain, err := NewClient(config.ClientSection{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := getWithClient(plain, srv.URL); err == nil {
		t.Fatal("默认配置不应信任自签名证书")
	}

	caPath := writeServerCert(t, srv)
	c, err := NewClient(config.ClientSection{URL: srv.URL, CACert: caPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := getWithClient(c, srv.URL); err != nil {
		t.Fatalf("配置 ca_cert 后应能连接: %v", err)
	}
}

func TestNewClientInsecureSkipVerify(t *testing.T) {
	srv := newTLSTestServer(t)
	defer srv.Close()

	c, err := NewClient(config.ClientSection{URL: srv.URL, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := getWithClient(c, srv.URL); err != nil {
		t.Fatalf("insecure_skip_verify 应跳过证书校验: %v", err)
	}
}

func TestNewClientRejectsMissingCACert(t *testing.T) {
	_, err := NewClient(config.ClientSection{
		URL:    "https://ft-server.example.com",
		CACert: filepath.Join(t.TempDir(), "missing.pem"),
	})
	if err == nil || !strings.Contains(err.Error(), "ca_cert") {
		t.Fatalf("ca_cert 文件缺失应报错，得到 %v", err)
	}
}

func TestNewClientRejectsInvalidCACert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient(config.ClientSection{
		URL:    "https://ft-server.example.com",
		CACert: path,
	})
	if err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("无效 ca_cert 应报错，得到 %v", err)
	}
}

func TestNewClientRejectsCACertWithInsecure(t *testing.T) {
	_, err := NewClient(config.ClientSection{
		URL:                "https://ft-server.example.com",
		CACert:             "ca.pem",
		InsecureSkipVerify: true,
	})
	if err == nil || !strings.Contains(err.Error(), "不能同时") {
		t.Fatalf("ca_cert 与 insecure_skip_verify 同时设置应报错，得到 %v", err)
	}
}
