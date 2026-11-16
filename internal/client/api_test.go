package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecuteRetriesTooManyRequests(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"server_busy","message":"busy"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d，期望 200", resp.StatusCode)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d，期望 2", attempts)
	}
}

func TestExecuteStopsAfterMaxRetries(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"server_busy","message":"busy"}`)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d，期望 429", resp.StatusCode)
	}
	if attempts != maxRetryAttempts {
		t.Fatalf("attempts = %d，期望 %d", attempts, maxRetryAttempts)
	}
}

func TestExecuteDoesNotRetryUnreplayableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("x")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.execute(req, nil); err == nil || !strings.Contains(err.Error(), "不可重放") {
		t.Fatalf("期望不可重放错误，得到 %v", err)
	}
}

func TestExecuteReplaysBodyOnRetry(t *testing.T) {
	payload := []byte("hello retry")
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		if len(bodies) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"server_busy","message":"busy"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil) // 走 req.GetBody 回退
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d，期望 200", resp.StatusCode)
	}
	if len(bodies) != 2 || bodies[0] != string(payload) || bodies[1] != string(payload) {
		t.Fatalf("请求体重放不正确: %q", bodies)
	}
}

func TestExecuteRetriesServiceUnavailable(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client()}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d，期望 200", resp.StatusCode)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d，期望 2", attempts)
	}
}

func TestSleepContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v，期望 context.Canceled", err)
	}
}

func TestRetryNotice(t *testing.T) {
	var buf bytes.Buffer
	retryNotice(&buf, 5*time.Second)
	if got, want := buf.String(), "服务器繁忙，等待 5 秒后重试\n"; got != want {
		t.Fatalf("notice = %q，期望 %q", got, want)
	}

	buf.Reset()
	retryNotice(&buf, 0)
	if got, want := buf.String(), "服务器繁忙，正在重试\n"; got != want {
		t.Fatalf("notice = %q，期望 %q", got, want)
	}

	buf.Reset()
	retryNotice(&buf, 300*time.Millisecond)
	if got, want := buf.String(), "服务器繁忙，等待 1 秒后重试\n"; got != want {
		t.Fatalf("notice = %q，期望 %q", got, want)
	}

	retryNotice(nil, time.Second) // 不应 panic
}

func TestExecuteShowsRetryNotice(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"server_busy","message":"busy"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	c := &Client{http: srv.Client(), retryLog: &buf}
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.execute(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.Contains(buf.String(), "服务器繁忙") {
		t.Fatalf("未输出重试提示: %q", buf.String())
	}
	if n := strings.Count(buf.String(), "服务器繁忙"); n != 1 {
		t.Fatalf("重试提示出现 %d 次，期望 1 次: %q", n, buf.String())
	}
}

func TestRetryWait(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "7")
	if got := retryWait(resp, 1); got != 7*time.Second {
		t.Fatalf("Retry-After 秒 = %v，期望 7s", got)
	}

	resp.Header.Set("Retry-After", time.Now().Add(2*time.Second).UTC().Format(http.TimeFormat))
	if got := retryWait(resp, 1); got <= 0 || got > 2*time.Second {
		t.Fatalf("Retry-After 日期 = %v", got)
	}

	resp.Header.Set("Retry-After", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))
	if got := retryWait(resp, 1); got != 0 {
		t.Fatalf("过去的 Retry-After 日期 = %v，期望 0", got)
	}

	resp.Header.Del("Retry-After")
	if got := retryWait(resp, 3); got < 3*time.Second {
		t.Fatalf("指数退避 = %v，期望至少 3s", got)
	}
}
