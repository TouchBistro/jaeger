package cmd

import (
	"github.com/TouchBistro/goutils/fatal"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "jaeger",
	Short: "jaeger is a tool to aid in live debugging of ECS containers.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fatal.ExitErr(err, "Failed executing command.")
	}
}
