package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var reserved = map[string]struct{}{
	"root": {}, "ft-admin": {}, "ft-data": {}, "ft-operator": {},
	"ft-server-data": {}, "ft-server-admin": {}, "ft-copy-out": {},
	"ft-user-group": {}, "ft-operator-group": {}, "ft-admin-group": {},
	"ft-download-svc": {}, "ft-copyout-svc": {}, "copyin": {}, "copyout": {},
}

// ValidatePassword 校验注册密码：至少 8 位，包含大写、小写、数字和特殊字符。
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	if utf8.RuneCountInString(password) > 128 {
		return errors.New("密码不能超过 128 位")
	}
	var upper, lower, digit, special bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		case !unicode.IsSpace(r):
			special = true
		}
	}
	switch {
	case !upper:
		return errors.New("密码至少包含一个大写字母")
	case !lower:
		return errors.New("密码至少包含一个小写字母")
	case !digit:
		return errors.New("密码至少包含一个数字")
	case !special:
		return errors.New("密码至少包含一个特殊字符")
	}
	return nil
}

func ValidateUsername(name string) error {
	if !usernameRe.MatchString(name) {
		return errors.New("用户名只能包含字母、数字、下划线和连字符，长度 1-64")
	}
	if _, ok := reserved[name]; ok {
		return fmt.Errorf("不允许使用保留用户名: %s", name)
	}
	return nil
}

type SessionClaims struct {
	Sub  string `json:"sub"`
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat"`
	Kind string `json:"kind"`
}

// AdminClaims 是 ft-admin 登录后取得的管理接口凭证。
type AdminClaims struct {
	Sub  string `json:"sub"`
	Exp  int64  `json:"exp"`
	Iat  int64  `json:"iat"`
	Kind string `json:"kind"`
}

type MultipartClaims struct {
	Sub        string `json:"sub"`
	Path       string `json:"path"`
	PartSize   int64  `json:"part_size"`
	S3UploadID string `json:"s3_upload_id"`
	Exp        int64  `json:"exp"`
	Iat        int64  `json:"iat"`
	Kind       string `json:"kind"`
}

func Sign(secret string, claims any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v1."))
	mac.Write([]byte(p))
	s := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "v1." + p + "." + s, nil
}

func Verify(secret, token string, out any) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return errors.New("token 格式错误")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v1."))
	mac.Write([]byte(parts[1]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(want, got) {
		return errors.New("token 签名错误")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("token 载荷错误")
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return errors.New("token 载荷错误")
	}
	return nil
}

func Expired(exp int64) bool {
	return time.Now().Unix() >= exp
}

func GeneratePassword(n int) (string, error) {
	if n < 8 {
		n = 8
	}
	const (
		lower   = "abcdefghijkmnopqrstuvwxyz"
		upper   = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		digits  = "23456789"
		special = "!@#$%^&*()-_=+"
	)
	all := lower + upper + digits + special
	out := make([]byte, 0, n)
	// 每类至少一个，保证生成结果满足 ValidatePassword。
	for _, pool := range []string{lower, upper, digits, special} {
		i, err := randomIndex(len(pool))
		if err != nil {
			return "", err
		}
		out = append(out, pool[i])
	}
	for len(out) < n {
		i, err := randomIndex(len(all))
		if err != nil {
			return "", err
		}
		out = append(out, all[i])
	}
	// Fisher-Yates 洗牌，避免固定位置出现固定字符类别。
	for i := len(out) - 1; i > 0; i-- {
		j, err := randomIndex(i + 1)
		if err != nil {
			return "", err
		}
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}

func randomIndex(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

func GenerateSecret(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
