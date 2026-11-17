package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/config"
)

var (
	uploadDest string
	uploadYes  bool

	uploadCmd = &cobra.Command{
		Use:   "upload <本地路径>",
		Short: "上传文件",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			runUpload(clientState.g, clientState.cfg, args[0], uploadDest, uploadYes)
		},
	}
)

func init() {
	rootCmd.AddCommand(uploadCmd)
	uploadCmd.Flags().StringVar(&uploadDest, "dest", "", "目标名称")
	uploadCmd.Flags().BoolVar(&uploadYes, "yes", false, "跳过确认")
}

func runUpload(g globals, cfg config.ClientConfig, localPath, dest string, yes bool) {
	if _, err := os.Stat(localPath); err != nil {
		fail(exitLocalFile, "%v", err)
	}
	cl := login(g, cfg)
	if cl == nil {
		return
	}
	completed, err := uploadFlow(cl, cfg, localPath, dest, yes)
	if err != nil {
		if errors.Is(err, errPartialUpload) {
			fail(exitPartial, "%v", err)
		}
		failFor(err)
	}
	if completed {
		os.Exit(exitOK)
	}
}
