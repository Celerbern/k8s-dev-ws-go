# devws — Kubernetes Development Workspaces

devws lets users develop applications **inside a Kubernetes cluster**:

- **Per-user workspaces** — each user gets a dedicated namespace; each
  workspace is a pod with a **persistent home directory** (`/home/devws`)
  that survives stop/start and pod restarts.
- **Local IDE support** — connect with **VS Code Remote-SSH** and
  **JetBrains Gateway** through an SSH gateway. No IDE plugins required.
- **Git support** — git + ssh toolchains in every workspace, keys and
  credentials persist on the workspace volume, optional repo pre-clone.
- **Workspace image catalog** — admins curate the images users can pick
  (per-language toolchains, pinned tags); regular users are restricted to
  the catalog, admins may use any image reference.
- **Private registry support** — one `imagePullSecrets` list drives the
  control plane and workspace pods; secrets are synced into every user
  namespace and rotated live.
- **Admin console** — web UI for managing users, quotas, workspaces and
  the image catalog, live usage vs quotas, and a cross-namespace audit log.
- **Fully kubernetes-native** — users, workspaces and catalog images are
  custom resources (CRDs); there is **no database** to operate. Install
  with Helm.

```
 Local machine                        k8s cluster
 ┌─────────────────┐       ┌──────────────────────────────────────────┐
 │ VS Code         │  :22  │  devws-system (platform namespace)       │
 │  Remote-SSH ────┼───────┼─▶ apiserver: SSH gateway (auth+routing)   │
 │ JetBrains       │       │           REST API + web UI (Vue 3)      │
 │  Gateway ───────┼───────┼─▶ controllers: User, Workspace            │
 │ Browser         │ HTTPS │                │                          │
 │  dashboard ─────┼───────┼─▶ k8s API / etcd ◀── source of truth      │
 │ `devws` CLI ────┼───────┤                                           │
 └─────────────────┘       │  devws-<user> (namespace per user)       │
                          │   ResourceQuota · NetworkPolicy           │
                          │   Pod (sshd + toolchains) · PVC (home)    │
                          └──────────────────────────────────────────┘
```

## Quickstart on kind

Prerequisites: docker, kind, helm, go, node/npm, and `kubectl`.

```bash
# 1. Create a cluster (maps host port 2222 to the SSH gateway)
kind create cluster --name devws --config hack/kind-config.yaml

# 2. Build images and load them into kind
make docker-apiserver docker-workspace
kind load docker-image devws-apiserver:dev --name devws
kind load docker-image devws-workspace:dev --name devws

# 3. Install devws
helm upgrade --install devws charts/devws \
  --namespace devws-system --create-namespace \
  --set image.repository=devws-apiserver --set image.tag=dev \
  --set image.pullPolicy=Never \
  --set workspaceImage=devws-workspace:dev \
  --set config.sshHost=127.0.0.1:2222 \
  --set service.ssh.type=NodePort --set service.ssh.nodePort=30022 \
  --set admin.password=$(openssl rand -base64 18 | tr -d '/+=' | head -c 16)

kubectl -n devws-system rollout status deploy/devws-apiserver

# 4. Open the web UI (admin / your admin.password)
kubectl -n devws-system port-forward svc/devws-web 8080:80
open http://localhost:8080
```

Or run the whole flow as one command: **`make kind-e2e`** (creates the
cluster if missing, installs, and smoke-tests everything including SSH).

## Single sign-on (OIDC)

Browser login can be delegated to an OpenID Connect provider (Keycloak,
Dex, Okta, …). Enable it at install time with the `oidc.*` values:

```bash
helm upgrade --install devws charts/devws ... \
  --set oidc.enabled=true \
  --set oidc.issuerUrl=https://dex.example.com/dex \
  --set oidc.clientId=devws \
  --set oidc.clientSecret=<secret> \
  --set oidc.redirectUrl=https://devws.example.com/api/auth/oidc/callback
```

The login page then offers a "Sign in with single sign-on" button.
Users are never auto-provisioned: an admin pre-creates each user with
auth method *OIDC* (the username must match the provider's username
claim), and unrecognized identities are rejected. The first admin is
always bootstrapped locally, and admins can be OIDC users too. See
[docs/oidc.md](docs/oidc.md) for details and provider caveats.

## Using workspaces

In the web UI (or with the CLI):

1. **Admin** creates a user (username, password, quotas) and curates the
   **workspace image catalog** (Admin → Images, or `devws image add`).
2. The user signs in and **adds an SSH public key** (Profile → SSH keys).
3. The user creates a **workspace** (name + size class; optionally an
   image from the catalog and public git repos to pre-clone).
