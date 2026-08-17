#!/usr/bin/env bash
# Demonstrates ArgoCD self-heal by making an out-of-band change and watching it
# get reverted. Run it against a cluster where argocd/applications/ is already
# applied and the staging Application is syncing.
#
#   scripts/demo-drift.sh [application] [namespace]
set -euo pipefail

APP="${1:-sample-service-staging}"
NAMESPACE="${2:-staging}"
WORKLOAD_KIND="${WORKLOAD_KIND:-rollout}"
WORKLOAD="${WORKLOAD:-sample-service}"
DRIFT_REPLICAS=9

command -v kubectl > /dev/null || { echo "kubectl is required" >&2; exit 1; }

replicas() {
  kubectl -n "$NAMESPACE" get "$WORKLOAD_KIND" "$WORKLOAD" -o jsonpath='{.spec.replicas}'
}

before="$(replicas)"
echo "Desired state in git: ${before} replicas"

echo
echo "Editing the cluster directly, the way someone would during an incident:"
echo "  kubectl -n ${NAMESPACE} scale ${WORKLOAD_KIND} ${WORKLOAD} --replicas=${DRIFT_REPLICAS}"
kubectl -n "$NAMESPACE" scale "$WORKLOAD_KIND" "$WORKLOAD" --replicas="$DRIFT_REPLICAS"
echo "Live state now: $(replicas) replicas"

if command -v argocd > /dev/null; then
  echo
  echo "ArgoCD sees the difference:"
  argocd app diff "$APP" || true
fi

echo
echo "Waiting for self-heal to revert it (syncPolicy.automated.selfHeal is on)."
for i in $(seq 1 60); do
  current="$(replicas)"
  if [[ "$current" == "$before" ]]; then
    echo "Reverted to ${current} replicas after ~$((i * 5))s. Git stayed the source of truth."
    exit 0
  fi
  sleep 5
done

echo "Still at $(replicas) replicas after 5 minutes." >&2
echo "Check that the Application has syncPolicy.automated.selfHeal enabled." >&2
exit 1
