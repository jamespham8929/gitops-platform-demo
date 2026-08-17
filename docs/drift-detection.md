# Drift detection and self-heal

The staging Application has `selfHeal: true`. What that buys is easy to say and
easy to doubt, so here is how to see it.

`scripts/demo-drift.sh` makes an out-of-band change to the cluster, then polls
until ArgoCD undoes it.

```bash
kubectl apply -f argocd/applications/
scripts/demo-drift.sh sample-service-staging staging
```

## What it does

The script scales the Rollout directly, which is the realistic version of this
problem. Nobody edits YAML in a cluster for fun. They do it at 2am because a
service is struggling and scaling it up is the fastest thing they can think of.

```bash
kubectl -n staging scale rollout sample-service --replicas=9
```

ArgoCD notices within its refresh interval, reports the Application `OutOfSync`,
and because self-heal is on, applies the desired state from git. The replica
count goes back to what `values-staging.yaml` says. Expect it to take under a
minute.

```
Desired state in git: 2 replicas
Live state now: 9 replicas

ArgoCD sees the difference:
===== argoproj.io/Rollout staging/sample-service ======
2c2
<   replicas: 9
---
>   replicas: 2

Waiting for self-heal to revert it (syncPolicy.automated.selfHeal is on).
Reverted to 2 replicas after ~20s. Git stayed the source of truth.
```

## Why production is configured differently

The production Application has no `automated` block at all, so it neither
auto-syncs nor self-heals. That is not an oversight.

Self-heal reverts a change without asking. In staging that is what you want: the
cost of losing an experiment is nothing, and the value of staging matching git is
everything. In production, reverting an emergency change is itself an event. If
an operator scaled a service up during an incident, having a controller quietly
scale it back down two minutes later makes the incident worse and does it
invisibly.

So production drifts visibly instead. The Application shows `OutOfSync`, the
diff is there to read, and a human decides whether the cluster or git is right.
The one that is right is not always git.

## What this does not catch

ArgoCD compares against what the chart renders. Anything outside the
Application's scope is invisible to it: a Secret created by hand in the
namespace, a NetworkPolicy applied from someone's laptop, a change to a resource
the chart does not manage. Drift detection covers what git claims to own, and
nothing else.
