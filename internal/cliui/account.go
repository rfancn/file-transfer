// Package cliui 提供终端交互与输出格式辅助。
package cliui

import "fmt"

// PrintAccount 以醒目格式输出新建用户的账号和一次性密码。
func PrintAccount(username, password string) {
	printAccount("用户创建成功", username, password)
}

// PrintResetPassword 以醒目格式输出重置后的账号和一次性密码。
func PrintResetPassword(username, password string) {
	printAccount("密码重置成功", username, password)
}

func printAccount(title, username, password string) {
	fmt.Println("========================================")
	fmt.Printf("  %s\n", title)
	fmt.Println("========================================")
	fmt.Printf("  账号: %s\n", username)
	fmt.Printf("  密码: %s\n", password)
	fmt.Println("----------------------------------------")
	fmt.Println("  密码只显示这一次，请立即保存。")
	fmt.Println("========================================")
}
