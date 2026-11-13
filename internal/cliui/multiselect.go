package cliui

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// MultiItem 是多选列表中的一项。
type MultiItem struct {
	Text       string
	Selectable bool
	Checked    bool
	// Partial 表示下级只有部分文件被选中，显示为 [-]。
	Partial bool
	// Touched 记录用户是否用空格切换过该项，调用方据此同步选择状态。
	Touched bool
}

const (
	keyUp   rune = -1
	keyDown rune = -2
)

// MultiSelect 显示可勾选列表：空格切换勾选，回车返回当前项，ESC 取消。
// items 中的 Checked 会在返回时更新（切片元素共享）。
func MultiSelect(label string, items []MultiItem) (int, error) {
	if !IsInteractive() {
		return -1, errors.New("多选需要交互式终端")
	}
	if len(items) == 0 {
		return -1, errors.New("没有可选项")
	}
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return -1, err
	}
	defer term.Restore(int(os.Stdin.Fd()), state)

	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h")

	m := &multiSelect{label: label, items: items}
	defer m.clear()
	m.render(true)
	for {
		key, err := readKey(os.Stdin)
		if err != nil {
			return -1, err
		}
		switch key {
		case keyUp:
			if m.cursor == 0 {
				m.cursor = len(items) - 1
			} else {
				m.cursor--
			}
			m.render(false)
		case keyDown:
			m.cursor = (m.cursor + 1) % len(items)
			m.render(false)
		case ' ':
			if items[m.cursor].Selectable {
				items[m.cursor].Checked = !items[m.cursor].Checked
				items[m.cursor].Partial = false
				items[m.cursor].Touched = true
			}
			m.render(false)
		case '\r', '\n':
			return m.cursor, nil
		case 3:
			return -1, ErrInterrupt
		case 27:
			return -1, ErrCanceled
		}
	}
}

type multiSelect struct {
	label  string
	items  []MultiItem
	cursor int
}

func (m *multiSelect) render(first bool) {
	lines := m.lines()
	if !first {
		fmt.Fprintf(os.Stdout, "\033[%dA", len(lines))
	}
	for _, line := range lines {
		fmt.Fprintf(os.Stdout, "\033[2K\r%s\n", line)
	}
}

func (m *multiSelect) clear() {
	lines := len(m.items) + 2
	fmt.Fprintf(os.Stdout, "\033[%dA", lines)
	for i := 0; i < lines; i++ {
		fmt.Fprint(os.Stdout, "\033[2K\r")
		if i != lines-1 {
			fmt.Fprint(os.Stdout, "\n")
		}
	}
	fmt.Fprintf(os.Stdout, "\033[%dA", lines-1)
	fmt.Fprint(os.Stdout, "\r")
}

func (m *multiSelect) lines() []string {
	width := stdoutWidth() - 1
	if width < 20 {
		width = 20
	}
	lines := []string{truncateDisplayWidth("? "+m.label, width)}
	for i, item := range m.items {
		prefix := "  "
		if i == m.cursor {
			prefix = "▸ "
		}
		box := "    "
		if item.Selectable {
			switch {
			case item.Checked:
				box = "[x] "
			case item.Partial:
				box = "[-] "
			default:
				box = "[ ] "
			}
		}
		line := truncateDisplayWidth(prefix+box+item.Text, width)
		if i == m.cursor {
			line = "\033[1;36m" + line + "\033[0m"
		}
		lines = append(lines, line)
	}
	lines = append(lines, truncateDisplayWidth("  空格勾选  回车进入/执行  ESC 返回  Ctrl+C 退出", width))
	return lines
}

func truncateDisplayWidth(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	return runewidth.Truncate(s, width, "")
}

func stdoutWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 120
}

func readKey(f *os.File) (rune, error) {
	buf := make([]byte, 1)
	if _, err := f.Read(buf); err != nil {
		return 0, err
	}
	if buf[0] != 27 {
		return rune(buf[0]), nil
	}
	// ESC 序列：方向键等原样解析，裸 ESC 返回取消。
	if !fdReadable(f, 30*time.Millisecond) {
		return 27, nil
	}
	if _, err := f.Read(buf); err != nil {
		return 27, nil
	}
	if buf[0] != '[' && buf[0] != 'O' {
		return 27, nil
	}
	if _, err := f.Read(buf); err != nil {
		return 27, nil
	}
	switch buf[0] {
	case 'A':
		return keyUp, nil
	case 'B':
		return keyDown, nil
	}
	return 0, nil
}
