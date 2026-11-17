package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/adminapi"
)

var (
	cleanDays int
	cleanDry  bool
	cleanYes  bool

	cleanCmd = &cobra.Command{
		Use:   "clean [inbound|outbound|upload|incomplete]",
		Short: "清理历史数据",
		Args:  cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			dirArg := ""
			if len(args) > 0 {
				dirArg = args[0]
			}
			runClean(adminState.ac, dirArg, cleanDays, cleanDry, cleanYes)
		},
	}
)

func init() {
	rootCmd.AddCommand(cleanCmd)
	cleanCmd.Flags().IntVar(&cleanDays, "days", -1, "保留天数")
	cleanCmd.Flags().BoolVar(&cleanDry, "dry-run", false, "只列出将删除的对象")
	cleanCmd.Flags().BoolVar(&cleanYes, "yes", false, "跳过确认")
}

func runClean(ac *adminapi.Client, dirArg string, days int, dry, yes bool) {
	dirs := []string{"inbound", "upload"}
	if dirArg != "" {
		switch dirArg {
		case "inbound", "outbound", "upload", "incomplete":
			dirs = []string{dirArg}
		default:
			failf("未知目录: %s", dirArg)
			return
		}
	}
	if days == 0 {
		fmt.Println("保留天数为 0，已取消")
		return
	}

	if !yes && !dry {
		target := strings.Join(dirs, "/")
		suffix := ""
		if len(dirs) > 1 {
			suffix = "（outbound 不变）"
		}
		var prompt string
		if days > 0 {
			prompt = fmt.Sprintf("清理所有用户 %s 中 %d 天前的数据%s", target, days, suffix)
		} else {
			prompt = fmt.Sprintf("按配置的保留天数清理所有用户 %s 数据%s", target, suffix)
		}
		if !confirm(prompt) {
			return
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	failed := false
	for _, dir := range dirs {
		res, err := ac.Clean(ctx, dir, days, dry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "清理 %s 失败: %v\n", dir, err)
			failed = true
			continue
		}
		if dry {
			for _, key := range res.Items {
				fmt.Printf("[dry-run] %s\n", key)
			}
		}
		unit := "个对象"
		if dir == "incomplete" {
			unit = "个未完成分片"
		}
		action := "已清理"
		if dry {
			action = "待清理"
		}
		fmt.Printf("%s：%s %d %s\n", dir, action, res.Count, unit)
	}
	if failed && !interactiveMenu {
		log.Fatal("部分目录清理失败")
	}
}
