# Connecting your IDE to a devws workspace

devws workspaces are plain SSH hosts. Any editor with remote development
over SSH works; this guide covers **VS Code Remote-SSH** and **JetBrains
Gateway** (IntelliJ, PyCharm, GoLand, …), the two setups devws is tested
with.

## Prerequisites (once)

1. An SSH key pair on your machine:

   ```bash
   ssh-keygen -t ed25519            # ~/.ssh/id_ed25519
   ```

2. The **public** key registered in devws — Profile → *SSH keys* in the web
   UI, or:

   ```bash
   devws key add --name laptop < ~/.ssh/id_ed25519.pub
   ```

3. SSH config entries for your workspaces:

   ```bash
   devws login https://devws.example.com --ssh-host ssh.devws.example.com
   devws workspace create main
   devws ssh-config --write
   ```

   `devws ssh-config --write` manages a marked block in `~/.ssh/config` and
   looks like this:

   ```ssh-config
   # --- devws workspaces (managed by `devws ssh-config --write`) ---

   # The gateway entry authenticates the ProxyJump below (ProxyJump opens a
   # separate connection that does NOT inherit the workspace host's
   # IdentityFile, so the gateway needs its own entry).
   Host ssh.devws.example.com
     Port 22
     User jdoe
     IdentityFile ~/.ssh/id_ed25519
     ServerAliveInterval 30
     ServerAliveCountMax 4
     StrictHostKeyChecking accept-new

   Host devws-main
     HostName main.devws-jdoe.ws.internal
     Port 2222
     User devws
     ProxyJump jdoe@ssh.devws.example.com
     ServerAliveInterval 30
     ServerAliveCountMax 4
     StrictHostKeyChecking accept-new
     IdentityFile ~/.ssh/id_ed25519
   ```

4. Sanity check before blaming the IDE:

   ```bash
   ssh devws-main            # should land in a shell in /home/devws
   ```

> **Key changes take a moment**: newly registered SSH keys reach a
> *running* workspace within a minute or two (kubelet secret sync plus the
> workspace's authorized_keys sync loop). Stop/start the workspace to
> apply a new key immediately.

## VS Code (Remote-SSH)

1. Install the **Remote - SSH** extension
   (`ms-vscode-remote.remote-ssh`) in your local VS Code.
2. Open the command palette (`F1` / `Cmd+Shift+P`) → **Remote-SSH:
   Connect to Host…** → pick `devws-main`.
3. Either open folder `/home/devws`, or a specific project like
   `/home/devws/Hello-World` that was pre-cloned for you.
4. First connect: VS Code downloads its server component into `~/.vscode-server`
   on the workspace volume — this persists, so subsequent connects are fast.

One-liner from a terminal:

```bash
devws code main
# or directly:
code --folder-uri vscode-remote://ssh-remote+devws-main/home/devws
```

Notes:

- Extensions are installed **per workspace** (Extensions view → "Install
  in SSH: devws-main"). Your toolchains (`go`, `node`, `python3`) are
  already in the image.
- The Remote-SSH connection uses the same `devws-main` host entry, so all
  settings there (identity file, keepalives) apply.

## JetBrains Gateway (IntelliJ, PyCharm, GoLand, …)

1. Install [JetBrains Gateway](https://www.jetbrains.com/remote-development/gateway/)
   (free; the IDE backend needs a license on first use).
2. Welcome screen → **New connection** → **SSH connection**:
   - Host: `devws-main` (the alias from `~/.ssh/config` — Gateway honors
     your SSH config, including `ProxyJump`)
   - Port: `2222`, Username: `devws`
   - Authentication: key pair → your private key file
3. **Check connection and continue** — Gateway verifies the environment.
4. Pick the IDE version and backend location (the default under
   `~/.cache/JetBrains` persists on the workspace volume).
5. Choose the project directory, e.g. `/home/devws` or a pre-cloned repo,
   and open it.

The full IDE then runs *inside* your workspace; the local window is a thin
client. Your code, run configurations and indexes survive workspace
stop/start because they live on the persistent home volume.

## Terminal (and everything else)

`ssh devws-main` is a normal SSH session — `git`, `tmux`, builds and test
runs all work, and can run alongside VS Code / JetBrains sessions.

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `Permission denied (publickey)` right after adding a key | Key propagation delay (see note above). Wait ~2 min or restart the workspace. |
| `Permission denied (publickey)` persisting | Wrong key: the *same* key must be registered in devws (gateway auth) and allowed by the workspace (it is mounted from your profile — same key). Check `devws key list`. |
| `Could not resolve hostname` for the gateway | Run `devws ssh-config --write` with `--ssh-host`/`--ssh-port` matching your install (`devws login --ssh-host …`). |
| Host key verification failed | First connect prompts for the workspace host key; `StrictHostKeyChecking accept-new` (in the generated config) accepts it automatically. A *changed* host key means the workspace volume was recreated. |
| VS Code hangs on "Setting up SSH Host" | Check the workspace is Running in the UI; inspect logs in the workspace detail view (container `workspace`) or `devws workspace logs main`. |
| Connection drops when the workspace stops | Expected — stop deletes the pod. Start the workspace and reconnect; unsaved IDE state is lost but files are not. |
| Cloned repo missing | Check the workspace status: a failed `git clone` fails the workspace with `git clone failed` and the reason is in the logs, container `git-clone`. Fix `gitRepos` and restart the workspace. |

## How the SSH path works (background)

```
you ──ssh──▶ gateway (devws-apiserver :22)      # pubkey auth vs your User CR
        └──ProxyJump/direct-tcpip──▶ pod :2222  # gateway checks the target
                                                # is in YOUR namespace
```

The gateway only forwards connections to workspace pods in the
authenticated user's namespace, so one user can never reach another's
workspaces. Inside the pod, `sshd` re-authenticates the same key via the
`authorized_keys` mounted from your profile.