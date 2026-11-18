package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/config"
)

// interactiveMenu 为 true 时，操作失败只打印错误并返回菜单，不退出进程。
var interactiveMenu bool

// canceledByUser 在 Ctrl-C 时退出程序，ESC 时返回 true 交由调用方返回上级。
func canceledByUser(err error) bool {
	if errors.Is(err, cliui.ErrInterrupt) {
		os.Exit(130)
	}
	return errors.Is(err, cliui.ErrCanceled)
}

func failf(format string, args ...any) {
	if interactiveMenu {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		return
	}
	log.Fatalf(format, args...)
}

// confirm 显示确认提示；用户取消或按 ESC 时返回 false。
func confirm(prompt string) bool {
	ok, err := cliui.Confirm(prompt, false)
	if canceledByUser(err) {
		return false
	}
	if err != nil {
		failf("确认失败: %v", err)
		return false
	}
	if !ok {
		fmt.Println("已取消")
	}
	return ok
}

func menu(cfg config.AdminConfig, ac *adminapi.Client) {
	interactiveMenu = true
	for {
		idx, err := cliui.Select("请选择操作", []string{
			"添加用户",
			"删除用户",
			"重置用户密码",
			"列出用户",
			"清理数据",
			"设置策略 IP 白名单",
			"初始化",
			"退出",
		})
		if errors.Is(err, cliui.ErrCanceled) {
			if cliui.IsInteractive() {
				continue
			}
			return
		}
		if errors.Is(err, cliui.ErrInterrupt) {
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			continue
		}
		switch idx {
		case 0:
			if name, ok := promptUsername(); ok {
				addUser(ac, name, "", false)
			}
		case 1:
			if name, ok := promptUsername(); ok {
				delUser(ac, name, false)
			}
		case 2:
			if name, ok := promptUsername(); ok {
				resetUser(ac, name)
			}
		case 3:
			listUsers(ac)
		case 4:
			runClean(ac, "", -1, false, false)
		case 5:
			runPolicyIPMenu(ac)
		case 6:
			if runInit(&cfg, ac, "", "") {
				return
			}
		case 7:
			return
		}
	}
}

func promptUsername() (string, bool) {
	value, err := cliui.Input("用户名", "", nil)
	if canceledByUser(err) {
		return "", false
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取用户名失败: %v\n", err)
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		fmt.Println("用户名不能为空")
		return "", false
	}
	return value, true
}
