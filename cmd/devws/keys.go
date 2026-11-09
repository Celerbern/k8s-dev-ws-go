package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

// runCommand execs an external binary, forwarding stdio and exit code.
func runCommand(name string, args []string) error {
	bin, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s is not installed or not in PATH", name)
	}
	proc := exec.Command(bin, args...)
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr
	if err := proc.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	return nil
}

func keyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Manage your SSH public keys",
	}
	cmd.AddCommand(keyAddCmd(), keyListCmd(), keyRemoveCmd())
	return cmd
}

func keyAddCmd() *cobra.Command {
	var (
		name string
		file string
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register an SSH public key (from --file or stdin)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			var raw []byte
			if file != "" && file != "-" {
				raw, err = os.ReadFile(file)
			} else {
				fmt.Fprintln(os.Stderr, "Paste your public key (ssh-ed25519 AAAA... user@host):")
				raw, err = readAllStdin()
			}
			if err != nil {
				return err
			}
			if name == "" {
				name = "cli-key"
			}
			if _, err := apiPost[map[string]any](cfg, "/api/users/"+cfg.Username+"/keys", map[string]string{
				"name": name,
				"key":  string(raw),
			}); err != nil {
				return err
			}
			fmt.Println("key registered")
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "label for the key")
	cmd.Flags().StringVar(&file, "file", "", "path to the public key file (\"-\" for stdin)")
	return cmd
}

func keyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your registered SSH public keys",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			user, err := apiGet[userView](cfg, "/api/users/"+cfg.Username)
			if err != nil {
				return err
			}
			if len(user.SSHPublicKeys) == 0 {
				fmt.Println("no keys registered — add one with `devws key add`")
				return nil
			}
			for _, k := range user.SSHPublicKeys {
				fmt.Printf("%s\t%s\t%s\n", k.Name, k.Type, k.Fingerprint)
			}
			return nil
		},
	}
}

func keyRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Remove a registered SSH public key",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if _, err := apiDelete[map[string]any](cfg, "/api/users/"+cfg.Username+"/keys/"+args[0]); err != nil {
				return err
			}
			fmt.Println("key removed")
			return nil
		},
	}
}

func tokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage your API tokens",
	}
	cmd.AddCommand(tokenCreateCmd(), tokenRevokeCmd())
	return cmd
}

func tokenCreateCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create an API token (shown once)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0]}
			if days > 0 {
				body["expiresDays"] = days
			}
			resp, err := apiPost[struct {
				Token string `json:"token"`
			}](cfg, "/api/users/"+cfg.Username+"/tokens", body)
			if err != nil {
				return err
			}
			fmt.Println(resp.Token)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "token lifetime in days (0 = no expiry)")
	return cmd
}

func tokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke NAME",
		Short: "Revoke an API token",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if _, err := apiDelete[map[string]any](cfg, "/api/users/"+cfg.Username+"/tokens/"+args[0]); err != nil {
				return err
			}
			fmt.Println("token revoked")
			return nil
		},
	}
}

func readAllStdin() ([]byte, error) {
	var buf [1 << 16]byte
	n, _ := os.Stdin.Read(buf[:])
	return buf[:n], nil
}
