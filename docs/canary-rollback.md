# Watching a canary roll itself back

The claim this repo makes is that a bad build stops partway through a deploy
without anyone intervening. This page is how you check that claim yourself, on a
kind cluster, in about ten minutes.

`scripts/demo-canary-rollback.sh` does the setup: kind cluster, ingress-nginx,
the Argo Rollouts controller, a trimmed kube-prometheus-stack, the chart, and a
load generator posting orders through the ingress. Then it deploys a build that
fails 40% of order requests and stops, leaving the rollout to decide what happens
next.

```bash
scripts/demo-canary-rollback.sh
kubectl argo rollouts get rollout sample-service -n demo --watch
```

## What the failure actually is

The bad build is the same image. `failureRate` is a value in the chart, wired to
the `FAILURE_RATE` environment variable, and the service fails that fraction of
`POST /api/v1/orders` with a 500. Everything else about the pod is unchanged,
which is the point:

- The liveness probe passes. `/health` never fails.
- The readiness probe passes. `/ready` never fails.
- The pods are Running and Ready, and stay that way.

A Deployment rolling update ships this build to every pod. There is nothing in
the rolling update contract that would stop it.

## What the rollout does instead

The canary takes 25% of traffic. The background AnalysisRun starts after the
first step and queries Prometheus every 30 seconds:

```promql
sum(rate(http_requests_total{service="sample-service-canary",namespace="demo",status!~"5.."}[1m]))
/
sum(rate(http_requests_total{service="sample-service-canary",namespace="demo"}[1m]))
```

With 40% of order requests failing, the canary's success rate settles around
0.7, well under the 0.98 threshold. The measurement is over the failure limit,
the AnalysisRun goes `Failed`, and the Rollout goes `Degraded`. Traffic returns
to the stable ReplicaSet, which never stopped serving.

Expect the rollout view to end up looking like this:

```
Name:            sample-service
Status:          ✖ Degraded
Message:         RolloutAborted: Rollout aborted update to revision 2
Strategy:        Canary
  Step:          0/2
  SetWeight:     0
  ActualWeight:  0

NAME                                       KIND        STATUS        AGE   INFO
⟳ sample-service                           Rollout     ✖ Degraded    14m
├──# revision:2
│  ├──⧉ sample-service-7d9f4b6c8           ReplicaSet  • ScaledDown  4m    canary
│  └──α sample-service-7d9f4b6c8-2         AnalysisRun ✖ Failed      3m    ✖ 2
└──# revision:1
   └──⧉ sample-service-5c8b7d9f4           ReplicaSet  ✔ Healthy     14m   stable
```

The AnalysisRun keeps every measurement it took, which is the part worth reading
after the fact:

```bash
kubectl -n demo describe analysisrun -l rollout-type=Background
```

Each measurement carries its value and its verdict, so "why did this roll back"
has an answer that is not a guess.

## Recovering

An aborted rollout stays aborted. The fix is a new revision, which in the real
flow means reverting the promotion PR: git goes back to the previous tag, ArgoCD
syncs it, and because the stable ReplicaSet is still the one serving traffic
there is no second outage while it comes back.

The failed ReplicaSet is kept for `abortScaleDownDelaySeconds` (15 minutes in
production) so its pods and logs can still be pulled after traffic has moved
away.

## Tuning it

`BAD_FAILURE_RATE` controls how bad the bad build is. Below the threshold gap it
will not trip: at `0.01` the canary sits at 99% and the rollout finishes, which
is a useful thing to see once. It shows the gate has a threshold rather than
being a coin flip.

```bash
BAD_FAILURE_RATE=0.01 scripts/demo-canary-rollback.sh   # finishes
BAD_FAILURE_RATE=0.4  scripts/demo-canary-rollback.sh   # aborts
scripts/demo-canary-rollback.sh --clean                 # delete the cluster
```
