package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogin(t *testing.T) {
	var gotUser, gotPass string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/login" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotUser, gotPass = req.Username, req.Password
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "admin-token", "expires_in": 3600})
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background(), "admin", "admin-pass"); err != nil {
		t.Fatal(err)
	}
	if gotUser != "admin" || gotPass != "admin-pass" {
		t.Fatalf("user=%q pass=%q", gotUser, gotPass)
	}
	if c.token != "admin-token" {
		t.Fatalf("token = %q", c.token)
	}
}

func TestInit(t *testing.T) {
	var gotAuth, gotAK, gotSK string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/bootstrap" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			AccessKey string `json:"access_key"`
			SecretKey string `json:"secret_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotAK, gotSK = req.AccessKey, req.SecretKey
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	if err := c.Init(context.Background(), "root", "secret"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer admin-token" || gotAK != "root" || gotSK != "secret" {
		t.Fatalf("auth=%q ak=%q sk=%q", gotAuth, gotAK, gotSK)
	}
}

func TestAddUser(t *testing.T) {
	var gotAuth, gotUsername string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/admin/users" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req struct {
			Username string `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotUsername = req.Username
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"username": req.Username, "password": "generated"})
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	res, err := c.AddUser(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer admin-token" || gotUsername != "alice" || res.Password != "generated" {
		t.Fatalf("auth=%q req=%q res=%+v", gotAuth, gotUsername, res)
	}
}

func TestResetPassword(t *testing.T) {
	var gotAuth, gotMethod, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"username": "alice", "password": "Ab3xY9kL"})
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	res, err := c.ResetPassword(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/admin/users/alice/password" || gotAuth != "Bearer admin-token" {
		t.Fatalf("method=%q path=%q auth=%q", gotMethod, gotPath, gotAuth)
	}
	if res.Username != "alice" || res.Password != "Ab3xY9kL" {
		t.Fatalf("res = %+v", res)
	}
}

func TestListUsers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/admin/users" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"users": []map[string]any{{"username": "alice", "system": true, "status": "enabled", "policy_name": "p", "groups": []string{"g"}}},
		})
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	users, err := c.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "alice" || !users[0].System || users[0].PolicyName != "p" || len(users[0].Groups) != 1 {
		t.Fatalf("users = %+v", users)
	}
}

func TestClean(t *testing.T) {
	var gotAuth, gotDays, gotDry string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/admin/data/inbound" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotDays = r.URL.Query().Get("days")
		gotDry = r.URL.Query().Get("dry_run")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CleanResult{Count: 2, Items: []string{"a", "b"}, DryRun: true})
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.token = "admin-token"
	res, err := c.Clean(context.Background(), "inbound", 7, true)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer admin-token" || gotDays != "7" || gotDry != "true" {
		t.Fatalf("auth=%q days=%q dry=%q", gotAuth, gotDays, gotDry)
	}
	if res.Count != 2 || len(res.Items) != 2 || !res.DryRun {
		t.Fatalf("res = %+v", res)
	}
}

func TestUnauthorizedError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_credentials", "message": "管理员用户名或密码错误"})
	}))
	defer ts.Close()

	c, err := New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Login(context.Background(), "admin", "wrong")
	ae, ok := AsAPIError(err)
	if !ok || ae.Status != http.StatusUnauthorized || ae.Code != "invalid_credentials" {
		t.Fatalf("err = %#v", err)
	}
}
