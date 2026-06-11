package cmd

import (
	"fmt"

	"github.com/hanthor/corral/pkg/catalog"
	"github.com/spf13/cobra"
)

var imagesCmd = &cobra.Command{
	Use:   "images",
	Short: "List the built-in OS image catalog",
	Long: `List ready-to-boot OS images. Create a VM from one with:

  corral create myvm --image ubuntu

For anything else, import a qcow2/raw disk image by URL:

  corral create myvm --import https://…/image.qcow2`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("%-16s %-9s %s\n", "NAME", "USER", "DESCRIPTION")
		for _, img := range catalog.Images {
			fmt.Printf("%-16s %-9s %s\n", img.Name, img.DefaultUser, img.Description)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(imagesCmd)
}
