package cli

import (
	"github.com/spf13/cobra"
)

var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "velocix",
	Short: "Centralized pipeline viewer for GitHub Actions",
	Long:  "Velocix provides a real-time dashboard for monitoring GitHub Actions workflow runs across your organization.",
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(upgradeCmd)
	rootCmd.Version = Version
}
