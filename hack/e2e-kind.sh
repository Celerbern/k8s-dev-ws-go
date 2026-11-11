#!/usr/bin/env bash
# End-to-end test of devws on a local kind cluster.
#
# Flow: create kind cluster → build images → install Dex IdP → helm install
# (with OIDC) → bootstrap admin → create user via API → user creates
# workspace → registers SSH key → connects over SSH through the gateway →
# OIDC single sign-on through Dex.
#
# Usage: ./hack/e2e-kind.sh [keep]
#   keep  leave the cluster running after the test
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KIND_NAME="${KIND_NAME:-devws}"
RELEASE_NS="devws-system"
API="http://127.0.0.1:18080"
METRICS="http://127.0.0.1:18081"
SSH_PORT="${SSH_PORT:-2222}"
ADMIN_PASSWORD="devws-e2e-password"
USER_PASSWORD="devws-user-password"
OIDC_PASSWORD="devws-oidc-password"
DEX_IMAGE="ghcr.io/dexidp/dex:v2.46.1"
DEX_HOST="devws-dex.${RELEASE_NS}.svc.cluster.local"
WORKSPACE="e2e-ws"
SSH_KEY_DIR="$(mktemp -d)"
PF_PID=""
PF2_PID=""
PF3_PID=""

cleanup() {
  local keep="${1:-}"
  kill "$PF_PID" 2>/dev/null || true
  kill "$PF2_PID" 2>/dev/null || true
  kill "$PF3_PID" 2>/dev/null || true
  echo
  echo "=== e2e logs (devws apiserver) ==="
  kubectl -n "$RELEASE_NS" logs deploy/devws-apiserver --tail=80 || true
  if [[ "$keep" != "keep" ]]; then
    kind delete cluster --name "$KIND_NAME" || true
  fi
  rm -rf "$SSH_KEY_DIR"
}
trap 'cleanup "${1:-}"' EXIT

port_forward() {
  kubectl -n "$RELEASE_NS" port-forward svc/devws-web 18080:80 >/dev/null 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if curl -fsS "$API/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  echo "FAILED: apiserver did not come up on $API"
  return 1
}

step() { echo; echo "===> $*"; }

step "Creating kind cluster"
if ! kind get clusters 2>/dev/null | grep -q "^${KIND_NAME}$"; then
  kind create cluster --name "$KIND_NAME" --config hack/kind-config.yaml
else
  echo "cluster $KIND_NAME already exists, reusing"
fi
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"

step "Building images"
make docker-apiserver docker-workspace

step "Loading images into kind"
kind load docker-image devws-apiserver:dev --name "$KIND_NAME"
kind load docker-image devws-workspace:dev --name "$KIND_NAME"
# Dex (the OIDC identity provider for the e2e) is pulled from ghcr.io and
# loaded into kind so the cluster never depends on image pulls mid-test.
docker pull -q "$DEX_IMAGE"
kind load docker-image "$DEX_IMAGE" --name "$KIND_NAME"

# Reset any previous install so this script is safe to re-run: the image tag
# stays :dev, so a stale deployment would keep running old code. The release
# is uninstalled FIRST and the apiserver pod waited out, because the User
# controller re-adds the devws.io/cleanup finalizer while it runs — stripping
# finalizers before that would leave CRD deletion hanging. Only the apiserver
# removes those finalizers, so after it is gone they must be patched away.
step "Resetting previous install"
helm uninstall devws -n "$RELEASE_NS" >/dev/null 2>&1 || true
kubectl -n "$RELEASE_NS" wait --for=delete pod -l app.kubernetes.io/name=devws --timeout=90s >/dev/null 2>&1 || true
if kubectl get crd users.devws.io >/dev/null 2>&1; then
  kubectl get users.devws.io -o name 2>/dev/null | while read -r u; do
    kubectl patch "$u" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
  done
fi
kubectl delete crd users.devws.io workspaces.devws.io --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
kubectl delete ns "$RELEASE_NS" devws-e2e devws-admin --ignore-not-found --timeout=120s >/dev/null 2>&1 || true

# Dex must run before the chart install: the apiserver performs OIDC
# discovery against the issuer at startup and would crash-loop otherwise.
# The manifest carries its own Namespace object (the reset above deleted it).
step "Starting Dex (OIDC identity provider)"
kubectl apply -f hack/dex.yaml
kubectl -n "$RELEASE_NS" rollout status deploy/devws-dex --timeout=180s

