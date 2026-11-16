package client

import (
	"context"
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

	resp.Header.Del("Retry-After")
	if got := retryWait(resp, 3); got < 3*time.Second {
		t.Fatalf("指数退避 = %v，期望至少 3s", got)
	}
}
