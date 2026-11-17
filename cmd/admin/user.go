package main

import "github.com/spf13/cobra"

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "管理用户",
}

func init() {
	rootCmd.AddCommand(userCmd)
}
