package cliui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPathPrompt(t *testing.T) {
	dir := t.TempDir()
	got := pathPrompt("本地路径", dir, dir)
	if !strings.Contains(got, "本地路径["+dir+"]") ||
		!strings.Contains(got, "Tab 补全") ||
		!strings.Contains(got, "回车使用默认值") ||
		!strings.HasSuffix(got, ": ") {
		t.Fatalf("prompt=%q", got)
	}
	got = pathPrompt("下载到目录", ".", dir)
	if !strings.Contains(got, "["+dir+"] [.]") {
		t.Fatalf("prompt=%q", got)
	}
}

func TestPathCompleter(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "alpine"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha.txt", "beta.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	p := pathCompleter{}
	tests := []struct {
		input string
		want  []string
	}{
		{filepath.Join(dir, "al"), []string{"pha.txt", "pine/"}},
		{filepath.Join(dir, "alpha"), []string{".txt"}},
		{filepath.Join(dir, "zzz"), nil},
	}
	for _, tt := range tests {
		line := []rune(tt.input)
		got, offset := p.Do(line, len(line))
		gotStrings := make([]string, len(got))
		for i, c := range got {
			gotStrings[i] = string(c)
		}
		sort.Strings(gotStrings)
		want := append([]string(nil), tt.want...)
		sort.Strings(want)
		if len(gotStrings) != len(want) {
			t.Fatalf("%q: got %v, want %v", tt.input, gotStrings, want)
		}
		for i := range want {
			if gotStrings[i] != want[i] {
				t.Fatalf("%q: got %v, want %v", tt.input, gotStrings, want)
			}
		}
		if len(want) > 0 && offset != len(line) {
			t.Fatalf("%q: offset=%d, want %d", tt.input, offset, len(line))
		}
	}

	if got, offset := p.Do([]rune("~"), 1); offset != 1 || len(got) != 1 || string(got[0]) != "/" {
		t.Fatalf("~: got %v offset=%d", got, offset)
	}

	t.Run("empty input uses current dir", func(t *testing.T) {
		t.Chdir(dir)
		got, offset := p.Do(nil, 0)
		gotStrings := make([]string, len(got))
		for i, c := range got {
			gotStrings[i] = string(c)
		}
		sort.Strings(gotStrings)
		want := []string{"alpha.txt", "alpine/", "beta.txt"}
		sort.Strings(want)
		if offset != 0 || len(gotStrings) != len(want) {
			t.Fatalf("got %v offset=%d, want %v offset=0", gotStrings, offset, want)
		}
		for i := range want {
			if gotStrings[i] != want[i] {
				t.Fatalf("got %v, want %v", gotStrings, want)
			}
		}
	})
}
