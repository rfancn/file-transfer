package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rfancn/file-transfer/internal/adminapi"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/version"
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

func main() {
	fs := flag.NewFlagSet("ft-admin", flag.ExitOnError)
	cfgPath := fs.String("config", "", "管理端配置")
	showVersion := fs.Bool("version", false, "输出版本")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println(version.Version)
		return
	}
	cfg, err := config.LoadAdmin(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Server.URL == "" {
		cfg.Server.URL = promptServerURL()
	}
	args := fs.Args()
	ac := login(cfg)
	if len(args) == 0 {
		menu(cfg, ac)
		return
	}
	switch args[0] {
	case "init":
		runInit(&cfg, ac)
	case "user":
		runUser(ac, args[1:])
	case "clean":
		runClean(ac, args[1:])
	default:
		log.Fatalf("未知子命令: %s", args[0])
	}
}

// login 在显示菜单或执行命令前校验管理员凭证。
func login(cfg config.AdminConfig) *adminapi.Client {
	fmt.Printf("ft-server: %s\n", cfg.Server.URL)
	const attempts = 3
	for i := 0; i < attempts; i++ {
		user, pass := serverAdminCreds()
		ac, err := adminapi.New(cfg.Server.URL)
		if err != nil {
			log.Fatalf("连接 ft-server 失败: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = ac.Login(ctx, user, pass)
		cancel()
		if err == nil {
			fmt.Printf("管理员 %s 登录成功\n", user)
			return ac
		}
		if ae, ok := adminapi.AsAPIError(err); ok && ae.Status == http.StatusUnauthorized {
			fmt.Println("管理员用户名或密码错误")
			if os.Getenv("FT_ADMIN_PASSWORD") != "" {
				log.Fatal("管理员凭证无效，请检查 FT_ADMIN_USER / FT_ADMIN_PASSWORD")
			}
			continue
		}
		if ae, ok := adminapi.AsAPIError(err); ok {
			log.Fatalf("校验管理员凭证失败（%s，HTTP %d %s）: %s", cfg.Server.URL, ae.Status, ae.Code, ae.Message)
		}
		log.Fatalf("校验管理员凭证失败（%s）: %v", cfg.Server.URL, err)
	}
	log.Fatal("管理员凭证校验失败，已退出")
	return nil
}

// runInit 收集 root 凭据并调用 ft-server 的初始化接口；凭据只在内存中使用。
func runInit(cfg *config.AdminConfig, ac *adminapi.Client) {
	if !ensureAdminCreds(cfg) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ac.Init(ctx, cfg.S3.AccessKey, cfg.S3.SecretKey); err != nil {
		failf("初始化失败: %v（请确认 root AK/SK 正确，且 ft-server 能访问 s3.endpoint）", err)
		return
	}
	fmt.Println("初始化完成。首次初始化时 ft-server 会自动重启以加载配置。")
}

// serverAdminCreds 返回 ft-server 管理员凭证：环境变量优先，否则交互输入。
func serverAdminCreds() (string, string) {
	user := strings.TrimSpace(os.Getenv("FT_ADMIN_USER"))
	if user == "" {
		value, err := cliui.Input("ft-server 管理员用户名 [admin]", "", nil)
		if canceledByUser(err) {
			if cliui.IsInteractive() {
				os.Exit(130)
			}
			user = "admin"
		} else {
			if err != nil {
				log.Fatalf("读取管理员用户名失败: %v", err)
			}
			user = strings.TrimSpace(value)
			if user == "" {
				user = "admin"
			}
		}
	}
	pass := os.Getenv("FT_ADMIN_PASSWORD")
	if pass == "" {
		value, err := cliui.Password("ft-server 管理员密码")
		if canceledByUser(err) {
			if cliui.IsInteractive() {
				os.Exit(130)
			}
		} else {
			if err != nil {
				log.Fatalf("读取管理员密码失败（可设置 FT_ADMIN_PASSWORD）: %v", err)
			}
			pass = value
		}
	}
	if pass == "" {
		log.Fatal("缺少 ft-server 管理员密码（可设置 FT_ADMIN_PASSWORD）")
	}
	return user, pass
}

// ensureAdminCreds 只在内存中补齐 RustFS root 凭据：环境变量优先，否则交互输入。
func ensureAdminCreds(cfg *config.AdminConfig) bool {
	if cfg.S3.AccessKey == "" {
		value, err := cliui.Input("RustFS Access Key", "", nil)
		if canceledByUser(err) {
			if !cliui.IsInteractive() {
				failf("缺少 RustFS Access Key（可设置 RUSTFS_ACCESS_KEY）")
			}
			return false
		}
		if err != nil {
			failf("读取 RustFS Access Key 失败: %v", err)
			return false
		}
		cfg.S3.AccessKey = strings.TrimSpace(value)
	}
	if cfg.S3.SecretKey == "" {
		value, err := cliui.Password("RustFS Secret Key")
		if canceledByUser(err) {
			if !cliui.IsInteractive() {
				failf("缺少 RustFS Secret Key（可设置 RUSTFS_SECRET_KEY）")
			}
			return false
		}
		if err != nil {
			failf("读取 RustFS Secret Key 失败: %v", err)
			return false
		}
		cfg.S3.SecretKey = value
	}
	if cfg.S3.AccessKey == "" || cfg.S3.SecretKey == "" {
		failf("缺少 RustFS 管理凭据（可设置 RUSTFS_ACCESS_KEY/RUSTFS_SECRET_KEY）")
		return false
	}
	return true
}

const defaultAdminServerURL = "http://127.0.0.1:8080"

func promptServerURL() string {
	value, err := cliui.Input("服务器地址 ["+defaultAdminServerURL+"]", "", nil)
	if canceledByUser(err) {
		if cliui.IsInteractive() {
			os.Exit(130)
		}
		return defaultAdminServerURL
	}
	if err != nil {
		log.Fatalf("读取服务器地址失败: %v", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultAdminServerURL
	}
	return value
}

func runUser(ac *adminapi.Client, args []string) {
	if len(args) < 1 {
		log.Fatal("用法: ft-admin user add|del|reset|list <username>")
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			log.Fatal("用法: ft-admin user add <username>")
		}
		addUser(ac, args[1])
	case "del":
		if len(args) < 2 {
			log.Fatal("用法: ft-admin user del <username> [--yes]")
		}
		yes := len(args) > 2 && args[2] == "--yes"
		delUser(ac, args[1], yes)
	case "reset":
		if len(args) < 2 {
			log.Fatal("用法: ft-admin user reset <username>")
		}
		resetUser(ac, args[1])
	case "list":
		listUsers(ac)
	default:
		log.Fatalf("未知 user 子命令: %s", args[0])
	}
}

func addUser(ac *adminapi.Client, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := ac.AddUser(ctx, name)
	if err != nil {
		if ae, ok := adminapi.AsAPIError(err); ok && ae.Code == "already_exists" {
			fmt.Printf("用户已存在，不重置密码: %s\n", name)
			return
		}
		failf("创建用户失败: %v", err)
		return
	}
	cliui.PrintAccount(res.Username, res.Password)
}

func resetUser(ac *adminapi.Client, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := ac.ResetPassword(ctx, name)
	if err != nil {
		if ae, ok := adminapi.AsAPIError(err); ok && ae.Code == "user_not_found" {
			fmt.Printf("用户不存在: %s\n", name)
			return
		}
		failf("重置密码失败: %v", err)
		return
	}
	cliui.PrintResetPassword(res.Username, res.Password)
}

func delUser(ac *adminapi.Client, name string, yes bool) {
	if !yes {
		fmt.Printf("将永久删除用户 %s 及其全部数据，不可恢复。\n", name)
		ok, err := cliui.Confirm(fmt.Sprintf("确认永久删除用户 %s", name), false)
		if canceledByUser(err) {
			return
		}
		if err != nil {
			failf("确认失败: %v", err)
			return
		}
		if !ok {
			fmt.Println("已取消")
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	n, err := ac.DeleteUser(ctx, name)
	if err != nil {
		failf("删除失败: %v", err)
		return
	}
	fmt.Printf("已删除用户 %s，删除对象 %d 个\n", name, n)
}

func listUsers(ac *adminapi.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	users, err := ac.ListUsers(ctx)
	if err != nil {
		failf("列出用户失败: %v", err)
		return
	}
	fmt.Printf("%-24s %-6s %-10s %-32s %s\n", "用户名", "类型", "状态", "策略", "组")
	for _, u := range users {
		kind := "普通"
		if u.System {
			kind = "系统"
		}
		fmt.Printf("%-24s %-6s %-10s %-32s %s\n", u.Username, kind, u.Status, u.PolicyName, strings.Join(u.Groups, ","))
	}
}

type cleanJob struct {
	dir  string
	days int // <=0 表示使用 ft-server 配置的默认保留天数
}

func runClean(ac *adminapi.Client, args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	daysFlag := fs.Int("days", -1, "保留天数")
	dry := fs.Bool("dry-run", false, "只列出将删除的对象")
	yes := fs.Bool("yes", false, "跳过确认")
	_ = fs.Parse(args)

	dirs := []string{"inbound", "upload"}
	if rest := fs.Args(); len(rest) > 0 {
		switch rest[0] {
		case "inbound", "outbound", "upload", "incomplete":
			dirs = []string{rest[0]}
		default:
			failf("未知目录: %s", rest[0])
			return
		}
	}
	if *daysFlag == 0 {
		fmt.Println("保留天数为 0，已取消")
		return
	}

	if !*yes && !*dry {
		var prompt string
		if *daysFlag > 0 {
			if len(dirs) == 1 {
				prompt = fmt.Sprintf("清理所有用户 %s 中 %d 天前的数据", dirs[0], *daysFlag)
			} else {
				prompt = fmt.Sprintf("清理所有用户 inbound/upload 中 %d 天前的数据（outbound 不变）", *daysFlag)
			}
		} else if len(dirs) == 1 {
			prompt = fmt.Sprintf("按配置的保留天数清理所有用户 %s 数据", dirs[0])
		} else {
			prompt = "按配置的保留天数清理所有用户 inbound/upload 数据（outbound 不变）"
		}
		ok, err := cliui.Confirm(prompt, false)
		if canceledByUser(err) {
			return
		}
		if err != nil {
			failf("确认失败: %v", err)
			return
		}
		if !ok {
			fmt.Println("已取消")
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	failed := false
	for _, dir := range dirs {
		res, err := ac.Clean(ctx, dir, *daysFlag, *dry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "清理 %s 失败: %v\n", dir, err)
			failed = true
			continue
		}
		if *dry {
			for _, key := range res.Items {
				fmt.Printf("[dry-run] %s\n", key)
			}
		}
		unit := "个对象"
		if dir == "incomplete" {
			unit = "个未完成分片"
		}
		action := "已清理"
		if *dry {
			action = "待清理"
		}
		fmt.Printf("%s：%s %d %s\n", dir, action, res.Count, unit)
	}
	if failed && !interactiveMenu {
		log.Fatal("部分目录清理失败")
	}
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
				addUser(ac, name)
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
			runClean(ac, nil)
		case 5:
			runInit(&cfg, ac)
		case 6:
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
