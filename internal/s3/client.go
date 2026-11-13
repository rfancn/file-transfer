package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/rfancn/file-transfer/internal/protocol"
)

type Manager struct {
	Client *minio.Client
	Core   minio.Core
	Bucket string
}

func New(endpoint, ak, sk, region string, bucket string) (*Manager, error) {
	host := endpoint
	secure := false
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
		secure = u.Scheme == "https"
	}
	if region == "" {
		region = "us-east-1"
	}
	c, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(ak, sk, ""),
		Secure: secure,
		Region: region,
	})
	if err != nil {
		return nil, err
	}
	return &Manager{Client: c, Core: minio.Core{Client: c}, Bucket: bucket}, nil
}

// EnsureBucket 保证桶存在。BucketExists 失败时不影响后续创建尝试；
// 创建时已存在则视为成功。
func (m *Manager) EnsureBucket(ctx context.Context) error {
	if ok, _ := m.Client.BucketExists(ctx, m.Bucket); ok {
		return nil
	}
	if err := m.Client.MakeBucket(ctx, m.Bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
		if isBucketAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

func isBucketAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	code := minio.ToErrorResponse(err).Code
	if code == "BucketAlreadyOwnedByYou" || code == "BucketAlreadyExists" {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "already exists") || strings.Contains(low, "already owned")
}

func (m *Manager) PutPlaceholder(ctx context.Context, key string) error {
	_, err := m.Client.PutObject(ctx, m.Bucket, key, bytes.NewReader(nil), 0, minio.PutObjectOptions{ContentType: "application/x-directory"})
	return err
}

func (m *Manager) Exists(ctx context.Context, key string) (bool, error) {
	_, err := m.Client.StatObject(ctx, m.Bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.StatusCode == 404 {
		return false, nil
	}
	return false, err
}

func (m *Manager) PrefixExists(ctx context.Context, prefix string) (bool, error) {
	ch := m.Client.ListObjects(ctx, m.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, MaxKeys: 1})
	for obj := range ch {
		if obj.Err != nil {
			return false, obj.Err
		}
		return true, nil
	}
	return false, nil
}

func (m *Manager) List(ctx context.Context, prefix string) ([]protocol.FileInfo, error) {
	ch := m.Client.ListObjects(ctx, m.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	var out []protocol.FileInfo
	for obj := range ch {
		if obj.Err != nil {
			return nil, obj.Err
		}
		if strings.HasSuffix(obj.Key, "/") || strings.HasSuffix(obj.Key, "/.keep") {
			continue
		}
		out = append(out, protocol.FileInfo{
			Path:         strings.TrimPrefix(obj.Key, prefix),
			Size:         obj.Size,
			LastModified: obj.LastModified,
		})
	}
	return out, nil
}

func (m *Manager) Stat(ctx context.Context, key string) (minio.ObjectInfo, error) {
	return m.Client.StatObject(ctx, m.Bucket, key, minio.StatObjectOptions{})
}

// Get 返回对象读取器。end < 0 表示读取完整对象；否则按 [start, end] 读取。
func (m *Manager) Get(ctx context.Context, key string, start, end int64) (*minio.Object, error) {
	opts := minio.GetObjectOptions{}
	if end >= 0 {
		if err := opts.SetRange(start, end); err != nil {
			return nil, err
		}
	}
	return m.Client.GetObject(ctx, m.Bucket, key, opts)
}

func (m *Manager) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	_, err := m.Client.PutObject(ctx, m.Bucket, key, r, size, minio.PutObjectOptions{})
	return err
}

func (m *Manager) Remove(ctx context.Context, key string) error {
	return m.Client.RemoveObject(ctx, m.Bucket, key, minio.RemoveObjectOptions{})
}

func (m *Manager) RemovePrefix(ctx context.Context, prefix string) (int, error) {
	ch := m.Client.ListObjects(ctx, m.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	objCh := make(chan minio.ObjectInfo, 100)
	var count int
	go func() {
		defer close(objCh)
		for obj := range ch {
			if obj.Err != nil {
				objCh <- obj
				return
			}
			count++
			objCh <- obj
		}
	}()
	var firstErr error
	for err := range m.Client.RemoveObjects(ctx, m.Bucket, objCh, minio.RemoveObjectsOptions{}) {
		if err.Err != nil && firstErr == nil {
			firstErr = err.Err
		}
	}
	return count, firstErr
}

func (m *Manager) NewMultipart(ctx context.Context, key string) (string, error) {
	return m.Core.NewMultipartUpload(ctx, m.Bucket, key, minio.PutObjectOptions{})
}

func (m *Manager) PutPart(ctx context.Context, key, uploadID string, partNumber int, r io.Reader, size int64) (string, error) {
	p, err := m.Core.PutObjectPart(ctx, m.Bucket, key, uploadID, partNumber, r, size, minio.PutObjectPartOptions{})
	if err != nil {
		return "", err
	}
	return p.ETag, nil
}

func (m *Manager) ListParts(ctx context.Context, key, uploadID string) ([]protocol.PartInfo, error) {
	var out []protocol.PartInfo
	marker := 0
	for {
		res, err := m.Core.ListObjectParts(ctx, m.Bucket, key, uploadID, marker, 1000)
		if err != nil {
			return nil, err
		}
		for _, p := range res.ObjectParts {
			out = append(out, protocol.PartInfo{Number: p.PartNumber, Size: p.Size, ETag: p.ETag})
		}
		if !res.IsTruncated {
			break
		}
		if res.NextPartNumberMarker == 0 || res.NextPartNumberMarker == marker {
			break
		}
		marker = res.NextPartNumberMarker
	}
	return out, nil
}

func (m *Manager) CompleteMultipart(ctx context.Context, key, uploadID string, parts []protocol.PartInfo) error {
	cp := make([]minio.CompletePart, 0, len(parts))
	for _, p := range parts {
		cp = append(cp, minio.CompletePart{PartNumber: p.Number, ETag: p.ETag})
	}
	if len(cp) == 0 {
		return errors.New("没有可提交的分片")
	}
	_, err := m.Core.CompleteMultipartUpload(ctx, m.Bucket, key, uploadID, cp, minio.PutObjectOptions{})
	return err
}

func (m *Manager) AbortMultipart(ctx context.Context, key, uploadID string) error {
	return m.Core.AbortMultipartUpload(ctx, m.Bucket, key, uploadID)
}

func (m *Manager) IncompleteUploads(ctx context.Context) <-chan minio.ObjectMultipartInfo {
	return m.Client.ListIncompleteUploads(ctx, m.Bucket, "", true)
}

func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	return minio.ToErrorResponse(err).Code
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	r := minio.ToErrorResponse(err)
	return r.Code == "NoSuchKey" || r.Code == "NoSuchUpload" || r.Code == "NoSuchBucket" || r.StatusCode == http.StatusNotFound
}

func Wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}
