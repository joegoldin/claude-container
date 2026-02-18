package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var psJSON bool

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List all sessions",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("ps: not yet implemented")
	},
}

func init() {
	psCmd.Flags().BoolVar(&psJSON, "json", false, "Machine-readable JSON output")
	rootCmd.AddCommand(psCmd)
}
