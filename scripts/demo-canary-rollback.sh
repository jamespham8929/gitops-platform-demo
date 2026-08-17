#!/usr/bin/env bash
# Reproduces an automated canary rollback end to end on a throwaway kind
# cluster. The point is to watch the rollout abort itself: nothing in this script
# tells Argo Rollouts to stop, the AnalysisTemplate's success-rate query does.
#
#   scripts/demo-canary-rollback.sh          # run it
#   scripts/demo-canary-rollback.sh --clean  # delete the cluster
#
# Needs kind, kubectl, helm, and docker. Takes about ten minutes, most of it
# pulling images.
set -euo pipefail

CLUSTER="${CLUSTER:-gitops-demo}"
NAMESPACE="${NAMESPACE:-demo}"
HOSTNAME_FOR_DEMO="sample-service.local"
IMAGE="sample-service:demo"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# High enough that the canary breaches a 98% success rate within one window,
# low enough that it is a plausible regression rather than a total outage.
BAD_FAILURE_RATE="${BAD_FAILURE_RATE:-0.4}"

step() { printf '\n=== %s ===\n' "$*"; }

if [[ "${1:-}" == "--clean" ]]; then
  kind delete cluster --name "$CLUSTER"
  exit 0
fi

for tool in kind kubectl helm docker; do
  command -v "$tool" > /dev/null || { echo "$tool is required" >&2; exit 1; }
done

step "Creating kind cluster $CLUSTER"
if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --config - <<'KIND'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
KIND
fi
kubectl config use-context "kind-$CLUSTER"

step "Installing ingress-nginx"
kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.10.1/deploy/static/provider/kind/deploy.yaml
kubectl -n ingress-nginx wait --for=condition=available deployment/ingress-nginx-controller --timeout=300s

step "Installing the Argo Rollouts controller"
kubectl create namespace argo-rollouts --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml
kubectl -n argo-rollouts wait --for=condition=available deployment/argo-rollouts --timeout=300s

step "Installing Prometheus"
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts > /dev/null
helm repo update > /dev/null
# Only the operator and the server are needed; the analysis reads one metric.
helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring --create-namespace \
  --set grafana.enabled=false \
  --set alertmanager.enabled=false \
  --set nodeExporter.enabled=false \
  --set kubeStateMetrics.enabled=false \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --wait --timeout 10m

step "Building and loading the service image"
docker build -t "$IMAGE" "$REPO_ROOT/apps/sample-service"
kind load docker-image "$IMAGE" --name "$CLUSTER"

step "Installing the good build"
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install sample-service "$REPO_ROOT/charts/sample-service" \
  --namespace "$NAMESPACE" \
  --set image.repository=sample-service \
  --set image.tag=demo \
  --set image.pullPolicy=Never \
  --set autoscaling.enabled=false \
  --set replicaCount=3 \
  --set ingress.hosts[0].host="$HOSTNAME_FOR_DEMO" \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix \
  --set ingress.tls=null \
  --set ingress.annotations=null \
  --set rollout.analysis.prometheusAddress=http://kube-prometheus-stack-prometheus.monitoring.svc:9090 \
  --set rollout.analysis.window=1m \
  --set rollout.analysis.interval=30s \
  --set rollout.analysis.initialDelay=30s \
  --set rollout.analysis.count=20 \
  --set-json 'rollout.steps=[{"setWeight":25},{"pause":{"duration":"5m"}}]' \
  --wait --timeout 5m

step "Starting a load generator"
kubectl -n "$NAMESPACE" delete deployment load-generator --ignore-not-found
kubectl -n "$NAMESPACE" create deployment load-generator \
  --image=curlimages/curl:8.8.0 -- \
  sh -c "while true; do curl -s -o /dev/null -X POST \
    -H 'Host: ${HOSTNAME_FOR_DEMO}' -H 'Content-Type: application/json' \
    -d '{\"customer_id\":\"load\",\"items\":[{\"sku\":\"widget\",\"quantity\":1,\"unit_cents\":500}]}' \
    http://ingress-nginx-controller.ingress-nginx.svc/api/v1/orders; sleep 0.2; done"
kubectl -n "$NAMESPACE" wait --for=condition=available deployment/load-generator --timeout=120s

step "Waiting for Prometheus to see the service"
sleep 90

step "Deploying a build that fails ${BAD_FAILURE_RATE} of order requests"
helm upgrade sample-service "$REPO_ROOT/charts/sample-service" \
  --namespace "$NAMESPACE" --reuse-values \
  --set failureRate="$BAD_FAILURE_RATE"

cat <<TEXT

The canary is now live on 25% of traffic and failing ${BAD_FAILURE_RATE} of its
order requests. Nothing below intervenes; the analysis run decides.

Watch it:

  kubectl argo rollouts get rollout sample-service -n ${NAMESPACE} --watch

Expect the AnalysisRun to go Failed within a couple of intervals, the Rollout to
go Degraded, and traffic to return to the stable ReplicaSet. Then:

  kubectl argo rollouts get rollout sample-service -n ${NAMESPACE}
  kubectl -n ${NAMESPACE} describe analysisrun -l rollout-type=Background

Tear the cluster down with: scripts/demo-canary-rollback.sh --clean
TEXT
