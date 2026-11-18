package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/config"
)

func TestRunInitReturnsTrueOnSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/bootstrap" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := config.AdminConfig{Server: config.ClientSection{URL: ts.URL}}
	ac, err := adminapi.New(cfg.Server)
	if err != nil {
		t.Fatal(err)
	}
	if !runInit(&cfg, ac, "ak", "sk") {
		t.Fatal("runInit = false, want true")
	}
}

func TestRunInitReturnsFalseOnFailure(t *testing.T) {
	interactiveMenu = true
	defer func() { interactiveMenu = false }()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := config.AdminConfig{Server: config.ClientSection{URL: ts.URL}}
	ac, err := adminapi.New(cfg.Server)
	if err != nil {
		t.Fatal(err)
	}
	if runInit(&cfg, ac, "ak", "sk") {
		t.Fatal("runInit = true, want false")
	}
}
