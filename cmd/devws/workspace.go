package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Manage workspaces",
	}
	cmd.AddCommand(workspaceListCmd(), workspaceCreateCmd(), workspaceGetCmd(), workspaceStartCmd(), workspaceStopCmd(), workspaceDeleteCmd(), workspaceLogsCmd(), workspaceEventsCmd())
	return cmd
}

func workspaceListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your workspaces (or all of them, for admins)",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			path := "/api/workspaces"
			if all {
				path += "?scope=all"
			}
			items, err := apiGet[[]workspaceView](cfg, path)
			if err != nil {
				return err
			}
			if len(*items) == 0 {
				fmt.Println("no workspaces — create one with `devws workspace create <name>`")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 4, 4, 2, ' ', 0)
			if all {
				fmt.Fprintln(w, "NAME\tUSER\tPHASE\tCLASS\tSSH HOST\tMESSAGE")
			} else {
				fmt.Fprintln(w, "NAME\tPHASE\tCLASS\tSSH HOST\tMESSAGE")
			}
			for _, ws := range *items {
				if all {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", ws.Name, ws.User, ws.Phase, ws.Class, ws.SSHHost, ws.Message)
				} else {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", ws.Name, ws.Phase, ws.Class, ws.SSHHost, ws.Message)
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list workspaces of all users (admin)")
	return cmd
}

func workspaceGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get NAME",
		Short: "Show one workspace in detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			ws, err := apiGet[workspaceView](cfg, "/api/workspaces/"+args[0])
			if err != nil {
				return err
			}
			fmt.Printf("name:       %s\n", ws.Name)
			fmt.Printf("user:       %s (%s)\n", ws.User, ws.Namespace)
			fmt.Printf("phase:      %s\n", ws.Phase)
			if ws.Message != "" {
				fmt.Printf("message:    %s\n", ws.Message)
			}
			fmt.Printf("class:      %s\n", ws.Class)
			if ws.Image != "" {
				fmt.Printf("image:      %s\n", ws.Image)
			}
			if ws.Storage != "" {
				fmt.Printf("storage:    %s\n", ws.Storage)
			}
			if len(ws.GitRepos) > 0 {
				fmt.Printf("git repos:  %s\n", strings.Join(ws.GitRepos, ", "))
			}
			if ws.PodName != "" {
				fmt.Printf("pod:        %s\n", ws.PodName)
			}
			fmt.Printf("ssh host:   %s\n", ws.SSHHost)
			fmt.Printf("created:    %s\n", ws.CreatedAt)
			fmt.Printf("connect:    devws ssh %s   (VS Code: devws code %s)\n", ws.Name, ws.Name)
			return nil
		},
	}
}

func workspaceCreateCmd() *cobra.Command {
	var (
		class    string
		image    string
		storage  string
		gitRepos []string
	)
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0]}
			if class != "" {
				body["class"] = class
			}
			if image != "" {
				body["image"] = image
			}
			if storage != "" {
				body["storage"] = storage
			}
			if len(gitRepos) > 0 {
				body["gitRepos"] = gitRepos
			}
			ws, err := apiPost[workspaceView](cfg, "/api/workspaces", body)
			if err != nil {
				return err
			}
			fmt.Printf("workspace %q created (phase: %s)\n", ws.Name, ws.Phase)
			fmt.Println("connect with:  devws ssh " + ws.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&class, "class", "", "workspace class (server default when empty)")
	cmd.Flags().StringVar(&image, "image", "", "container image override")
	cmd.Flags().StringVar(&storage, "storage", "", "persistent volume size (e.g. 20Gi)")
	cmd.Flags().StringSliceVar(&gitRepos, "git-repo", nil, "public git repo to clone on first start (repeatable)")
	return cmd
}

func workspaceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start NAME",
		Short: "Start a stopped workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return simpleWorkspaceAction("start", args[0])
		},
	}
}

func workspaceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop NAME",
		Short: "Stop a workspace (keeps your data)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return simpleWorkspaceAction("stop", args[0])
		},
	}
}

func workspaceDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete NAME",
		Aliases: []string{"rm"},
		Short:   "Delete a workspace and its persistent volume",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if !yes {
				fmt.Printf("This deletes workspace %q INCLUDING all files on its volume. Continue? [y/N] ", args[0])
				var answer string
				fmt.Scanln(&answer)
				if !strings.EqualFold(strings.TrimSpace(answer), "y") {
					return fmt.Errorf("aborted")
				}
			}
			if _, err := apiDelete[map[string]any](cfg, "/api/workspaces/"+args[0]); err != nil {
				return err
			}
			fmt.Println("workspace deleted")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation")
	return cmd
}

func simpleWorkspaceAction(action, name string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if _, err := apiPost[map[string]any](cfg, "/api/workspaces/"+name+"/"+action, nil); err != nil {
		return err
	}
	fmt.Println(action + " requested for workspace " + name)
	return nil
}

func workspaceLogsCmd() *cobra.Command {
	var (
		follow    bool
		tail      int
		container string
		previous  bool
	)
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Stream workspace pod logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			// Ctrl-C aborts the request instead of leaving a dangling
			// follow connection on the server.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			q := url.Values{}
			q.Set("tail", strconv.Itoa(tail))
			if container != "" {
				q.Set("container", container)
			}
			if follow {
				q.Set("follow", "true")
			}
			if previous {
				q.Set("previous", "true")
			}
			stream, err := apiStream(ctx, cfg, "/api/workspaces/"+args[0]+"/logs?"+q.Encode())
			if err != nil {
				return err
			}
			defer stream.Close()
			_, err = io.Copy(os.Stdout, stream)
			// Ctrl-C on a live tail cancels the stream mid-read; that is
			// a normal way to end it (Go 1.27 reports the signal as a
			// context.Canceled cause), not an error.
			if err != nil && errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep the stream open (live tail)")
	cmd.Flags().IntVar(&tail, "tail", 200, "number of trailing lines (0 = all)")
	cmd.Flags().StringVar(&container, "container", "", "container to read (default \"workspace\"; init containers: git-clone, fix-permissions)")
	cmd.Flags().BoolVar(&previous, "previous", false, "read the previous (restarted) container instance")
	return cmd
}

func workspaceEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events NAME",
		Short: "Show kubernetes events for a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			items, err := apiGet[[]eventView](cfg, "/api/workspaces/"+args[0]+"/events")
			if err != nil {
				return err
			}
			if len(*items) == 0 {
				fmt.Println("no events recorded")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 4, 4, 2, ' ', 0)
			fmt.Fprintln(w, "LAST SEEN\tTYPE\tREASON\tOBJECT\tMESSAGE")
			for _, ev := range *items {
				lastSeen := ev.LastSeen
				if t, err := time.Parse(time.RFC3339, ev.LastSeen); err == nil {
					lastSeen = humanAge(time.Since(t))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", lastSeen, ev.Type, ev.Reason, ev.Object, ev.Message)
			}
			return w.Flush()
		},
	}
}

// humanAge renders a duration the way people read event ages.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// gatewayHostPort resolves the SSH gateway endpoint from the CLI config.
func gatewayHostPort(cfg *Config) (string, int) {
	sshHost := cfg.SSHHost
	if sshHost == "" {
		sshHost = "ssh.devws.local"
	}
	sshPort := cfg.SSHPort
	if sshPort == 0 {
		sshPort = 22
	}
	return sshHost, sshPort
}

// gatewayBlock renders the ~/.ssh/config entry that authenticates to the
// gateway itself. ProxyJump opens a separate connection that does not
// inherit the workspace host's IdentityFile, so the gateway needs its own
// entry.
func gatewayBlock(cfg *Config, identity string) string {
	host, port := gatewayHostPort(cfg)
	return fmt.Sprintf(`Host %s
  Port %d
  User %s
  IdentityFile %s
  ServerAliveInterval 30
  ServerAliveCountMax 4
  StrictHostKeyChecking accept-new
`, host, port, cfg.Username, identity)
}

// sshConfigBlock renders the ~/.ssh/config entry for a workspace.
func sshConfigBlock(cfg *Config, ws workspaceView, identity string) string {
	host, port := gatewayHostPort(cfg)
	gateway := fmt.Sprintf("%s@%s", cfg.Username, host)
	if port != 22 {
		gateway = fmt.Sprintf("%s@%s:%d", cfg.Username, host, port)
	}
	return fmt.Sprintf(`Host devws-%s
  HostName %s
  Port 2222
  User devws
  ProxyJump %s
  ServerAliveInterval 30
  ServerAliveCountMax 4
  StrictHostKeyChecking accept-new
  IdentityFile %s
`, ws.Name, ws.SSHHost, gateway, identity)
}

func sshConfigCmd() *cobra.Command {
	var (
		write    bool
		identity string
	)
	cmd := &cobra.Command{
		Use:   "ssh-config",
		Short: "Print (or write) ~/.ssh/config entries for your workspaces",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			items, err := apiGet[[]workspaceView](cfg, "/api/workspaces")
			if err != nil {
				return err
			}
			sort.Slice(*items, func(i, j int) bool { return (*items)[i].Name < (*items)[j].Name })
			identityFile := identity
			if identityFile == "" {
				identityFile = "~/.ssh/id_ed25519"
			}
			var sb strings.Builder
			sb.WriteString("# --- devws workspaces (managed by `devws ssh-config --write`) ---\n\n")
			sb.WriteString(gatewayBlock(cfg, identityFile))
			for _, ws := range *items {
				sb.WriteString("\n" + sshConfigBlock(cfg, ws, identityFile))
			}
			sb.WriteString("# --- end devws workspaces ---\n")
			out := sb.String()
			fmt.Print(out)
			if write {
				sshPath := homeDir() + "/.ssh/config"
				existing := ""
				if raw, err := os.ReadFile(sshPath); err == nil {
					existing = string(raw)
				}
				// Replace a previously managed block if present.
				if i := strings.Index(existing, "# --- devws workspaces"); i >= 0 {
					if j := strings.Index(existing[i:], "# --- end devws workspaces ---"); j >= 0 {
						existing = existing[:i] + existing[i+j+len("# --- end devws workspaces ---"):]
					}
				}
				existing = strings.TrimRight(existing, "\n") + "\n\n" + out
				if err := os.MkdirAll(homeDir()+"/.ssh", 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(sshPath, []byte(existing), 0o600); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "written to %s\n", sshPath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&write, "write", false, "append the entries to ~/.ssh/config")
	cmd.Flags().StringVar(&identity, "identity", "", "identity file to use (default ~/.ssh/id_ed25519)")
	return cmd
}

func sshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh NAME [-- command...]",
		Short: "Open an SSH session to a workspace",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := "devws-" + args[0]
			sshArgs := append([]string{"ssh", host}, args[1:]...)
			return runCommand("ssh", sshArgs[1:])
		},
	}
}

func codeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "code NAME",
		Short: "Open a workspace in local VS Code (Remote-SSH)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			host := "devws-" + args[0]
			uri := "vscode-remote://ssh-remote+" + host + "/home/devws"
			fmt.Println("Opening VS Code at", uri)
			return runCommand("code", []string{"--folder-uri", uri})
		},
	}
}
