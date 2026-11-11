package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// imageCmd manages the workspace image catalog through the REST API (like
// the workspace commands; only admins may modify it, everyone may list).
func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "image",
		Aliases: []string{"images"},
		Short:   "Manage the workspace image catalog",
	}
	cmd.AddCommand(imageListCmd(), imageAddCmd(), imageRemoveCmd(), imageDeprecateCmd())
	return cmd
}

func imageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the workspace images users can pick from",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			items, err := apiGet[[]workspaceImageView](cfg, "/api/workspace-images")
			if err != nil {
				return err
			}
			if len(*items) == 0 {
				fmt.Println("no workspace images in the catalog — users get the platform default")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 4, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tIMAGE\tDESCRIPTION\tDEPRECATED")
			for _, img := range *items {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", img.Name, img.Image, img.Description, deprecatedLabel(img.Deprecated))
			}
			return w.Flush()
		},
	}
}

func deprecatedLabel(deprecated bool) string {
	if deprecated {
		return "yes"
	}
	return "-"
}

func imageAddCmd() *cobra.Command {
	var (
		image       string
		tag         string
		description string
	)
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a workspace image to the catalog (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0], "image": strings.TrimSpace(image)}
			if tag != "" {
				body["tag"] = tag
			}
			if description != "" {
				body["description"] = description
			}
			img, err := apiPost[workspaceImageView](cfg, "/api/workspace-images", body)
			if err != nil {
				return err
			}
			fmt.Printf("workspace image %q added (%s)\n", img.Name, img.Image)
			fmt.Println("users can now pick it when creating a workspace")
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "image repository, e.g. ghcr.io/acme/devws-python")
	cmd.Flags().StringVar(&tag, "tag", "", "image tag (appended when --image carries none)")
	cmd.Flags().StringVar(&description, "description", "", "description shown in the workspace creation picker")
	return cmd
}

func imageRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm"},
		Short:   "Remove a workspace image from the catalog (admin)",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if _, err := apiDelete[map[string]any](cfg, "/api/workspace-images/"+args[0]); err != nil {
				return err
			}
			fmt.Println("workspace image removed — existing workspaces keep running it")
			return nil
		},
	}
}

func imageDeprecateCmd() *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "deprecate NAME",
		Short: "Hide a workspace image from the creation picker (admin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			body := map[string]any{"deprecated": !undo}
			if _, err := apiPatch[workspaceImageView](cfg, "/api/workspace-images/"+args[0], body); err != nil {
				return err
			}
			if undo {
				fmt.Println("workspace image offered again in the creation picker")
			} else {
				fmt.Println("workspace image deprecated — hidden from the creation picker; existing workspaces keep running it")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "offer the image again")
	return cmd
}
