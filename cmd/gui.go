package cmd

import (
	"github.com/spf13/cobra"

	"github.com/iyear/tdl/app/desktop"
)

// NewGUI starts the local Fyne desktop downloader.
func NewGUI() *cobra.Command {
	return &cobra.Command{
		Use:     "gui",
		Aliases: []string{"desktop"},
		Short:   "Start the desktop downloader",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return desktop.Run(cmd.Context())
		},
	}
}
