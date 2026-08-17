# gitops-platform-demo

A GitOps delivery pipeline where the deploy decides for itself whether to
finish. GitHub Actions builds and verifies, ArgoCD reconciles git to the
cluster, and Argo Rollouts shifts traffic in steps while a Prometheus query
watches the new version. A build that starts cleanly and then misbehaves stops
at 5% of production traffic and rolls back on its own.

## The delivery path

```
push to main
  │
  ├─ CI: test, lint, smoke the binary, build, scan, sign
  ├─ CI writes the tag into values-staging.yaml
  │
  ▼
ArgoCD auto-syncs staging ──► canary at 25% ──► analysis ──► 100%
  │                                              │
  │                                              └─ fails ──► abort, stable keeps serving
  ▼
promotion PR bumps values-production.yaml   ← the human gate
  │
  ▼
operator approves the production sync
  │
  ▼
canary 5% ─► 20% ─► 50% ─► pause
  │            │        │
  │            │        └─ post-promotion smoke gate ──► promote or abort
  └────────────┴─ background analysis, aborts at any step
```

Three gates, and only one of them is a person. The PR decides whether a change
should ship now. Everything after that is decided by measurements. The reasoning
is in [ADR 0003](docs/adr/0003-promotion-strategy.md).

## What makes the canary self-cancelling

The Rollout runs a background AnalysisRun from the first traffic step. It
queries Prometheus on an interval and aborts on a breach:

```promql
sum(rate(http_requests_total{service="sample-service-canary",namespace="production",status!~"5.."}[2m]))
/
sum(rate(http_requests_total{service="sample-service-canary",namespace="production"}[2m]))
```

Argo Rollouts pins the canary service's selector to the canary pods, and
prometheus-operator stamps a `service` label on samples it scrapes through a
service, so that one label isolates canary traffic without any pod-hash
relabeling. Production thresholds are 99% success and 400ms at p95. Staging runs
the same machinery on a shorter clock with looser thresholds, so the query is
exercised before production depends on it.

CI checks that the metric names the analysis queries are the ones the service
exports. A gate reading a metric that no longer exists returns no data, and no
data is treated as no evidence rather than as failure, so a silent rename would
otherwise turn the gate off.

[docs/canary-rollback.md](docs/canary-rollback.md) walks through reproducing an
automated rollback on kind, and
[ADR 0002](docs/adr/0002-progressive-delivery-with-argo-rollouts.md) covers why
canary rather than blue-green or alerting.

## The service

`apps/sample-service` is a Go HTTP service with an order-pricing endpoint,
because a canary needs something to measure. `POST /api/v1/orders` validates a
payload, applies coupon rules, computes shipping and tax, and returns 400 for
malformed input, 422 for rules the caller could satisfy differently, and 201
with the priced order.

```bash
curl -X POST localhost:8080/api/v1/orders -H 'Content-Type: application/json' \
  -d '{"customer_id":"c-1","items":[{"sku":"widget","quantity":2,"unit_cents":500}]}'

{"order_id":"ord_a1b2c3d4e5f6","customer_id":"c-1","subtotal_cents":1000,
 "discount_cents":0,"shipping_cents":599,"tax_cents":139,"total_cents":1738}
```

It exports `http_requests_total`, `http_request_duration_seconds`, and the
order counters at `/metrics`. Request metrics are labelled with the route the
mux matched rather than the raw path, so unmatched traffic shares one series
instead of minting one per URL. Scrapes are excluded from the request counters,
because counting them would inflate the success rate the canary analysis reads.

`FAILURE_RATE` makes the service fail that fraction of order requests. It is how
the rollback demo produces a build that passes every probe and still needs to be
stopped.

## Repository structure

```
apps/sample-service/      Go service: order pricing, metrics, graceful drain
charts/sample-service/    Helm chart: Rollout (or Deployment), AnalysisTemplate,
                          stable and canary Services, Ingress, HPA, PDB,
                          ServiceMonitor
argocd/applications/      ArgoCD Application per environment
scripts/
  smoke.sh                Post-deploy probe suite, used by CI and the gate
  demo-canary-rollback.sh Reproduces an automated rollback on kind
  demo-drift.sh           Makes drift and watches self-heal revert it
docs/adr/                 Why the delivery works the way it does
.github/workflows/
  ci.yml                  Test, lint, smoke, chart validation, build, scan, sign
  promote.yml             Opens the production promotion PR
  post-promotion-smoke.yml  Probes the production canary, promotes or aborts
```

## Promoting to production

Each environment pins its own image tag and the two move independently.

1. Push to main. CI builds, tags with the short SHA, and writes that tag into
   `charts/sample-service/values-staging.yaml`. ArgoCD auto-syncs staging and
   the staging canary runs.
2. Run the **Promote to production** workflow with the staging tag. It opens a
   PR that changes one line:

   ```diff
    image:
      # Updated by .github/workflows/promote.yml, which opens a PR for review.
   -  tag: "9f8e7d6"
   +  tag: "a1b2c3d"
   ```

3. Merge it. The production Application goes `OutOfSync`, and an operator
   approves the sync in ArgoCD.
4. The canary walks to 50% and pauses. The post-promotion smoke gate probes it
   through the canary ingress header route, then promotes on success or aborts
   on failure.

Rolling back is reverting the merge. The previous ReplicaSet is still scaled up
for `scaleDownDelaySeconds`, so it serves immediately.

## Running it

The chart installs either a Rollout or a plain Deployment. Set
`rollout.enabled=false` on a cluster without the Argo Rollouts controller and
the same pod spec ships behind a rolling update.

```bash
kubectl apply -f argocd/applications/          # both environments

scripts/demo-canary-rollback.sh                # kind cluster, bad build, watch it abort
scripts/demo-drift.sh sample-service-staging staging
```

## Local development

```bash
cd apps/sample-service/src
go run .                                       # :8080

scripts/smoke.sh http://localhost:8080         # the suite the promotion gate runs
FAILURE_RATE=0.5 go run .                      # then watch the suite fail
```

## Running CI locally

```bash
cd apps/sample-service/src && go test ./... -race -cover && golangci-lint run ./...

helm lint charts/sample-service
helm template sample-service charts/sample-service \
  -f charts/sample-service/values.yaml \
  -f charts/sample-service/values-production.yaml \
  | kubeconform -strict -kubernetes-version 1.29.0 -schema-location default \
      -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

shellcheck scripts/*.sh
```

## Prerequisites

- A Kubernetes cluster with ArgoCD, the Argo Rollouts controller, ingress-nginx,
  and a Prometheus that scrapes ServiceMonitors
- `IMAGE_REGISTRY` pointing at your container registry
- For the live promotion gate: `KUBECONFIG_B64` and `ARGOCD_AUTH_TOKEN` secrets
  and an `ARGOCD_SERVER` variable. Without them the gate reports what it would
  have done and passes, so the workflow is readable on a repo with no cluster
  attached.

## License

MIT
