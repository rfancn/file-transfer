package cliui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chzyer/readline"
	"github.com/manifoldco/promptui"
	"golang.org/x/term"
)

// ErrCanceled 表示用户按 ESC 或 Ctrl-D 取消了当前交互。
var ErrCanceled = errors.New("已取消")

// ErrInterrupt 表示用户按 Ctrl-C 中断。
var ErrInterrupt = errors.New("中断")

// IsCanceled 报告错误是否由 ESC/Ctrl-D 或 Ctrl-C 产生。
func IsCanceled(err error) bool {
	return errors.Is(err, ErrCanceled) || errors.Is(err, ErrInterrupt)
}

// ValidateFunc 校验输入；返回非 nil 时交互会提示错误并允许重新输入。
type ValidateFunc = promptui.ValidateFunc

var stdinReader = bufio.NewReader(os.Stdin)

// IsInteractive 报告标准输入是否为终端。
func IsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// ReadLine 从标准输入读取一行，供 --password-stdin 等非交互场景使用。
func ReadLine() (string, error) {
	line, ok := readLine()
	if !ok {
		return "", ErrCanceled
	}
	return line, nil
}

func readLine() (string, bool) {
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.TrimRight(line, "\r\n"), true
}

// Select 显示选项列表并返回选中项的下标；非终端环境退化为编号输入。
func Select(label string, items []string) (int, error) {
	if len(items) == 0 {
		return -1, errors.New("没有可选项")
	}
	if !IsInteractive() {
		fmt.Println(label)
		for i, item := range items {
			fmt.Printf("  %d) %s\n", i+1, item)
		}
		for {
			fmt.Printf("请选择 [1-%d]: ", len(items))
			line, ok := readLine()
			if !ok {
				return -1, ErrCanceled
			}
			n, err := strconv.Atoi(strings.TrimSpace(line))
			if err == nil && n >= 1 && n <= len(items) {
				return n - 1, nil
			}
			fmt.Println("无效选择")
		}
	}
	size := len(items)
	if size > 15 {
		size = 15
	}
	p := promptui.Select{
		Label: label,
		Items: items,
		Size:  size,
		Stdin: promptStdin(),
		Templates: &promptui.SelectTemplates{
			// 保留默认的 "▸ " 前缀，保证与未选中项的缩进对齐。
			Active: fmt.Sprintf("%s {{ . | cyan | bold }}", promptui.IconSelect),
			Help: `{{ "↑/↓ 移动" | faint }}  {{ "回车 确认/进入" | faint }}  {{ "空格 选择" | faint }}  ` +
				`{{ "ESC 返回上级" | faint }}  {{ "Ctrl+C 退出" | faint }}`,
		},
	}
	idx, _, err := p.Run()
	if err != nil {
		return -1, normalizeErr(err)
	}
	return idx, nil
}

// Input 显示带默认值的单行输入；validate 非 nil 时实时校验。
func Input(label, defaultVal string, validate ValidateFunc) (string, error) {
	display := label
	if defaultVal != "" {
		display = fmt.Sprintf("%s [%s]", label, defaultVal)
	}
	if !IsInteractive() {
		return readFromStdin(inputPrompt(label, defaultVal), defaultVal, validate)
	}
	wrapped := validate
	if defaultVal != "" && validate != nil {
		wrapped = func(s string) error {
			if s == "" {
				s = defaultVal
			}
			return validate(s)
		}
	}
	p := promptui.Prompt{
		Label:    display,
		Validate: wrapped,
		Stdin:    promptStdin(),
	}
	value, err := p.Run()
	if err != nil {
		return "", normalizeErr(err)
	}
	if value == "" {
		value = defaultVal
	}
	return value, nil
}

// Password 读取密码；终端下以遮罩字符显示（密码不会明文回显）。
func Password(label string) (string, error) {
	return PasswordWithValidate(label, nil)
}

// PasswordWithValidate 读取密码，并在提交时校验。
func PasswordWithValidate(label string, validate ValidateFunc) (string, error) {
	if !IsInteractive() {
		return "", errors.New("非交互终端请使用 --password-stdin 提供密码")
	}
	p := promptui.Prompt{Label: label, Mask: '●', Validate: validate, Stdin: promptStdin()}
	value, err := p.Run()
	if err != nil {
		return "", normalizeErr(err)
	}
	return value, nil
}