4. Connect your IDE — the user's `~/.ssh/config` gets an entry per
   workspace:

   ```
   Host devws-main
     HostName main.devws-jdoe.ws.internal
     Port 2222
     User devws
     ProxyJump jdoe@ssh.example.com
     IdentityFile ~/.ssh/id_ed25519
   ```

   - **VS Code**: install *Remote - SSH*, connect to host `devws-main`.
   - **JetBrains**: Gateway → SSH → `devws-main`.
   - **Terminal**: `ssh devws-main`.

   Step-by-step IDE setup (including troubleshooting) lives in
   [docs/ide-setup.md](docs/ide-setup.md).

With the CLI:

```bash
go build -o bin/devws ./cmd/devws
bin/devws login http://localhost:8080 --ssh-host 127.0.0.1 --ssh-port 2222
bin/devws workspace create main
bin/devws key add --name laptop < ~/.ssh/id_ed25519.pub
bin/devws ssh-config --write        # writes ~/.ssh/config
ssh devws-main
bin/devws code main                 # opens VS Code directly
bin/devws workspace logs main -f    # live log tail
bin/devws workspace events main     # what the controller has been doing
```

The image catalog is managed with `devws image` (changes require an
admin login; everyone may `list`):

```bash
bin/devws image add python-3-12 --image ghcr.io/acme/devws-python --tag 3.12 \
  --description "Python 3.12 toolchain"
bin/devws image list
bin/devws image deprecate python-3-12   # hide from the picker (--undo to restore)
bin/devws image remove python-3-12      # existing workspaces keep running it
```

Your home directory (`/home/devws`) persists across stop/start; `git clone`,
ssh keys, IDE caches and installed tools all survive. Workspaces created
with `gitRepos` (web UI or `devws workspace create --git-repo <url>`) clone
those repos on first start; a failed clone fails the workspace with the
reason in its status, events and the `git-clone` container logs.

The web UI has a per-workspace detail view: connection card with copy-paste
ssh config and IDE guides, a live log viewer (per container, follow mode)
and the workspace's kubernetes events.

## How SSH routing works

