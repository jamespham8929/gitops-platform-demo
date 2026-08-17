# 2. Progressive delivery with Argo Rollouts

Date: 2026-08-17

## Status

Accepted

## Context

Before this decision the chart shipped a plain Deployment with a rolling update.
That gives you one safety property: pods are replaced gradually and an unready
pod does not receive traffic. It gives you nothing at all against a build that
starts cleanly and then behaves badly, which is the common case. A service that
returns 500 on every order still passes its liveness probe, so a rolling update
walks it all the way to 100% of traffic and the first signal anyone gets is a
page.

ArgoCD closes the loop between git and the cluster. It does not decide whether
the new revision is any good. Something has to make that call, and the options
were:

1. Leave it to alerting. Deploy at full speed, page a human when the SLO burns.
2. Blue-green. Run both versions, cut over after a manual check.
3. Canary with automated analysis. Shift traffic in steps and measure the new
   version against a threshold at each step.

## Decision

Use Argo Rollouts with a canary strategy, and gate the steps on an
AnalysisTemplate that queries Prometheus.

The Rollout replaces the Deployment behind a `rollout.enabled` flag, so the
chart still installs on a cluster without the Rollouts controller. Both paths
render the same pod spec from one helper, so they cannot drift.

Two metrics decide the outcome:

- Success rate, non-5xx over total, thresholded at 99% in production.
- 95th percentile latency, thresholded at 400ms in production.

Both queries select on `service="sample-service-canary"`. Argo Rollouts keeps
the canary service's selector pinned to the canary pods, and prometheus-operator
stamps a `service` label on everything it scrapes through a service, so that one
label is enough to isolate canary traffic. The alternative, matching on
`rollouts-pod-template-hash`, needs extra relabeling to carry pod labels into
the sample and breaks quietly when that relabeling is missing.

The analysis runs in the background from step 2 rather than as a single
checkpoint. A canary that only degrades under sustained load passes a one-shot
check and fails a continuous one.

## Consequences

A bad build stops at 5% of production traffic and rolls back without anyone
being paged. The evidence is recorded: the AnalysisRun keeps every measurement
it took and why it failed.

The cost is real, and worth naming:

- The rollout is slow by design. Production takes at least 15 minutes of pauses
  before it can finish, and `progressDeadlineSeconds` has to exceed the sum of
  every pause plus the analysis, or a healthy rollout times out on its own
  schedule.
- The analysis is only as good as its query. A metric name change in the service
  silently turns the gate into a no-op, because a query returning no data is
  treated as no evidence of failure rather than as failure. CI checks that the
  metric names in the AnalysisTemplate are the ones the service actually
  exports, which catches the rename but not a semantic change.
- Treating no data as success is a deliberate choice. The alternative, failing
  closed, aborts every rollout that happens during a Prometheus outage or a
  quiet hour. The rollout's progress deadline bounds how long a silent canary
  can sit there.
- Two extra CRDs, Rollout and AnalysisTemplate, mean schema validation in CI
  needs their schemas, and `kubectl get deployment` no longer shows the
  workload.
