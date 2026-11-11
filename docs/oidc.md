# OIDC single sign-on setup

devws can delegate browser login to any OpenID Connect provider
(Keycloak, Dex, Okta, Entra ID, Google, …). This document explains the
configuration, how identities map to devws users, and the per-provider
caveats.

## How it works

1. The login page shows a **"Sign in with single sign-on"** button when
   OIDC is enabled.
2. Clicking it starts an OAuth2 **authorization code flow with PKCE
   (S256)**, protected by a signed `state` + `nonce` transaction cookie.
3. After the provider authenticates the user, devws verifies the ID
   token and reads the configured **username claim** (default
   `preferred_username`). The claim value (lowercased) must **exactly
   match** an existing devws username.
4. That user must have `spec.authMethod: oidc`. Users are **never
   auto-provisioned** — if no matching OIDC user exists, the login is
   rejected ("no devws account matches your identity").
5. On success the user gets the same browser session as a password
   login. Roles, quotas and SSH keys work exactly as for local users;
   admins can be OIDC users too.

The first admin is always bootstrapped locally with a password (helm
values `admin.*`), independently of OIDC.

## Configuration

Enable OIDC at install time:

```bash
helm upgrade --install devws charts/devws \
  --namespace devws-system --create-namespace \
  --set oidc.enabled=true \
  --set oidc.issuerUrl=https://dex.example.com/dex \
  --set oidc.clientId=devws \
  --set oidc.clientSecret=$(openssl rand -base64 18) \
  --set oidc.redirectUrl=https://devws.example.com/api/auth/oidc/callback
```

| Value | Meaning |
|---|---|
| `oidc.issuerUrl` | Issuer identifier; discovery (`/.well-known/openid-configuration`) happens at apiserver startup — a broken issuer fails the rollout. |
| `oidc.clientId` / `oidc.clientSecret` | Client credentials registered at the provider. Stored in the `devws-oidc` Secret. |
| `oidc.redirectUrl` | The exact callback URL to register at the provider: `https://<devws-host>/api/auth/oidc/callback`. |
| `oidc.usernameClaim` | ID token claim matched against the devws username (default `preferred_username`). |
| `oidc.scopes` | Comma-separated scopes; `openid` is always included. Defaults to `openid profile email`. |

Changing these values rolls the apiserver deployment (the configuration
is read once at startup).

## Creating OIDC users

Users must be pre-created by an admin before they can sign in:

- **Web UI**: Admin → Users → **+ New user** → Authentication: *Single
  sign-on (OIDC)*. The username must match the provider's username
  claim.
- **CLI**:

  ```bash
  devws user create --username jdoe --auth-method oidc --email jdoe@example.com
  ```

- **Kubernetes** (the User CR is the source of truth):

  ```yaml
  apiVersion: devws.io/v1alpha1
  kind: User
  metadata:
    name: jdoe
  spec:
    authMethod: oidc
    role: user        # or admin
    email: jdoe@example.com
  ```

An OIDC user has no password. An admin can switch the method later
(**Edit user** in the UI or `PATCH /api/users/{name}`):
`local→oidc` removes the password, `oidc→local` requires a new password
in the same request. Admins cannot change their own auth method.

## CLI usage for OIDC users

`devws login` (username/password) does not work for OIDC users. Mint an
API token in the web UI instead: **Profile → API tokens → Create
token**, then:

```bash
devws login <server-url> --token devws_…
```

## Provider notes

The mapping is *username claim → devws username*, and devws usernames
are DNS labels (`[a-z0-9-]`). Pick a claim whose values conform:

- **Keycloak**: `preferred_username` (the login username, e.g. `jdoe`)
  — the default works.
- **Dex**: static password users carry a `username` field that is
  emitted as `preferred_username`; LDAP/connector deployments usually
  map `uid`. Verify with a decoded test ID token.
- **Okta**: `preferred_username` is often the email — set
  `oidc.usernameClaim` to a login-attribute claim that yields DNS-label
  values, or create devws users whose names match after lowercasing.
- **Google / Entra ID**: the natural claims (`email`, `upn`) are
  email-like and **cannot** match a devws username. Use a custom claim
  (e.g. Entra ID optional claim, or a federated attribute) that contains
  the short username, or run an intermediate provider (Dex/Keycloak)
  that rewrites claims.

If the claim value is not a valid devws username, the login fails with
"no devws account matches your identity" — check the apiserver logs
(`oidc: username claim is not a valid devws username`) to see what the
claim name was configured as.

## Troubleshooting

- **Button missing on the login page**: OIDC is disabled — check that
  the `devws-oidc` Secret exists in the platform namespace
  (`kubectl -n devws-system get secret devws-oidc`) and that the
  apiserver pod started after it was created.
- **`oidc_error` on callback**: technical failure — state/nonce mismatch
  (stale login attempt, cookie blocked), token exchange failure (wrong
  client secret, redirect URI not registered), or ID token verification
  failure. The apiserver log has the reason.
- **`oidc_unauthorized`**: the claim did not map to an existing user
  with `authMethod: oidc`.