#!/usr/bin/env bash
# Complete teardown of a devws release.
#
# `helm uninstall` alone is not a full teardown: the chart ships its
# CRDs in charts/devws/crds/, and Helm never deletes CRDs installed
# from that directory (deleting a CRD deletes every instance of it, so
# Helm leaves them alone by design). On top of the release, this script
# removes:
#
#   - all User, Workspace and WorkspaceImage objects (via CRD deletion;
#     helm-seeded catalog images go with the release, API-created ones
#     do not)
#   - per-user namespaces (devws-<user>) with everything inside them:
#     workspace pods, home PVCs, quotas, network policies, ssh secrets
#   - the release namespace itself (Helm never deletes namespaces)
#
# Ordering matters — same reasoning as the reset block in
# hack/e2e-kind.sh: the User controller adds the devws.io/cleanup
# finalizer to every User and only the apiserver removes it (after
# deleting the user's namespace). Users are therefore deleted while
# the apiserver still runs, letting the controller clean up namespaces
# itself; when that cannot finish — apiserver already gone or
# unhealthy — the script uninstalls the release first and patches the
# finalizers by hand afterwards.
#
# Usage: ./hack/teardown.sh   (or: make teardown)
# Environment overrides:
#   RELEASE_NAME       helm release name       (default: devws)
#   RELEASE_NS         helm release namespace  (default: devws-system)
#   DELETE_RELEASE_NS  also delete the release namespace (default: true;
#                      set to false when devws shares it with other apps)
#   GRACEFUL_TIMEOUT   seconds to wait for controller-led user cleanup
#                      (default: 180)
set -euo pipefail

RELEASE_NAME="${RELEASE_NAME:-devws}"
RELEASE_NS="${RELEASE_NS:-devws-system}"
DELETE_RELEASE_NS="${DELETE_RELEASE_NS:-true}"
GRACEFUL_TIMEOUT="${GRACEFUL_TIMEOUT:-180}"
CRDS="users.devws.io workspaces.devws.io workspaceimages.devws.io"

step() { echo; echo "===> $*"; }

# ---------------------------------------------------------------------
# 1. Users first, while the apiserver may still be running: deleting a
#    User makes the controller delete the devws-<user> namespace and
#    only then drop the finalizer. This path needs no patching; if it
#    cannot run or finish, step 3 patches the finalizers instead.
# ---------------------------------------------------------------------
step "Deleting User objects (controller removes their namespaces)"
if kubectl get users.devws.io >/dev/null 2>&1; then
  if helm status "$RELEASE_NAME" -n "$RELEASE_NS" >/dev/null 2>&1 \
    && kubectl -n "$RELEASE_NS" get pods -l app.kubernetes.io/name=devws -o name 2>/dev/null | grep -q .; then
    kubectl delete users.devws.io --all --timeout="${GRACEFUL_TIMEOUT}s" \
      || echo "WARN: controller-led cleanup did not finish in ${GRACEFUL_TIMEOUT}s — falling back"
  else
    echo "release or apiserver not running — finalizers will be patched instead"
  fi
fi

# ---------------------------------------------------------------------
# 2. The release itself.
# ---------------------------------------------------------------------
step "Uninstalling the helm release"
if helm status "$RELEASE_NAME" -n "$RELEASE_NS" >/dev/null 2>&1; then
  helm uninstall "$RELEASE_NAME" -n "$RELEASE_NS"
else
  echo "release '$RELEASE_NAME' not found in namespace '$RELEASE_NS' — skipping"
fi

# The pods must be gone before finalizers are patched below: a live
# User controller would re-add devws.io/cleanup immediately.
step "Waiting for the apiserver to stop"
kubectl -n "$RELEASE_NS" wait --for=delete pod -l app.kubernetes.io/name=devws \
  --timeout=90s >/dev/null 2>&1 || true

# ---------------------------------------------------------------------
# 3. Brute-force fallback for Users the controller could not finish:
#    strip the finalizer so CRD deletion is not blocked.
# ---------------------------------------------------------------------
step "Patching finalizers off remaining User objects"
if kubectl get crd users.devws.io >/dev/null 2>&1; then
  kubectl get users.devws.io -o name | while read -r u; do
    kubectl patch "$u" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
  done
fi

# ---------------------------------------------------------------------
# 4. The CRDs themselves — this deletes every remaining instance
#    (Users, Workspaces, API-created WorkspaceImages).
# ---------------------------------------------------------------------
step "Deleting CRDs ($CRDS)"
kubectl delete crd $CRDS --ignore-not-found --timeout=120s \
  || echo "WARN: CRD deletion timed out — continuing"
for crd in $CRDS; do
  gone=0
  for _ in $(seq 1 15); do
    kubectl get crd "$crd" >/dev/null 2>&1 || { gone=1; break; }
    sleep 2
  done
  [[ "$gone" == 1 ]] || echo "WARN: crd/$crd is stuck terminating (inspect: kubectl get crd $crd -o yaml)"
done

# ---------------------------------------------------------------------
# 5. Namespaces. The User controller labels every namespace it manages
#    with devws.io/managed=true, so select on the label instead of
#    guessing usernames; this also catches leftovers from interrupted
#    teardowns.
# ---------------------------------------------------------------------
step "Deleting per-user namespaces"
kubectl delete ns -l devws.io/managed=true --ignore-not-found --timeout=180s \
  || echo "WARN: some namespaces did not finish terminating"

if [[ "$DELETE_RELEASE_NS" == "true" ]]; then
  step "Deleting the release namespace"
  kubectl delete ns "$RELEASE_NS" --ignore-not-found --timeout=180s \
    || echo "WARN: namespace $RELEASE_NS did not finish terminating"
fi

# ---------------------------------------------------------------------
# 6. Data-safety note: workspace PVCs follow their StorageClass reclaim
#    policy. kind's local-path deletes the PV with its claim, but a
#    Retain class leaves the PV — and the data on it — behind on
#    purpose. Surface those instead of deleting them silently.
# ---------------------------------------------------------------------
step "Checking for released volumes"
RELEASED=$(kubectl get pv -o jsonpath='{range .items[?(@.status.phase=="Released")]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
if [[ -n "$RELEASED" ]]; then
  echo "Released PVs remain (Retain reclaim policy?):"
  echo "$RELEASED"
  echo "Delete them explicitly with: kubectl delete pv <name>"
else
  echo "none"
fi

echo
echo "Teardown complete."