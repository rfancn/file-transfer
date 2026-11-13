package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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
