package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestBucketErrorClassification(t *testing.T) {
	if !isBucketAlreadyExists(minio.ErrorResponse{Code: "BucketAlreadyOwnedByYou"}) {
		t.Fatal("BucketAlreadyOwnedByYou should match")
	}
	if !isBucketAlreadyExists(errors.New("The bucket already exists")) {
		t.Fatal("already exists message should match")
	}
	if isBucketAlreadyExists(errors.New("Access Denied")) {
		t.Fatal("access denied should not match")
	}
}

func TestIsNotFoundBucket(t *testing.T) {
	if !IsNotFound(minio.ErrorResponse{Code: "NoSuchBucket"}) {
		t.Fatal("NoSuchBucket should be not found")
	}
}

func TestGetRangeIncludesZeroStart(t *testing.T) {
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Type", "application/octet-stream")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-0/1")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	m, err := New(srv.URL, "ak", "sk", "us-east-1", "bucket")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		start, end int64
		wantRange  string
	}{
		{start: 0, end: 0, wantRange: "bytes=0-0"},
		{start: 0, end: -1, wantRange: ""},
	}
	for _, tc := range cases {
		obj, err := m.Get(context.Background(), "k", tc.start, tc.end)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(obj); err != nil {
			t.Fatal(err)
		}
		_ = obj.Close()
		if got := ranges[len(ranges)-1]; got != tc.wantRange {
			t.Fatalf("start=%d end=%d 发送 Range %q，期望 %q", tc.start, tc.end, got, tc.wantRange)
		}
	}
}
