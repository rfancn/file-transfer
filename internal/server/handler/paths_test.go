package handler

import "testing"

func TestValidateDownloadPath(t *testing.T) {
	valid := []string{"inbound/a.txt", "inbound/sub/b.txt", "upload/report.pdf", "upload/2026/01/x.bin"}
	for _, p := range valid {
		if _, err := validateDownloadPath(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	invalid := []string{"", "outbound/a.txt", "/inbound/a.txt", "inbound/", "inbound/../a.txt", "inbound/./a.txt", "inbound//a.txt"}
	for _, p := range invalid {
		if _, err := validateDownloadPath(p); err == nil {
			t.Fatalf("%s should be invalid", p)
		}
	}
}

func TestValidateUploadPath(t *testing.T) {
	valid := []string{"outbound/a.txt", "outbound/20260101/sub/b.txt"}
	for _, p := range valid {
		if _, err := validateUploadPath(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	invalid := []string{"", "inbound/a.txt", "upload/a.txt", "/outbound/a.txt", "outbound/../a.txt", "outbound//a.txt"}
	for _, p := range invalid {
		if _, err := validateUploadPath(p); err == nil {
			t.Fatalf("%s should be invalid", p)
		}
	}
}
