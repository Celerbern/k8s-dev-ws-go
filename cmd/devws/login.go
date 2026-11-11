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
		token   string
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

			cfg := &Config{ServerURL: server, SSHHost: sshHost, SSHPort: sshPort, Token: token}
			username := ""

			if token == "" {
				reader := bufio.NewReader(os.Stdin)
				fmt.Print("username: ")
				username, _ = reader.ReadString('\n')
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
				cfg.Token = resp.Token
			} else {
				// --token: use a token minted in the web UI (the usual
				// path for single sign-on users) and resolve who we are.
				me, err := apiGet[userView](cfg, "/api/auth/me")
				if err != nil {
					return fmt.Errorf("validating token: %w", err)
				}
				username = me.Username
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
			cfg.Username = username
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("logged in as %s (token stored in %s)\n", username, ConfigPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "SSH gateway host to use in generated ssh configs (server-advertised when unset)")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 0, "SSH gateway port to use in generated ssh configs (server-advertised when unset)")
	cmd.Flags().StringVar(&token, "token", "", "use an API token minted in the web UI (for single sign-on users) instead of username/password")
	return cmd
}
