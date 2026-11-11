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
  semi-trusted.** Users pick `spec.image` per workspace from the admin
  curated catalog (admins may pin any reference) and run arbitrary build
  tooling; devws confines the *pod*, not the *processes*.
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

### OIDC single sign-on (optional)

When the `devws-oidc` Secret is present (chart values `oidc.*`), the
login page additionally offers single sign-on through the configured
identity provider. The design points:

- **Authorization code flow with PKCE (S256), state and nonce.** The
  state, nonce and PKCE verifier live in a short-lived (10 min)
  HMAC-signed cookie scoped to the callback path
  (`internal/oidc/oidc.go`), not in apiserver memory — any replica can
  complete a login, and there is no flow state to protect on disk.
  The ID token is fully verified (signature via the provider's JWKS,
  issuer, audience, expiry) plus the nonce, rebound to the transaction
  cookie.
- **No auto-provisioning.** A successful OIDC login only maps the
  configured username claim (default `preferred_username`, lowercased)
  to an existing `User` CR with `spec.authMethod: oidc`. If no such user
  exists — or the user is a local/password user — the login is
  rejected and the browser lands on the login page with an error. The
  identity provider can therefore never widen the user set on its own;
  only admins create accounts.
- **Auth methods are exclusive.** An `oidc` user has no password hash:
  password login, password reset and CLI token issue via password are
  all refused. Admins can switch a user's method (`oidc→local` requires
  setting a new password immediately; `local→oidc` drops the password),
  but never their own — a mismatched username claim would otherwise
  lock the (only) admin out of the platform. The first admin is always
  bootstrapped locally (helm `admin.*` values), so a broken IdP never
  locks everyone out.
- **The session issued is identical** to a password login (same
  stateless cookie, same per-request re-checks of `role` and
  `disabled`), so revocation semantics are unchanged: disable the user
  or rotate the session key Secret. Logout is local (cookie clearing)
  and does not end the session at the provider.
- **Configuration** (issuer, client id/secret, redirect URL, username
  claim, scopes) is read once at startup from the `devws-oidc` Secret in
  the platform namespace; discovery runs against the issuer at startup
  so a misconfigured provider fails the deployment loudly. See
  [oidc.md](oidc.md) for setup.

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

- **`spec.image` is gated by the image catalog.** Regular users may only
  pin images offered by the `WorkspaceImage` catalog (plus the platform
  default); admins may use any reference. Whatever runs is *semi-trusted
  code shipped to the cluster* (see the trust model): admins should only
  catalog images they vet, pin tags/digests (references must carry an
  explicit `:tag` or `@sha256:` digest), and combine with admission
  controllers if they must restrict registries. Existing workspaces are
  never retro-validated — deprecating or removing a catalog entry does
  not stop them.
- **`sudo` is passwordless for the in-container user.** This is a
  development convenience (installing packages without switching users)
  and is *not* a security boundary — the user already controls
  everything the pod can do. It grants nothing beyond the pod's own
  (capability-restricted, non-token-holding) context.

### Private registry pull secrets

The configured `imagePullSecrets` (chart values, top-level) are copied
verbatim into every `devws-<user>` namespace so kubelet can read them
where the pods run. Workspace pods get no service account token, so
devws users cannot read the copies through the platform; anyone with
direct kubernetes access to a user namespace can, though — treat the
registry credentials as cluster-tenant-visible, use a read-only or
pull-limited robot account for them, and rotate via the platform
namespace secret (propagation to all user namespaces is automatic).

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