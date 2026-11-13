package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUploadTaskLifecycle(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	task := &UploadTask{
		Source: "/data/src",
		Batch:  "outbound/2026111301",
		Files:  []TaskFile{{Local: "/data/src/a", Remote: "outbound/2026111301/a", Size: 3}},
	}
	if err := SaveUploadTask("alice", task); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadUploadTask("alice", task.Source, task.Dest)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Batch != task.Batch || len(loaded.Files) != 1 {
		t.Fatalf("loaded=%+v", loaded)
	}
	pending, err := ListPendingUploadTasks("alice")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if err := CompleteUploadTask("alice", task.Source, task.Dest); err != nil {
		t.Fatal(err)
	}
	pending, err = ListPendingUploadTasks("alice")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
}

func TestPlanUploadToBatch(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanUploadTo(local, "renamed.txt", "outbound/2026111301")
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Files[0].Remote; got != "outbound/2026111301/renamed.txt" {
		t.Fatalf("remote=%s", got)
	}
}

func TestDiffTaskFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	c := filepath.Join(dir, "c.bin")
	previous := []TaskFile{
		{Local: a, Remote: "outbound/x/a.bin", Size: 1},
		{Local: b, Remote: "outbound/x/b.bin", Size: 2},
	}
	current := []UploadFile{
		{Local: a, Remote: "outbound/x/a.bin", Size: 3},
		{Local: c, Remote: "outbound/x/c.bin", Size: 4},
	}
	uploaded := map[string]bool{absPath(a): true, absPath(b): true}
	changes := DiffTaskFiles(previous, current, uploaded, dir)
	if len(changes) != 3 {
		t.Fatalf("changes=%+v", changes)
	}
	want := []struct {
		name string
		kind string
		old  int64
		new  int64
		up   bool
	}{
		{"a.bin", ChangeModified, 1, 3, true},
		{"b.bin", ChangeMissing, 2, 0, true},
		{"c.bin", ChangeAdded, 0, 4, false},
	}
	for i, w := range want {
		got := changes[i]
		if got.Name != w.name || got.Kind != w.kind || got.OldSize != w.old || got.NewSize != w.new || got.Uploaded != w.up {
			t.Fatalf("change[%d]=%+v, want %+v", i, got, w)
		}
	}
}

func TestMissingFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	previous := []TaskFile{{Local: a}, {Local: b}}
	current := []UploadFile{{Local: a}}
	missing := MissingFiles(previous, current)
	if len(missing) != 1 || missing[0] != b {
		t.Fatalf("missing=%v", missing)
	}
	if got := MissingFiles(previous, []UploadFile{{Local: a}, {Local: b}}); len(got) != 0 {
		t.Fatalf("missing=%v", got)
	}
}

func TestNextBatch(t *testing.T) {
	date := time.Now().Format("20060102")
	body := fmt.Sprintf(`{"files":[{"path":"%s01/a","size":1},{"path":"%s03/b","size":1},{"path":"20000101/old","size":1}]}`, date, date)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, http: srv.Client()}
	batch, err := c.NextBatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := date + "04"; batch != want {
		t.Fatalf("batch=%s, want %s", batch, want)
	}
}
