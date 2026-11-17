package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/config"
)

var (
	downloadOutput string
	downloadForce  bool

	downloadCmd = &cobra.Command{
		Use:   "download <inbound|upload> <相对路径>",
		Short: "下载文件",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			runDownload(clientState.g, clientState.cfg, args[0], args[1], downloadOutput, downloadForce)
		},
	}
)

func init() {
	rootCmd.AddCommand(downloadCmd)
	downloadCmd.Flags().StringVar(&downloadOutput, "output", ".", "下载目录")
	downloadCmd.Flags().BoolVar(&downloadForce, "force", false, "覆盖已存在文件")
}

func runDownload(g globals, cfg config.ClientConfig, kind, remotePath, out string, force bool) {
	if kind != "inbound" && kind != "upload" {
		fail(exitUsage, "用法: ft-client download <inbound|upload> <相对路径> [--output DIR] [--force]")
	}
	cl := login(g, cfg)
	if cl == nil {
		return
	}
	if err := cl.Download(context.Background(), kind, remotePath, out, force, progressPrinter()); err != nil {
		failFor(err)
	}
	fmt.Printf("下载完成: %s\n", out)
}
