# devws security model

This document explains what devws assumes about its environment, what it
defends against, and — importantly — what it deliberately does *not* defend
against. It is the decision record behind the security-relevant choices in
the controllers, the workspace image and the helm chart.

## Trust model

Three parties interact; devws draws the trust boundaries between them:

| Party | Trusted to | Not trusted to |
|---|---|---|
| **Platform operators** (helm installers, admins) | everything: they set images, quotas and passwords | — |
| **Users** (devws accounts) | use their own namespace, keep their SSH keys private | see another user's data, touch the platform namespace, escalate RBAC |
| **Workspace content** (code, repos, toolchains inside a workspace) | run as unprivileged uid 1000 in the user's own namespace | anything else |

Two consequences of the design:

- **The workspace image and everything cloned into a workspace are
  semi-trusted.** Users can override `spec.image` per workspace and run
  arbitrary build tooling; devws confines the *pod*, not the *processes*.
  A workspace compromised through its content is contained to the user's
  own namespace and home volume (see [pod security](#workspace-pods)).
- **Cluster credentials are out of scope.** Whoever can run pods on the
  nodes (other operators, other platform workloads) can reach workspace
  pods and volumes by normal kubernetes means. Use node-level isolation
  if that matters.

## Authentication and sessions

- **Passwords** are stored as bcrypt hashes in the `User` CR
  (`internal/auth/password.go`); nothing keeps plaintext.
- **Browser sessions** are stateless HMAC-SHA256 tokens
  (`username.expiry.signature`) signed with a key that lives in a Secret
  in the platform namespace (`internal/auth/session.go`). The signature
  key is generated once and reused across restarts, so sessions survive
  redeploys; rotate the Secret to revoke all sessions.
- **API tokens** (`devws_…`) are shown once, stored as bcrypt hashes with
  a searchable prefix (`internal/auth/tokens.go`).
- **SSH** authenticates against the public keys registered in the user's
  profile: the gateway matches the offered key against the `User` CR
  before routing, and the same keys end up in the workspace's
  `authorized_keys` (Secret mounted read-only, mode 0400).

## Isolation between users

- One **namespace per user** (`devws-<name>`), labeled
  `devws.io/managed=true` / `devws.io/user=<name>`.
- A **ResourceQuota** per namespace caps workspace count (one PVC per
  workspace), requested CPU/memory and PVC storage per the user's quotas
  (defaults: 5 workspaces, 8 CPU, 16Gi memory, 100Gi storage).
- A **NetworkPolicy** (`devws-default`) makes ingress default-deny and
  only allows traffic from the same namespace (user's own workspaces
  talking to each other) and from the platform namespace (the SSH
  gateway). **Egress is intentionally open** — development workspaces
  need to reach package registries, git remotes and APIs; denying that
  would break the product's purpose. Tighten it per-installation if your
  cluster has an egress policy story.
- Users never receive kubernetes credentials of any kind. All API access
  goes through the devws REST API / SSH gateway, which only exposes the
  user's own resources.

## RBAC

The apiserver's service account holds (see `charts/devws/templates/rbac.yaml`):

- full CRUD on `devws.io` CRDs — they are the datastore;
- CRUD on **namespaces**, pods, PVCs, secrets, ResourceQuotas and
  NetworkPolicies **cluster-wide** — user namespaces are created and
  populated by the controllers, and kubernetes RBAC cannot express "the
  namespace matching this user" scoping. This is significant power: the
  namespace `delete` verb is what makes user deletion work, and it is
  also what a compromised apiserver would abuse. Run devws on nodes and
  with audit logging appropriate to that;
- read-only `configmaps` in the release namespace (platform config);
- `coordination.k8s.io/leases` in the release namespace only (leader
  election — scoped by a namespaced Role, not the ClusterRole).

The bootstrap job gets a separate ClusterRole limited to creating the
admin `User` CR. The workspace pods themselves get **no service account
token at all** (`automountServiceAccountToken: false`) — they cannot talk
to the kubernetes API even though the default service account would
technically have no permissions anyway.

## Pod security

### The apiserver

The deployment runs as non-root uid 65532 with a read-only root
filesystem, all capabilities dropped, `allowPrivilegeEscalation: false`,
`seccompProfile: RuntimeDefault` and an emptyDir `/tmp` — see
`charts/devws/templates/deployment.yaml`. It needs no special
privileges: the SSH gateway is an ordinary unprivileged TCP server.

### Workspace pods

Workspace pods are the deliberate exception to "least privilege", and
every deviation is load-bearing:

- **`runAsUser: 0`, `allowPrivilegeEscalation: true`** — the workspace
  runs `sshd`, which must `setuid(1000)` for the logged-in user after
  key auth and uses privilege separation that requires root.
- **Capabilities**: all dropped, then the minimum sshd set added —
  `SETUID`/`SETGID` (session uid switching), `SYS_CHROOT` (pre-auth
  privilege separation), `CHOWN`/`DAC_OVERRIDE`/`FOWNER` (entrypoint
  normalizes home-volume ownership on first boot), `KILL`, `AUDIT_WRITE`
  (sshd runtime). Notably *not* added: `MKNOD`, `NET_BIND_SERVICE` (sshd
  listens on 2222), `SYS_ADMIN`.
- **`seccompProfile: RuntimeDefault`** at pod level.
- **`fsGroup: 1000`** so the persistent home volume is writable by the
  unprivileged user inside the workspace.
- **Init containers** are tighter than the main container: the
  `fix-permissions` init (volume ownership on first boot) runs as root
  but with `allowPrivilegeEscalation: false`; the `git-clone` init runs
  fully unprivileged as uid/gid 1000. The `authorized_keys` Secret is
  mounted read-only (mode 0400) and copied into place by the entrypoint,
  never written by the pod itself.
- **No service account token** (see above).

### PodSecurityAdmission decision record

User namespaces are labeled:

```yaml
pod-security.kubernetes.io/enforce: privileged
pod-security.kubernetes.io/audit:   baseline
pod-security.kubernetes.io/warn:    baseline
```

`enforce: privileged` is required because the sshd security context
above (root, APE, added capabilities) violates every stricter level.
`audit` and `warn` are kept at `baseline`: today's workspace pods pass
baseline (root + caps are `privileged`-level violations; the baseline
checks that remain pass), so anything *beyond* baseline — a user's
custom workspace image trying hostPath, host namespaces, etc. through
the pod spec — would be recorded in events and surfaced to kubectl
users. `enforce` cannot be stricter than `privileged` without either
dropping SSH access or moving sshd out of the workspace pod.

**Recommendation for operators**: also label the *platform* namespace
(the helm release namespace) with `pod-security.kubernetes.io/enforce:
restricted` — the apiserver and bootstrap job already comply. devws
leaves this to the installer so it never fights an existing namespace
label policy.

## Workspace images

The default image (`images/workspace`) is Ubuntu 24.04 with Go, Node,
Python and common build tooling. Two deliberate choices:

- **`spec.image` is user-overridable.** Any image can be pinned per
  workspace; treat images (default or custom) as *semi-trusted code
  shipped to the cluster* (see the trust model). Pin and vet what you
  allow, and combine with admission controllers if you must restrict
  registries.
- **`sudo` is passwordless for the in-container user.** This is a
  development convenience (installing packages without switching users)
  and is *not* a security boundary — the user already controls
  everything the pod can do. It grants nothing beyond the pod's own
  (capability-restricted, non-token-holding) context.

## What this design does not cover

- **Egress from workspaces is unrestricted** (see isolation above).
- **Node-level isolation**: all workspaces share the cluster's nodes;
  a node exploit escapes the model.
- **Supply chain**: image provenance (cosign etc.) is left to the
  installation; the chart does not enforce admission policy.
- **Web UI transport**: TLS for the REST API is expected to be
  terminated by your ingress (chart values `ingress.*`); cookies are
  `HttpOnly`+`SameSite=Lax` and get the `Secure` flag only when the
  apiserver itself serves TLS (`r.TLS != nil`) — behind a plain-HTTP
  connection to a TLS-terminating ingress the flag stays unset, so set
  it at the edge if your users are on hostile networks.