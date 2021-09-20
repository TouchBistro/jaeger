package cmd

import (
	"fmt"

	"github.com/TouchBistro/goutils/fatal"
	"github.com/TouchBistro/jaeger/aws"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Args:  cobra.NoArgs,
	Short: "List all services available across every visible cluster.",
	Run: func(cmd *cobra.Command, args []string) {
		serviceNames, err := aws.ListServices(cmd.Context())
		if err != nil {
			fatal.ExitErr(err, "failed to list available services")
		}
		for _, n := range serviceNames {
			fmt.Println(n)
		}
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}