step "Installing helm chart"
helm upgrade --install devws charts/devws \
  --namespace "$RELEASE_NS" --create-namespace \
  --set image.repository=devws-apiserver \
  --set image.tag=dev \
  --set image.pullPolicy=Never \
  --set workspaceImage=devws-workspace:dev \
  --set "config.sshHost=127.0.0.1:${SSH_PORT}" \
  --set service.ssh.type=NodePort \
  --set service.ssh.nodePort=30022 \
  --set metrics.enabled=true \
  --set admin.password="$ADMIN_PASSWORD" \
  --set oidc.enabled=true \
  --set "oidc.issuerUrl=http://${DEX_HOST}:5556/dex" \
  --set oidc.clientId=devws \
  --set oidc.clientSecret=devws-e2e-client-secret \
  --set "oidc.redirectUrl=${API}/api/auth/oidc/callback"

step "Waiting for the apiserver"
kubectl -n "$RELEASE_NS" rollout status deploy/devws-apiserver --timeout=180s
port_forward

step "Smoke: health endpoints"
curl -fsS "$API/healthz" >/dev/null
curl -fsS "$API/readyz" >/dev/null
echo "ok"

step "Smoke: login as bootstrap admin"
curl -fsS -c "$SSH_KEY_DIR/cookies" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASSWORD\"}" \
  "$API/api/auth/login" | grep -q '"username":"admin"'
echo "ok"

step "Creating user e2e via admin API"
curl -fsS -b "$SSH_KEY_DIR/cookies" -H 'Content-Type: application/json' \
  -d "{\"username\":\"e2e\",\"password\":\"$USER_PASSWORD\",\"email\":\"e2e@example.com\"}" \
  "$API/api/users" | grep -q '"username":"e2e"'
echo "ok"

step "Waiting for the user namespace"
for _ in $(seq 1 30); do
  if kubectl get ns devws-e2e >/dev/null 2>&1; then break; fi
  sleep 1
done
kubectl get ns devws-e2e
kubectl -n devws-e2e get resourcequota,networkpolicy
# PodSecurityAdmission labels: privileged enforcement for dev tooling,
# audit + warn at baseline (docs/security.md).
for label in enforce:privileged audit:baseline warn:baseline; do
  key="${label%%:*}"; want="${label##*:}"
  got=$(kubectl get ns devws-e2e -o jsonpath="{.metadata.labels.pod-security\\.kubernetes\\.io/${key}}")
  [[ "$got" == "$want" ]] || { echo "FAILED: psa $key = '$got', want '$want'"; exit 1; }
done
echo "psa labels ok"

