package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/limiter"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/server/app"
)

func TestParseRangeHeaderIfRange(t *testing.T) {
	lastModified := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	etag := "d41d8cd98f00b204e9800998ecf8427e"
	cases := []struct {
		name      string
		ifRange   string
		wantStart int64
		wantEnd   int64
		wantPart  bool
	}{
		{name: "带引号的 ETag", ifRange: `"` + etag + `"`, wantStart: 10, wantEnd: 19, wantPart: true},
		{name: "无引号的 ETag", ifRange: etag, wantStart: 10, wantEnd: 19, wantPart: true},
		{name: "不匹配的 ETag", ifRange: `"other"`, wantStart: 0, wantEnd: 99, wantPart: false},
		{name: "弱 ETag", ifRange: `W/"` + etag + `"`, wantStart: 0, wantEnd: 99, wantPart: false},
		{name: "相同修改时间", ifRange: lastModified.Format(http.TimeFormat), wantStart: 10, wantEnd: 19, wantPart: true},
		{name: "对象更新更晚", ifRange: lastModified.Add(-time.Hour).Format(http.TimeFormat), wantStart: 0, wantEnd: 99, wantPart: false},
		{name: "对象未更新", ifRange: lastModified.Add(time.Hour).Format(http.TimeFormat), wantStart: 10, wantEnd: 19, wantPart: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/files/inbound/a.bin", nil)
			req.Header.Set("Range", "bytes=10-19")
			req.Header.Set("If-Range", tc.ifRange)
			start, end, partial, err := parseRangeHeader(req, 100, etag, lastModified)
			if err != nil {
				t.Fatal(err)
			}
			if start != tc.wantStart || end != tc.wantEnd || partial != tc.wantPart {
				t.Fatalf("got (%d,%d,%t), want (%d,%d,%t)", start, end, partial, tc.wantStart, tc.wantEnd, tc.wantPart)
			}
		})
	}
}

func TestFormatETag(t *testing.T) {
	cases := map[string]string{
		"abc":       `"abc"`,
		`"abc"`:     `"abc"`,
		`W/"abc"`:   `W/"abc"`,
		"":          "",
		`  "abc"  `: `"abc"`,
	}
	for in, want := range cases {
		if got := formatETag(in); got != want {
			t.Fatalf("formatETag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEndByteIfPartial(t *testing.T) {
	if got := endByteIfPartial(false, 7); got != -1 {
		t.Fatalf("完整请求应返回 -1，得到 %d", got)
	}
	if got := endByteIfPartial(true, 7); got != 7 {
		t.Fatalf("区间请求应返回结束位置，得到 %d", got)
	}
}

func TestWildcardPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/files/*path", func(c *gin.Context) {
		c.String(http.StatusOK, wildcardPath(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/files/inbound/a%20b/c%23d.txt", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got, want := rr.Body.String(), "inbound/a b/c#d.txt"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestDownloadReturnsBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := config.Runtime{Limits: config.Limits{
		Enabled:              true,
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueueTimeout:         10 * time.Millisecond,
	}}
	lim := limiter.New(rt.Limits)
	defer lim.Close()

	release, err := lim.AcquireTransfer(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	h := &Handler{App: &app.App{Limits: lim, RT: rt}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/files/inbound/a.bin", nil)
	c.Set("username", "alice")
	c.Params = gin.Params{{Key: "path", Value: "/inbound/a.bin"}}

	h.Download(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q，期望 1", got)
	}
	var body protocol.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应体失败: %v", err)
	}
	if body.Error != "server_busy" {
		t.Fatalf("error = %q，期望 server_busy", body.Error)
	}
}

func TestListReturnsBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := config.Runtime{Limits: config.Limits{
		Enabled:              true,
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueueTimeout:         10 * time.Millisecond,
	}}
	lim := limiter.New(rt.Limits)
	defer lim.Close()

	// 占满全部元数据槽。
	for i := 0; i < 8; i++ {
		release, err := lim.AcquireMetadata(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}

	h := &Handler{App: &app.App{Limits: lim, RT: rt}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/files?dir=inbound", nil)
	c.Set("username", "alice")

	h.List(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
}

func TestUploadReturnsBusy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := config.Runtime{Limits: config.Limits{
		Enabled:              true,
		MaxConcurrent:        1,
		MaxConcurrentPerUser: 1,
		QueueTimeout:         10 * time.Millisecond,
	}}
	lim := limiter.New(rt.Limits)
	defer lim.Close()

	release, err := lim.AcquireTransfer(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	h := &Handler{App: &app.App{Limits: lim, RT: rt}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/files/outbound/a.bin", strings.NewReader("x"))
	c.Set("username", "alice")
	c.Params = gin.Params{{Key: "path", Value: "/outbound/a.bin"}}

	h.Upload(c)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", w.Code)
	}
}
