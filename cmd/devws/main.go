// devws is the command line client for devws, the Kubernetes development
// workspace platform.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "devws",
		Short:   "devws — develop inside Kubernetes",
		Version: version,
	}
	root.AddCommand(
		loginCmd(),
		whoamiCmd(),
		logoutCmd(),
		workspaceCmd(),
		imageCmd(),
		sshConfigCmd(),
		sshCmd(),
		codeCmd(),
		keyCmd(),
		tokenCmd(),
		userCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the currently logged-in user",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			user, err := apiGet[userView](cfg, "/api/auth/me")
			if err != nil {
				return err
			}
			fmt.Printf("logged in to %s as %s (role: %s)\n", cfg.ServerURL, user.Username, user.Role)
			return nil
		},
	}
}

func logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored credentials",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := RemoveConfig(); err != nil {
				return err
			}
			fmt.Println("logged out")
			return nil
		},
	}
}