// Confirm 显示是/否确认，defaultYes 决定直接回车时的结果。
func Confirm(label string, defaultYes bool) (bool, error) {
	if !IsInteractive() {
		suffix := "[y/N]"
		if defaultYes {
			suffix = "[Y/n]"
		}
		for {
			fmt.Printf("%s %s: ", label, suffix)
			line, ok := readLine()
			if !ok {
				return false, ErrCanceled
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "":
				return defaultYes, nil
			case "y", "yes":
				return true, nil
			case "n", "no":
				return false, nil
			}
			fmt.Println("请输入 y 或 n")
		}
	}
	defaultVal := "n"
	if defaultYes {
		defaultVal = "y"
	}
	p := promptui.Prompt{Label: label, IsConfirm: true, Default: defaultVal, Stdin: promptStdin()}
	value, err := p.Run()
	if err != nil {
		if errors.Is(err, promptui.ErrAbort) {
			return false, nil
		}
		return false, normalizeErr(err)
	}
	if value == "" {
		return defaultYes, nil
	}
	return strings.EqualFold(value, "y"), nil
}

// PathInput 读取本地路径；终端下支持 Tab 补全，多个匹配时再次按 Tab 会列出候选。
func PathInput(label, defaultVal string) (string, error) {
	wd, _ := os.Getwd()
	if defaultVal == "" && wd != "" {
		defaultVal = wd
	}
	prompt := pathPrompt(label, defaultVal, wd)
	if !IsInteractive() {
		return pathFromStdin(prompt, defaultVal)
	}
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          prompt,
		Stdin:           promptStdin(),
		AutoComplete:    pathCompleter{},
		HistoryLimit:    -1,
		InterruptPrompt: "^C",
		UniqueEditLine:  true,
	})
	if err != nil {
		return pathFromStdin(prompt, defaultVal)
	}
	defer rl.Close()

	line, err := rl.Readline()
	if err != nil {
		return "", normalizeErr(err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = defaultVal
	}
	return line, nil
}

func inputPrompt(label, defaultVal string) string {
	if defaultVal == "" {
		return label + ": "
	}
	return fmt.Sprintf("%s [%s]: ", label, defaultVal)
}

// pathPrompt 在路径输入提示中附带当前工作目录、默认值和操作说明。
func pathPrompt(label, defaultVal, wd string) string {
	display := label
	if wd != "" {
		display = fmt.Sprintf("%s[%s]", label, wd)
	}
	if defaultVal != "" && defaultVal != wd {
		display += fmt.Sprintf(" [%s]", defaultVal)
	}
	// 使用半角标点：readline 按 rune 宽度计算光标，全角标点会被少算一列。
	return display + " (Tab 补全, 回车使用默认值): "
}

func readFromStdin(prompt, defaultVal string, validate ValidateFunc) (string, error) {
	for {
		fmt.Print(prompt)
		line, ok := readLine()
		if !ok {
			return "", ErrCanceled
		}
		if line == "" {
			line = defaultVal
		}
		if validate != nil {
			if err := validate(line); err != nil {
				fmt.Fprintln(os.Stderr, err)
				continue
			}
		}
		return line, nil
	}
}

func pathFromStdin(prompt, defaultVal string) (string, error) {
	value, err := readFromStdin(prompt, defaultVal, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func normalizeErr(err error) error {
	if errors.Is(err, promptui.ErrInterrupt) || errors.Is(err, readline.ErrInterrupt) {
		if escPressed.Swap(false) {
			return ErrCanceled
		}
		return ErrInterrupt
	}
	if errors.Is(err, promptui.ErrEOF) || errors.Is(err, io.EOF) {
		escPressed.Store(false)
		return ErrCanceled
	}
	return err
}

// pathCompleter 为 readline 提供本地路径补全；目录候选带 "/" 后缀。
type pathCompleter struct{}

// Do 返回每个候选相对当前输入的增量，以及公共部分的 rune 长度。
func (pathCompleter) Do(line []rune, pos int) ([][]rune, int) {
	input := string(line[:pos])
	// 直接按 Tab 时按当前目录补齐。
	if input == "" {
		candidates := pathCandidates(".", "")
		if len(candidates) == 0 {
			return nil, 0
		}
		return candidates, 0
	}
	// 单独的 ~ 补全到家目录。
	if input == "~" {
		return [][]rune{[]rune("/")}, pos
	}

	displayDir, prefix := filepath.Split(input)
	readDir := displayDir
	switch {
	case readDir == "":
		readDir = "."
	case strings.HasPrefix(readDir, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, 0
		}
		readDir = filepath.Join(home, readDir[2:])
	}

	candidates := pathCandidates(readDir, prefix)
	if len(candidates) == 0 {
		return nil, 0
	}
	return candidates, pos
}

// pathCandidates 返回 readDir 下以 prefix 开头的候选（目录带 "/" 后缀）。
func pathCandidates(readDir, prefix string) [][]rune {
	entries, err := os.ReadDir(readDir)
	if err != nil {
		return nil
	}
	var candidates [][]rune
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		candidates = append(candidates, []rune(name[len(prefix):]))
	}
	return candidates
}