The gateway is a standard SSH server: users authenticate with the public key
registered in their profile (matched against their `User` CR). IDEs connect
through it like a jump host (`ProxyJump`); the gateway authorizes each
forwarded connection and only allows destinations inside the authenticated
user's own namespace. The workspace pod runs a regular `sshd` on port 2222
whose `authorized_keys` (the user's registered keys) is mounted from a
Secret managed by the controller — the workspace itself has no Kubernetes
API access. Key changes reach a **running** workspace within a minute or two
(kubelet secret sync + the workspace's own authorized_keys sync loop);
restart the workspace to apply them immediately.

## Architecture

| Component | What it does |
|---|---|
| `cmd/devws-apiserver` | Single Go binary: REST API + embedded Vue UI, `User`/`Workspace` controllers, SSH gateway |
| `cmd/devws` | CLI: login, workspace lifecycle + logs/events, image catalog, `ssh-config`, `key`, `token`, admin bootstrap |
| `api/v1alpha1` | `User`, `Workspace` and `WorkspaceImage` CRDs (kubernetes is the only datastore) |
| `internal/controllers` | User → namespace + ResourceQuota + NetworkPolicy + pull-secret sync; Workspace → Pod + PVC + ssh Secret |
| `internal/sshgateway` | Public-key auth + namespace-scoped TCP routing |
| `web/` | Vue 3 + Vite SPA (embedded into the Go binary) |
| `images/workspace` | Ubuntu 24.04 + sshd + Go/Node/Python toolchains |
| `charts/devws` | Helm chart incl. CRDs, image catalog seed and admin bootstrap job |

Workspace classes (CPU/RAM/disk templates), the default workspace image,
storage class and per-user quotas are Helm values rendered into a ConfigMap.

## Operations

**Workspace images** — the catalog (`WorkspaceImage` CRs, cluster-scoped)
defines the images regular users can pick when creating a workspace;
leaving the image empty keeps the platform default (`workspaceImage`
value). Manage it via the admin console's *Images* tab or
`devws image add|remove|deprecate`. Deprecated entries disappear from the
creation picker but keep serving existing workspaces. Entries can also be
seeded from helm values (`workspaceImages: [{name, image, description,
deprecated}]`); those are helm-managed — `helm upgrade` reverts manual
edits to them, while API-created entries are free-form. Image references
must carry an explicit `:tag` or `@sha256:` digest so workspaces are
reproducible.

**Private registries** — set the chart's top-level `imagePullSecrets`
list to pull both the control-plane and workspace images from private
registries; the referenced docker-registry Secrets must live in the
release namespace:

```bash
kubectl -n devws-system create secret docker-registry my-registry \
  --docker-server=ghcr.io --docker-username=user --docker-password=pass
helm upgrade devws charts/devws --reuse-values --set "imagePullSecrets[0]=my-registry"
```

The user controller copies each configured secret into every
`devws-<user>` namespace (kubelet only reads pull secrets from the pod's
own namespace) and a watch propagates credential rotation without pod
restarts. Changing the *list* requires an apiserver restart — the
`checksum/config` annotation rolls the deployment automatically.

**Metrics** — the apiserver serves prometheus metrics on
`--metrics-addr` (`metrics.enabled=true` in values, port 8081): request
counters (`devws_api_requests_total{method,route,code}`), gateway
connection gauges, and per-phase workspace/user counts from the
controllers' caches. With the prometheus operator installed, set
`metrics.serviceMonitor.enabled=true` to get a ServiceMonitor scraping
the `devws.io/metrics: "true"` labeled service.

**Logging** — application logs (API, OIDC, SSH gateway) use slog at
`--log-level` (`logLevel` in values, default `info`). Set
`--set logLevel=debug` for per-request `/api/` access lines (method,
path, status, duration — never query strings, since the OIDC callback
carries a one-time authorization code) and OIDC login-flow breadcrumbs,
including the authorization-code prefix used to correlate a failed
login with the identity provider's logs. Controller-runtime components
log through zap and keep their own `-zap-*` flags.

**High availability** — the web API and SSH gateway are stateless and
serve from every replica; the controllers elect a leader via a Lease in
the release namespace. Scale with `replicaCount` (leader election is on
by default, `leaderElection=false` for single-replica setups without
Lease permissions). Deleting the leader pod fails the lease over within
seconds and the API keeps serving throughout.

**Usage & audit** — the admin console's *Usage* tab shows per-user
workspace/CPU/memory/storage consumption against quotas (with meter
bars); users see their own consumption on their profile page. The
*Audit log* tab lists recent kubernetes events across all user
namespaces, attributed to users — workspace lifecycle, scheduling and
quota denials, pod restarts.

A security review of the design (trust model, workspace pod security
context, PSA decision record, RBAC power) lives in
[docs/security.md](docs/security.md).

## Development

```bash
make build      # build web + both binaries into bin/
make generate   # regenerate deepcopy + CRDs after editing api/
make test       # unit tests (controllers, api, auth, ssh gateway, metrics)
make vet fmt
make kind-e2e   # full end-to-end run on kind
```

Notes:

- The committed `internal/web/dist` contains only a placeholder; `make
  build` (or `make build-web`) builds the real SPA into it.
- kind's default CNI does not enforce NetworkPolicies; install e.g. Calico
  if you want to verify cross-namespace isolation in e2e tests.
- On clusters without a LoadBalancer expose SSH via `NodePort` (see values)
  or tunnel port 22 through your ingress controller.

**CI** (`.github/workflows/`) runs on every push and PR:

- `ci.yml` — gofmt/vet, unit tests, web build, `helm lint` +
  `ct lint` (chart-testing, config in `hack/ct.yaml`).
- `e2e.yml` — the full [kind e2e suite](#quickstart-on-kind)
  (`hack/e2e-kind.sh`) plus a `ct install` of the chart with
  `charts/devws/ci/kind-values.yaml` into a fresh kind cluster.

## Status

M1 (walking skeleton) is complete: helm-installable control plane, admin
bootstrap, user/workspace lifecycle, SSH gateway, persistence, dashboard and
admin console.

M2 (IDE experience) is complete: workspace logs (live tail, per container)
and events in the UI and CLI, git repository pre-clone with failure
surfacing, connection cards with IDE guides, and a tested VS Code
Remote-SSH / JetBrains Gateway setup ([docs/ide-setup.md](docs/ide-setup.md)).

M3 (hardening & ops) is complete: prometheus metrics (+ optional
ServiceMonitor), leader election and multi-replica failover, quota usage
views (admin + profile), a cross-namespace audit log, pod security
admission labels with a documented decision record
([docs/security.md](docs/security.md)), and CI (unit, chart lint/install,
full kind e2e).

The workspace image catalog + private registry support adds the
`WorkspaceImage` CRD with admin CRUD (UI, CLI, helm seeding), the
create/patch image policy (regular users: catalog only; admins: any
ref), and platform-wide `imagePullSecrets` synced into every user
namespace with live rotation.

## License

Apache-2.0 (see headers); LICENSE file to be added.