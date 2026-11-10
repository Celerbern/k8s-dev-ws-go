#!/usr/bin/env bash
# End-to-end test of devws on a local kind cluster.
#
# Flow: create kind cluster → build images → helm install → bootstrap admin
# → create user via API → user creates workspace → registers SSH key →
# connects over SSH through the gateway.
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
WORKSPACE="e2e-ws"
SSH_KEY_DIR="$(mktemp -d)"
PF_PID=""
PF2_PID=""

cleanup() {
  local keep="${1:-}"
  kill "$PF_PID" 2>/dev/null || true
  kill "$PF2_PID" 2>/dev/null || true
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
  --set admin.password="$ADMIN_PASSWORD"

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

step "Admin usage endpoint"
curl -fsS -b "$SSH_KEY_DIR/cookies" "$API/api/admin/usage" | grep -q '"workspaces":1'
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
echo "Tear down with: kind delete cluster --name $KIND_NAME"
exit 0