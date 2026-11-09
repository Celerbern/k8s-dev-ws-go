package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func loginCmd() *cobra.Command {
	var (
		sshHost string
		sshPort int
	)
	cmd := &cobra.Command{
		Use:   "login SERVER_URL",
		Short: "Log in to a devws server and store an API token",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			server := strings.TrimRight(args[0], "/")
			if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
				server = "https://" + server
			}

			reader := bufio.NewReader(os.Stdin)
			fmt.Print("username: ")
			username, _ := reader.ReadString('\n')
			username = strings.TrimSpace(username)
			fmt.Print("password: ")
			passwordBytes, err := term.ReadPassword(int(syscall.Stdin))
			fmt.Println()
			if err != nil {
				return err
			}
			password := string(passwordBytes)
			if username == "" || password == "" {
				return fmt.Errorf("username and password are required")
			}

			cfg := &Config{ServerURL: server, SSHHost: sshHost, SSHPort: sshPort}
			resp, err := apiPost[struct {
				Token string `json:"token"`
				Name  string `json:"name"`
			}](cfg, "/api/auth/token", map[string]string{
				"username": username,
				"password": password,
				"name":     "cli",
			})
			if err != nil {
				return err
			}

			// Persist the advertised SSH endpoint from the server unless
			// the user overrode it.
			if sshHost == "" || sshPort == 0 {
				if pc, err := apiGet[platformConfig](cfg, "/api/config"); err == nil {
					if sshHost == "" {
						cfg.SSHHost = pc.SSHHost
					}
					if sshPort == 0 {
						cfg.SSHPort = pc.SSHPort
					}
				}
			}
			cfg.Token = resp.Token
			cfg.Username = username
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("logged in as %s (token %q stored in %s)\n", username, resp.Name, ConfigPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "SSH gateway host to use in generated ssh configs (server-advertised when unset)")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 0, "SSH gateway port to use in generated ssh configs (server-advertised when unset)")
	return cmd
}
