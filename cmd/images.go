package cmd

import (
	"fmt"
	"strings"

	"github.com/hanthor/corral/pkg/catalog"
	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/types"
	"github.com/spf13/cobra"
)

var imagesCmd = &cobra.Command{
	Use:   "images",
	Short: "Browse and pull the curated OS image catalog",
	Long: `List ready-to-boot OS images. Create a VM from one with:

  corral create myvm --image ubuntu

Pull one as a reusable golden template (clone-from-template):

  corral images pull ubuntu

For anything else, import a qcow2/raw disk image by URL or file:

  corral import myvm --source https://…/image.qcow2`,
	RunE: func(cmd *cobra.Command, args []string) error {
		printCatalog(catalog.Images)
		return nil
	},
}

var imagesSearchCmd = &cobra.Command{
	Use:   "search [term]",
	Short: "Search the image catalog",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		term := ""
		if len(args) == 1 {
			term = args[0]
		}
		matches := searchCatalog(term)
		if len(matches) == 0 {
			return fmt.Errorf("no catalog image matches %q", term)
		}
		printCatalog(matches)
		return nil
	},
}

var imagesPullNamespace string

var imagesPullCmd = &cobra.Command{
	Use:   "pull <name>",
	Short: "Pull a catalog image as a golden VM template",
	Long: `Creates a stopped VM from the catalog image and marks it as a Corral
template. New VMs clone from it (disks included):

  corral images pull ubuntu
  corral template new tmpl-ubuntu dev1   # or clone in the web UI`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return pullTemplate(args[0], imagesPullNamespace)
	},
}

func printCatalog(images []catalog.Image) {
	fmt.Printf("%-26s %-10s %-14s %-24s %s\n", "NAME", "USER", "TYPE", "SOURCE", "DESCRIPTION")
	for _, img := range images {
		fmt.Printf("%-26s %-10s %-14s %-24s %s\n", img.Name, img.DefaultUser, img.Kind(), img.Source, img.Description)
	}
}

// searchCatalog filters by substring over name + description + source ("" = all).
func searchCatalog(term string) []catalog.Image {
	term = strings.ToLower(term)
	var out []catalog.Image
	for _, img := range catalog.Images {
		if term == "" ||
			strings.Contains(strings.ToLower(img.Name), term) ||
			strings.Contains(strings.ToLower(img.Description), term) ||
			strings.Contains(strings.ToLower(img.Source), term) {
			out = append(out, img)
		}
	}
	return out
}

// templateName is the VM name a pulled catalog image gets.
func templateName(image string) string { return "tmpl-" + image }

// pullTemplate creates a stopped VM from a catalog image and marks it as a
// template for clone-from-template.
func pullTemplate(image, ns string) error {
	img := catalog.Find(image)
	if img == nil {
		return fmt.Errorf("unknown image %q — see `corral images`", image)
	}
	if img.ISO != "" {
		return fmt.Errorf("%q is an installer ISO — it needs a manual install, so it can't be pulled as a template; create a VM with it instead: corral create myvm --image %s", image, image)
	}
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	name := templateName(image)
	opts := types.CreateOpts{
		Name:          name,
		Namespace:     ns,
		ContainerDisk: img.ContainerDisk,
		ImportURL:     img.URL,
		SSHPublicKey:  kubevirt.LoadSSHPublicKey(),
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		return err
	}
	if err := kubevirt.NewClient(ns).MarkTemplate(name, true); err != nil {
		return fmt.Errorf("marking template: %w", err)
	}
	fmt.Printf("template %q ready — clone with: corral template new %s <vm>\n", name, name)
	return nil
}

func init() {
	imagesPullCmd.Flags().StringVarP(&imagesPullNamespace, "namespace", "n", "", "Namespace (default tailvm)")
	imagesCmd.AddCommand(imagesSearchCmd, imagesPullCmd)
	rootCmd.AddCommand(imagesCmd)
}
