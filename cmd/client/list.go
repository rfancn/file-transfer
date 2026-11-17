package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/rfancn/file-transfer/internal/config"
)

var listCmd = &cobra.Command{
	Use:       "list <inbound|outbound|upload>",
	Short:     "列出文件",
	ValidArgs: []string{"inbound", "outbound", "upload"},
	Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	Run: func(cmd *cobra.Command, args []string) {
		runList(clientState.g, clientState.cfg, args[0])
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList(g globals, cfg config.ClientConfig, kind string) {
	cl := login(g, cfg)
	if cl == nil {
		return
	}
	files, err := cl.List(context.Background(), kind)
	if err != nil {
		failFor(err)
	}
	printFiles(kind, files)
}
