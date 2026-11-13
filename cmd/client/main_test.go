package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rfancn/file-transfer/internal/protocol"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPrintSelectionSummaryListsFiles(t *testing.T) {
	out := captureStdout(t, func() {
		printSelectionSummary("upload", []summaryItem{{Name: "sub/b.txt", Size: 1024}}, 1, 1024, 40)
	})
	if !strings.Contains(out, "sub/b.txt") || !strings.Contains(out, "1.0 KiB") {
		t.Fatalf("summary missing file: %q", out)
	}
}

func TestBuildSummaryItemsCollapsesWholeDirectory(t *testing.T) {
	files := []protocol.FileInfo{
		{Path: "sub/a.txt", Size: 1024},
		{Path: "sub/b.txt", Size: 2048},
	}
	selected := map[string]downloadEntry{"sub": {Path: "sub", IsDir: true}}
	items := buildSummaryItems(selected, files, map[string]bool{})
	if len(items) != 1 || items[0].Name != "sub/" || items[0].Size != 3072 || !items[0].IsDir || items[0].FileCount != 2 {
		t.Fatalf("items=%+v", items)
	}

	selected = map[string]downloadEntry{"sub/a.txt": {Path: "sub/a.txt"}}
	items = buildSummaryItems(selected, files, map[string]bool{})
	if len(items) != 1 || items[0].Name != "sub/a.txt" {
		t.Fatalf("items=%+v", items)
	}
}

func TestBrowseEntries(t *testing.T) {
	files := []protocol.FileInfo{
		{Path: "a.txt", Size: 1},
		{Path: "sub/b.txt", Size: 2},
		{Path: "sub/deep/c.txt", Size: 3},
		{Path: "other/d.txt", Size: 4},
	}

	root := browseEntries(files, "")
	if len(root) != 3 {
		t.Fatalf("root=%+v", root)
	}
	if !root[0].IsDir || root[0].Name != "other" || !root[1].IsDir || root[1].Name != "sub" {
		t.Fatalf("root dirs=%+v", root)
	}
	if root[2].IsDir || root[2].Name != "a.txt" {
		t.Fatalf("root file=%+v", root[2])
	}

	sub := browseEntries(files, "sub")
	if len(sub) != 2 {
		t.Fatalf("sub=%+v", sub)
	}
	if !sub[0].IsDir || sub[0].Name != "deep" || sub[0].Path != "sub/deep" {
		t.Fatalf("sub dir=%+v", sub[0])
	}
	if sub[1].IsDir || sub[1].Name != "b.txt" || sub[1].Path != "sub/b.txt" {
		t.Fatalf("sub file=%+v", sub[1])
	}
}

func TestPadDisplay(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"a", 3, "a  "},
		{"中文", 6, "中文  "},
		{"abcdef", 3, "abcdef"},
	}
	for _, c := range cases {
		if got := padDisplay(c.in, c.width); got != c.want {
			t.Fatalf("padDisplay(%q,%d)=%q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestTruncateDisplay(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"中文名", 4, "中文"},
		{"abc", 5, "abc"},
		{"a中文", 3, "a中"},
	}
	for _, c := range cases {
		if got := truncateDisplay(c.in, c.width); got != c.want {
			t.Fatalf("truncateDisplay(%q,%d)=%q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestDirSelectionState(t *testing.T) {
	files := []protocol.FileInfo{
		{Path: "sub/a.txt", Size: 1},
		{Path: "sub/b.txt", Size: 1},
		{Path: "sub/deep/c.txt", Size: 1},
	}
	selected := map[string]downloadEntry{"sub/a.txt": {Path: "sub/a.txt"}}
	checked, partial := dirSelectionState("sub", files, selected, map[string]bool{})
	if checked || !partial {
		t.Fatalf("partial: checked=%v partial=%v", checked, partial)
	}

	selected["sub/b.txt"] = downloadEntry{Path: "sub/b.txt"}
	selected["sub/deep"] = downloadEntry{Path: "sub/deep", IsDir: true}
	checked, partial = dirSelectionState("sub", files, selected, map[string]bool{})
	if !checked || partial {
		t.Fatalf("full: checked=%v partial=%v", checked, partial)
	}

	if checked, partial := dirSelectionState("sub", files, map[string]downloadEntry{}, map[string]bool{}); checked || partial {
		t.Fatalf("empty: checked=%v partial=%v", checked, partial)
	}
}

func TestFileExcluded(t *testing.T) {
	excluded := map[string]bool{"sub/deep": true}
	if !fileExcluded("sub/deep/c.txt", excluded) {
		t.Fatal("sub/deep/c.txt should be excluded")
	}
	if fileExcluded("sub/b.txt", excluded) {
		t.Fatal("sub/b.txt should not be excluded")
	}
}

func TestMiddleTruncate(t *testing.T) {
	long := "a/very/long/path/to/some/deep/file.txt"
	got := middleTruncate(long, 20)
	if !strings.Contains(got, "…") || !strings.HasSuffix(got, "file.txt") {
		t.Fatalf("got=%q", got)
	}
	if short := middleTruncate("a.txt", 20); short != "a.txt" {
		t.Fatalf("got=%q", short)
	}
}

func TestSelectedByParent(t *testing.T) {
	selected := map[string]downloadEntry{"sub": {Path: "sub", IsDir: true}}
	if !selectedByParent("sub/b.txt", selected) {
		t.Fatal("sub/b.txt should inherit selection")
	}
	if !selectedByParent("sub/deep/c.txt", selected) {
		t.Fatal("sub/deep/c.txt should inherit selection")
	}
	if selectedByParent("other/x.txt", selected) {
		t.Fatal("other/x.txt should not inherit selection")
	}
	if selectedByParent("subway", selected) {
		t.Fatal("subway should not inherit selection")
	}
}

func TestExpandSelection(t *testing.T) {
	files := []protocol.FileInfo{
		{Path: "a.txt", Size: 1},
		{Path: "sub/b.txt", Size: 2},
		{Path: "sub/deep/c.txt", Size: 3},
	}
	selected := map[string]downloadEntry{
		"a.txt":     {Path: "a.txt"},
		"sub":       {Path: "sub", IsDir: true},
		"sub/b.txt": {Path: "sub/b.txt"},
	}
	got := expandSelection(selected, files, map[string]bool{})
	want := []string{"a.txt", "sub/b.txt", "sub/deep/c.txt"}
	if len(got) != len(want) {
		t.Fatalf("targets=%+v", got)
	}
	for i, w := range want {
		if got[i].Path != w {
			t.Fatalf("targets[%d]=%s, want %s", i, got[i].Path, w)
		}
	}
}

func TestParentDir(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"a":       "",
		"a/b":     "a",
		"a/b/c":   "a/b",
		"a/b/c/d": "a/b/c",
	}
	for in, want := range cases {
		if got := parentDir(in); got != want {
			t.Fatalf("parentDir(%q)=%q, want %q", in, got, want)
		}
	}
}
