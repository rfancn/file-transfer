package auth

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestValidatePassword(t *testing.T) {
	for _, p := range []string{"Abcd1234!", "A1b2C3d4#", "Passw0rd$"} {
		if err := ValidatePassword(p); err != nil {
			t.Fatalf("%q should be valid: %v", p, err)
		}
	}
	for _, p := range []string{"short", "abcdefgh1", "ABCDEFGH1", "Abcdefgh", "Abcdefg1", "Abcd1234 "} {
		if err := ValidatePassword(p); err == nil {
			t.Fatalf("%q should be invalid", p)
		}
	}
}

func TestSessionToken(t *testing.T) {
	secret := "secret"
	claims := SessionClaims{Sub: "alice", Exp: time.Now().Add(time.Hour).Unix(), Iat: time.Now().Unix(), Kind: "session"}
	tok, err := Sign(secret, claims)
	if err != nil {
		t.Fatal(err)
	}
	var got SessionClaims
	if err := Verify(secret, tok, &got); err != nil {
		t.Fatal(err)
	}
	if got.Sub != "alice" {
		t.Fatalf("got %q", got.Sub)
	}
	if err := Verify("other", tok, &got); err == nil {
		t.Fatal("wrong secret should fail")
	}
	if err := Verify(secret, tok+"x", &got); err == nil {
		t.Fatal("tampered token should fail")
	}
}

func TestMultipartToken(t *testing.T) {
	claims := MultipartClaims{Sub: "bob", Path: "bob/outbound/a", PartSize: 5 << 20, S3UploadID: "u1", Exp: time.Now().Add(time.Hour).Unix(), Kind: "multipart"}
	tok, err := Sign("s", claims)
	if err != nil {
		t.Fatal(err)
	}
	var got MultipartClaims
	if err := Verify("s", tok, &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != claims.Path || got.PartSize != claims.PartSize {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateUsername(t *testing.T) {
	for _, n := range []string{"alice", "a_b-c", "A1"} {
		if err := ValidateUsername(n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	for _, n := range []string{"", "a.b", "a b", "ft-admin", "ft-data", "ft-operator", "root", strings.Repeat("a", 65)} {
		if err := ValidateUsername(n); err == nil {
			t.Fatalf("%s should be invalid", n)
		}
	}
}

func TestGeneratePasswordSatisfiesPolicy(t *testing.T) {
	for _, n := range []int{8, 16, 24} {
		for i := 0; i < 50; i++ {
			p, err := GeneratePassword(n)
			if err != nil {
				t.Fatal(err)
			}
			if utf8.RuneCountInString(p) != n {
				t.Fatalf("长度 %d，期望 %d", utf8.RuneCountInString(p), n)
			}
			if err := ValidatePassword(p); err != nil {
				t.Fatalf("生成的密码 %q 不符合密码策略: %v", p, err)
			}
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	p, err := GeneratePassword(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 16 {
		t.Fatalf("len %d", len(p))
	}
	for _, r := range p {
		if !strings.ContainsRune("abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#$%^&*()-_=+", r) {
			t.Fatalf("unexpected rune %q", r)
		}
	}
}
