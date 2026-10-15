package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
)

var tagRemove bool

// tagCmd sets or clears a tag through the VM's backend Tagger. What a tag
// means is the backend's: a label on KubeVirt, a PVE tag on Proxmox, a
// key=value category on Nutanix.
var tagCmd = &cobra.Command{
	Use:   "tag <name> <tag>",
	Short: "Add or remove a VM tag through its backend",
	Example: `  corral tag web prod
  corral tag web prod --remove
  corral tag app env=prod        # Nutanix: an existing category`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		adapter, err := instanceAdapter(args[0])
		if err != nil {
			return err
		}
		tagger, ok := adapter.(backend.Tagger)
		if !ok {
			return unsupported(adapter, "tags")
		}
		return tagger.SetTag(args[0], args[1], !tagRemove)
	},
}

func init() {
	tagCmd.Flags().BoolVar(&tagRemove, "remove", false, "Remove the tag instead of adding it")
	rootCmd.AddCommand(tagCmd)
}