step "Getting a CLI token for user e2e"
TOKEN=$(curl -fsS -H 'Content-Type: application/json' \
  -d "{\"username\":\"e2e\",\"password\":\"$USER_PASSWORD\",\"name\":\"e2e\"}" \
  "$API/api/auth/token" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[[ -n "$TOKEN" ]] || { echo "FAILED: no token"; exit 1; }
AUTH="Authorization: Bearer $TOKEN"

step "Creating workspace as user e2e"
curl -fsS -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$WORKSPACE\",\"class\":\"tiny\"}" \
  "$API/api/workspaces" | grep -q "\"name\":\"$WORKSPACE\""
echo "ok — waiting for it to run"
for _ in $(seq 1 90); do
  PHASE=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p')
  if [[ "$PHASE" == "Running" ]]; then echo "workspace is Running"; break; fi
  if [[ "$PHASE" == "Failed" ]]; then
    echo "FAILED: workspace entered Failed phase"
    kubectl -n devws-e2e describe pod "$WORKSPACE" || true
    exit 1
  fi
  sleep 2
done
[[ "$PHASE" == "Running" ]] || { echo "FAILED: workspace never became Running (phase: $PHASE)"; exit 1; }

step "Registering an SSH key for the gateway"
ssh-keygen -q -t ed25519 -N '' -f "$SSH_KEY_DIR/id_ed25519"
PUBKEY=$(cat "$SSH_KEY_DIR/id_ed25519.pub")
curl -fsS -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"e2e\",\"key\":\"$PUBKEY\"}" \
  "$API/api/users/e2e/keys" | grep -q '"fingerprint"'
echo "ok"

step "SSH through the gateway (jump host to the workspace pod)"
SSH_HOST=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE" | sed -n 's/.*"sshHost":"\([^"]*\)".*/\1/p')
echo "ssh host: $SSH_HOST"
cat > "$SSH_KEY_DIR/ssh_config" <<EOF
# The gateway entry authenticates the ProxyJump below (it does not inherit
# the workspace host's IdentityFile).
Host 127.0.0.1
  Port $SSH_PORT
  User e2e
  UserKnownHostsFile /dev/null
  StrictHostKeyChecking no
  IdentityFile $SSH_KEY_DIR/id_ed25519

Host devws-$WORKSPACE
  HostName $SSH_HOST
  Port 2222
  User devws
  ProxyJump e2e@127.0.0.1:$SSH_PORT
  UserKnownHostsFile /dev/null
  StrictHostKeyChecking no
  IdentityFile $SSH_KEY_DIR/id_ed25519
EOF
SSH_RESULT=""
for i in $(seq 1 36); do
  if SSH_RESULT=$(ssh -F "$SSH_KEY_DIR/ssh_config" -o ConnectTimeout=15 devws-$WORKSPACE 'echo hello-from-workspace && git --version && ls /home/devws' 2>"$SSH_KEY_DIR/ssh_err"); then
    break
  fi
  # Newly registered keys take up to ~2min to reach a running workspace
  # (kubelet secret sync + the workspace's authorized_keys sync loop).
  echo "  ssh attempt $i failed: $(tail -1 "$SSH_KEY_DIR/ssh_err" 2>/dev/null)"
  sleep 5
done
echo "$SSH_RESULT"
echo "$SSH_RESULT" | grep -q "hello-from-workspace"

step "Stopping and starting the workspace (persistence check)"
echo "persist-$(date +%s)" > "$SSH_KEY_DIR/marker"
MARKER=$(cat "$SSH_KEY_DIR/marker")
ssh -F "$SSH_KEY_DIR/ssh_config" devws-$WORKSPACE "echo $MARKER > /home/devws/marker.txt"
curl -fsS -X POST -H "$AUTH" "$API/api/workspaces/$WORKSPACE/stop" >/dev/null
for _ in $(seq 1 60); do
  PHASE=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p')
  [[ "$PHASE" == "Stopped" ]] && break
  sleep 2
done
[[ "$PHASE" == "Stopped" ]] || { echo "FAILED: workspace did not stop"; exit 1; }
curl -fsS -X POST -H "$AUTH" "$API/api/workspaces/$WORKSPACE/start" >/dev/null
for _ in $(seq 1 90); do
  PHASE=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p')
  [[ "$PHASE" == "Running" ]] && break
  sleep 2
done
[[ "$PHASE" == "Running" ]] || { echo "FAILED: workspace did not restart"; exit 1; }
FOUND=$(ssh -F "$SSH_KEY_DIR/ssh_config" devws-$WORKSPACE "cat /home/devws/marker.txt")
[[ "$FOUND" == "$MARKER" ]] || { echo "FAILED: marker file lost: '$FOUND' != '$MARKER'"; exit 1; }
echo "data persisted across stop/start ✓"

step "Admin usage endpoint (usage vs quotas)"
USAGE=$(curl -fsS -b "$SSH_KEY_DIR/cookies" "$API/api/admin/usage")
echo "$USAGE" | grep -q '"workspaces":1' || { echo "FAILED: usage totals: $USAGE"; exit 1; }
echo "$USAGE" | grep -q '"username":"e2e"' || { echo "FAILED: no per-user row for e2e: $USAGE"; exit 1; }
# Defaults: maxWorkspaces 5, cpu 8, memory 16Gi, storage 100Gi.
echo "$USAGE" | grep -q '"maxWorkspaces":5,"running":1,"cpu":"250m"' || { echo "FAILED: usage row wrong: $USAGE"; exit 1; }
echo "$USAGE" | grep -q '"cpuQuota":"8"' || { echo "FAILED: cpu quota missing: $USAGE"; exit 1; }
echo "$USAGE" | grep -q '"memoryQuota":"16Gi"' || { echo "FAILED: memory quota missing: $USAGE"; exit 1; }
echo "$USAGE" | grep -q '"storageQuota":"100Gi"' || { echo "FAILED: storage quota missing: $USAGE"; exit 1; }
# Tiny workspace: 250m cpu, 512Mi memory, 5Gi storage.
echo "$USAGE" | grep -q '"cpu":"250m"' || { echo "FAILED: cpu usage wrong: $USAGE"; exit 1; }
echo "$USAGE" | grep -q '"memory":"512Mi"' || { echo "FAILED: memory usage wrong: $USAGE"; exit 1; }
echo "$USAGE" | grep -q '"storage":"5Gi"' || { echo "FAILED: storage usage wrong: $USAGE"; exit 1; }
# Own user view includes usage for the quota card.
curl -fsS -H "$AUTH" "$API/api/users/e2e" | grep -q '"usage":{"workspaces":1,"running":1,"cpu":"250m","memory":"512Mi","storage":"5Gi"}' || {
  echo "FAILED: own usage missing from user view"; exit 1
}
echo "ok"

step "Admin audit log (events across user namespaces)"
AUDIT=$(curl -fsS -b "$SSH_KEY_DIR/cookies" "$API/api/admin/events")
echo "$AUDIT" | grep -q '"user":"e2e"' || { echo "FAILED: audit events not attributed to user e2e: $AUDIT"; exit 1; }
echo "$AUDIT" | grep -q '"WorkspaceStarting"' || { echo "FAILED: no WorkspaceStarting in audit log: $AUDIT"; exit 1; }
if curl -fsS -H "$AUTH" "$API/api/admin/events" >/dev/null 2>&1; then
  echo "FAILED: non-admin must not read the audit log"; exit 1
fi
echo "ok"

step "Workspace logs API"
LOGS=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE/logs?tail=100")
[[ -n "$LOGS" ]] || { echo "FAILED: logs endpoint returned nothing"; exit 1; }
echo "$LOGS" | head -3
if curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE/logs?container=nope" >/dev/null 2>&1; then
  echo "FAILED: unknown container must 404"; exit 1
fi
echo "ok"

step "Workspace events API"
EVENTS=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE/events")
echo "$EVENTS" | grep -q '"WorkspaceStarting"' || { echo "FAILED: no WorkspaceStarting event: $EVENTS"; exit 1; }
echo "ok"

step "Prometheus metrics endpoint"
kubectl -n "$RELEASE_NS" port-forward svc/devws-web 18081:8081 >/dev/null 2>&1 &
PF2_PID=$!
for _ in $(seq 1 30); do
  if curl -fsS "$METRICS/metrics" >/dev/null 2>&1; then break; fi
  sleep 1
done
METRICS_BODY=$(curl -fsS "$METRICS/metrics") || { echo "FAILED: metrics endpoint did not come up"; exit 1; }
echo "$METRICS_BODY" | grep -q '^devws_workspaces_by_phase{phase="Running"} 1$' || {
  echo "FAILED: devws_workspaces_by_phase does not show 1 Running workspace"; exit 1
}
echo "$METRICS_BODY" | grep -q '^devws_users{state="active"} 2$' || {
  echo "FAILED: devws_users does not show 2 active users"; exit 1
}
echo "$METRICS_BODY" | grep -q 'devws_api_requests_total{code="200",method="GET",route="GET /api/workspaces/{name}"}' || {
  echo "FAILED: devws_api_requests_total does not count workspace GETs"; exit 1
}
echo "$METRICS_BODY" | grep -q '^devws_gateway_connections_total ' || {
  echo "FAILED: gateway connection counter missing"; exit 1
}
kill "$PF2_PID" 2>/dev/null || true
PF2_PID=""
echo "ok"

step "OIDC single sign-on (Dex IdP)"
# The login page's provider probe must advertise SSO.
curl -fsS "$API/api/auth/providers" | grep -q '"oidc":{"enabled":true}' || {
  echo "FAILED: /api/auth/providers must report oidc as enabled"; exit 1
}

# Admin pre-creates the OIDC users — there is no auto-provisioning.
curl -fsS -b "$SSH_KEY_DIR/cookies" -H 'Content-Type: application/json' \
  -d '{"username":"e2e-oidc","authMethod":"oidc","email":"e2e-oidc@example.com"}' \
  "$API/api/users" | grep -q '"authMethod":"oidc"' || {
  echo "FAILED: could not create oidc user e2e-oidc"; exit 1
}
curl -fsS -b "$SSH_KEY_DIR/cookies" -H 'Content-Type: application/json' \
  -d '{"username":"oidc-admin","authMethod":"oidc","role":"admin","email":"oidc-admin@example.com"}' \
  "$API/api/users" | grep -q '"authMethod":"oidc"' || {
  echo "FAILED: could not create oidc admin oidc-admin"; exit 1
}

# The host reaches Dex through a port-forward under its in-cluster DNS
# name (the issuer the apiserver discovered), so the scripted browser
# below follows the exact redirect chain a real browser would.
kubectl -n "$RELEASE_NS" port-forward svc/devws-dex 5556:5556 >/dev/null 2>&1 &
PF3_PID=$!
DEX_UP=0
for _ in $(seq 1 30); do
  if curl -fsS --resolve "$DEX_HOST:5556:127.0.0.1" "http://$DEX_HOST:5556/dex/healthz" >/dev/null 2>&1; then
    DEX_UP=1; break
  fi
  sleep 1
done
[[ "$DEX_UP" == 1 ]] || { echo "FAILED: dex did not come up on the port-forward"; exit 1; }

# oidc_login plays the browser: start the flow at the apiserver, land on
# the Dex password form, submit credentials (Dex static passwords log in
# with the EMAIL as "login"), and follow the whole redirect chain back.
# Prints the final URL after the last redirect. The jar starts empty so
# a rejected login cannot inherit the previous user's session cookie.
oidc_login() {
  local jar="$1" email="$2"
  local auth_url login_url
  rm -f "$jar"
  auth_url=$(curl -fsS -c "$jar" -o /dev/null -w '%{redirect_url}' "$API/api/auth/oidc/start")
  [[ "$auth_url" == "http://${DEX_HOST}:5556/dex/auth?"* ]] || {
    echo "FAILED: unexpected authorize redirect: $auth_url" >&2; return 1
  }
  login_url=$(curl -fsS -b "$jar" -c "$jar" --resolve "$DEX_HOST:5556:127.0.0.1" \
    -o /dev/null -L -w '%{url_effective}' "$auth_url")
  curl -fsS -b "$jar" -c "$jar" --resolve "$DEX_HOST:5556:127.0.0.1" \
    -o /dev/null -L --max-redirs 10 \
    --data-urlencode "login=$email" \
    --data-urlencode "password=$OIDC_PASSWORD" \
    -w '%{url_effective}' "$login_url"
}

OIDC_JAR="$SSH_KEY_DIR/oidc.jar"

# A pre-created oidc user signs in and lands in the app with a session.
END_URL=$(oidc_login "$OIDC_JAR" "e2e-oidc@example.com")
[[ "$END_URL" == "$API/" ]] || { echo "FAILED: e2e-oidc login ended at $END_URL"; exit 1; }
ME=$(curl -fsS -b "$OIDC_JAR" "$API/api/auth/me")
echo "$ME" | grep -q '"username":"e2e-oidc"' || { echo "FAILED: me is not e2e-oidc: $ME"; exit 1; }
echo "$ME" | grep -q '"authMethod":"oidc"' || { echo "FAILED: me lacks authMethod oidc: $ME"; exit 1; }

# An oidc admin signs in and can use admin endpoints.
END_URL=$(oidc_login "$OIDC_JAR" "oidc-admin@example.com")
[[ "$END_URL" == "$API/" ]] || { echo "FAILED: oidc-admin login ended at $END_URL"; exit 1; }
curl -fsS -b "$OIDC_JAR" "$API/api/users" | grep -q '"username":"oidc-admin"' || {
  echo "FAILED: oidc admin cannot list users"; exit 1
}

# An identity without a pre-created user is rejected: no session, error
# back on the login page.
END_URL=$(oidc_login "$OIDC_JAR" "nouser@example.com")
[[ "$END_URL" == "$API/login?error=oidc_unauthorized" ]] || {
  echo "FAILED: nouser login ended at $END_URL"; exit 1
}
if curl -fsS -b "$OIDC_JAR" "$API/api/auth/me" >/dev/null 2>&1; then
  echo "FAILED: unknown oidc identity must not get a session"; exit 1
fi

# OIDC users cannot sign in with a password (auth methods are exclusive).
if curl -fsS -H 'Content-Type: application/json' \
  -d "{\"username\":\"e2e-oidc\",\"password\":\"$OIDC_PASSWORD\"}" \
  "$API/api/auth/login" >/dev/null 2>&1; then
  echo "FAILED: oidc user must not be able to sign in with a password"; exit 1
fi

kill "$PF3_PID" 2>/dev/null || true
PF3_PID=""
echo "ok"

step "Leader election: two replicas, failover, API keeps serving"
kubectl -n "$RELEASE_NS" scale deploy/devws-apiserver --replicas=2
kubectl -n "$RELEASE_NS" rollout status deploy/devws-apiserver --timeout=180s
HOLDER=$(kubectl -n "$RELEASE_NS" get lease devws-apiserver-leader -o jsonpath='{.spec.holderIdentity}')
OLD_LEADER="${HOLDER%%_*}"
echo "lease holder: $HOLDER"
# The holder must be one of the running apiserver pods.
PODS=$(kubectl -n "$RELEASE_NS" get pods -l app.kubernetes.io/name=devws -o jsonpath='{range .items[*]}{.metadata.name} {end}')
HOLDER_IS_POD=0
for p in $PODS; do
  [[ "$HOLDER" == "${p}_"* ]] && HOLDER_IS_POD=1
done
[[ "$HOLDER_IS_POD" == 1 ]] || { echo "FAILED: lease holder is not a running pod (holder: $HOLDER, pods: $PODS)"; exit 1; }
# Kill the leader pod: another replica must take over the lease.
kubectl -n "$RELEASE_NS" delete pod "$OLD_LEADER" >/dev/null
for _ in $(seq 1 45); do
  NEW_HOLDER=$(kubectl -n "$RELEASE_NS" get lease devws-apiserver-leader -o jsonpath='{.spec.holderIdentity}')
  if [[ "$NEW_HOLDER" != "$HOLDER" ]]; then
    LIVE_PODS=$(kubectl -n "$RELEASE_NS" get pods -l app.kubernetes.io/name=devws -o jsonpath='{range .items[*]}{.metadata.name} {end}')
    for p in $LIVE_PODS; do
      [[ "$NEW_HOLDER" == "${p}_"* ]] && break 2
    done
  fi
  sleep 2
done
NEW_HOLDER=$(kubectl -n "$RELEASE_NS" get lease devws-apiserver-leader -o jsonpath='{.spec.holderIdentity}')
LIVE_PODS=$(kubectl -n "$RELEASE_NS" get pods -l app.kubernetes.io/name=devws -o jsonpath='{range .items[*]}{.metadata.name} {end}')
TAKEOVER=0
for p in $LIVE_PODS; do
  [[ "$NEW_HOLDER" == "${p}_"* ]] && TAKEOVER=1
done
[[ "$TAKEOVER" == 1 ]] || { echo "FAILED: no live pod took over the lease (holder: $NEW_HOLDER, pods: $LIVE_PODS)"; exit 1; }
echo "lease failed over to $NEW_HOLDER"
# The web API kept serving; re-establish the port-forward in case it was
# bound to the deleted pod.
kill "$PF_PID" 2>/dev/null || true
port_forward
PHASE=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$WORKSPACE" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p')
[[ "$PHASE" == "Running" ]] || { echo "FAILED: workspace not queryable after failover (phase: $PHASE)"; exit 1; }
# Back to a single replica for the rest of the run.
kubectl -n "$RELEASE_NS" scale deploy/devws-apiserver --replicas=1
kubectl -n "$RELEASE_NS" rollout status deploy/devws-apiserver --timeout=180s
echo "ok"

step "gitRepos pre-clone (public repo)"
GIT_WS="e2e-git"
GIT_REPO="https://github.com/octocat/Hello-World.git"
curl -fsS -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$GIT_WS\",\"class\":\"tiny\",\"gitRepos\":[\"$GIT_REPO\"]}" \
  "$API/api/workspaces" | grep -q "\"name\":\"$GIT_WS\""
for _ in $(seq 1 90); do
  PHASE=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$GIT_WS" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p')
  [[ "$PHASE" == "Running" ]] && break
  [[ "$PHASE" == "Failed" ]] && { echo "FAILED: git workspace Failed"; exit 1; }
  sleep 2
done
[[ "$PHASE" == "Running" ]] || { echo "FAILED: git workspace never became Running (phase: $PHASE)"; exit 1; }
# The clone ran in the git-clone init container: its logs mention the clone.
curl -fsS -H "$AUTH" "$API/api/workspaces/$GIT_WS/logs?container=git-clone&tail=all" | grep -q "devws: cloning $GIT_REPO" || {
  echo "FAILED: git-clone logs do not mention the clone"; exit 1
}
# And the repo landed on the persistent volume.
GIT_SSH_HOST=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$GIT_WS" | sed -n 's/.*"sshHost":"\([^"]*\)".*/\1/p')
cat >> "$SSH_KEY_DIR/ssh_config" <<EOF

Host devws-$GIT_WS
  HostName $GIT_SSH_HOST
  Port 2222
  User devws
  ProxyJump e2e@127.0.0.1:$SSH_PORT
  UserKnownHostsFile /dev/null
  StrictHostKeyChecking no
  IdentityFile $SSH_KEY_DIR/id_ed25519
EOF
for i in $(seq 1 36); do
  if ssh -F "$SSH_KEY_DIR/ssh_config" -o ConnectTimeout=15 devws-$GIT_WS 'test -d /home/devws/Hello-World/.git' 2>/dev/null; then
    echo "repo cloned onto the workspace volume ✓"; break
  fi
  sleep 5
done
ssh -F "$SSH_KEY_DIR/ssh_config" devws-$GIT_WS 'test -d /home/devws/Hello-World/.git' || {
  echo "FAILED: Hello-World was not cloned into the workspace"; exit 1
}

step "git clone failure surfaces in status and events"
BAD_WS="e2e-bad"
curl -fsS -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"$BAD_WS\",\"class\":\"tiny\",\"gitRepos\":[\"https://github.com/octocat/devws-no-such-repo-e2e.git\"]}" \
  "$API/api/workspaces" >/dev/null
for _ in $(seq 1 60); do
  PHASE=$(curl -fsS -H "$AUTH" "$API/api/workspaces/$BAD_WS" | sed -n 's/.*"phase":"\([^"]*\)".*/\1/p')
  [[ "$PHASE" == "Failed" ]] && break
  sleep 2
done
[[ "$PHASE" == "Failed" ]] || { echo "FAILED: bad repo workspace never Failed (phase: $PHASE)"; exit 1; }
curl -fsS -H "$AUTH" "$API/api/workspaces/$BAD_WS" | grep -q "git clone failed" || {
  echo "FAILED: status message does not mention the clone failure"; exit 1
}
curl -fsS -H "$AUTH" "$API/api/workspaces/$BAD_WS/events" | grep -q '"WorkspaceFailed"' || {
  echo "FAILED: no WorkspaceFailed event for bad repo"; exit 1
}
echo "clone failure surfaced ✓ — cleaning up"
curl -fsS -X DELETE -H "$AUTH" "$API/api/workspaces/$BAD_WS" >/dev/null

echo
echo "====================================================================="
echo "E2E PASSED"
echo "====================================================================="
echo "The cluster is still running. Try it yourself:"
echo "  export PATH=\"$PWD/bin:\$PATH\""
echo "  bin/devws login $API --ssh-host 127.0.0.1 --ssh-port $SSH_PORT   # user: e2e / $USER_PASSWORD"
echo "  bin/devws workspace create demo"
echo "  bin/devws ssh-config --write && ssh devws-demo"
echo "  bin/devws workspace logs demo -f     # live log tail"
echo "  bin/devws code demo                  # VS Code Remote-SSH"
echo "Web UI: $API (admin / $ADMIN_PASSWORD) — requires the port-forward:"
echo "  kubectl -n $RELEASE_NS port-forward svc/devws-web 18080:80"
echo "SSO: sign in as e2e-oidc / $OIDC_PASSWORD at Dex (port-forward 5556):"
echo "  kubectl -n $RELEASE_NS port-forward svc/devws-dex 5556:5556"
echo "Tear down with: kind delete cluster --name $KIND_NAME"
exit 0