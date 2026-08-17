# 3. Promotion strategy

Date: 2026-08-17

## Status

Accepted

## Context

Two environments share one chart and one image. What differs is when each one
gets a new tag, and what has to be true before it does. The choice is where the
gates go and what kind they are: a person, or a measurement.

The starting point was per-environment values files with a manual PR for
production. That answers "how does the tag change" but not "what stops a bad
tag", and it left production's approval as a single human click at the moment of
deploy, which is the least informed moment there is. The reviewer approving the
sync has seen the diff and nothing else.

The obvious alternative is ArgoCD Image Updater, which watches the registry and
writes the new tag itself. It removes the promotion PR entirely.

## Decision

Keep per-environment values files, and place three gates between a commit and
full production traffic.

1. Staging gets every green build automatically. CI writes the tag into
   `values-staging.yaml` and ArgoCD auto-syncs. No human is involved.
2. Production requires a promotion PR that bumps only the tag in
   `values-production.yaml`, and an operator approves the ArgoCD sync. This is
   the human gate, and it answers "should this ship now", not "does this work".
3. Once syncing, the canary answers "does this work" with measurements. The
   analysis run can abort at any step, and the rollout parks at 50% until the
   post-promotion smoke suite passes against the canary through its header
   route. The suite promotes on success and aborts on failure.

Separate values files rather than a shared file with environment blocks, because
the promotion diff should be one line in one file that a reviewer can read in
five seconds, and because a mistake in a staging value cannot reach production
without a separate commit.

Image Updater was rejected for production and would be redundant in staging. In
production it moves the decision out of git history and into controller
behaviour, so the answer to "who promoted this and when" stops being a commit.
In staging, CI already writes the tag as part of the build it just verified.

## Consequences

The rollback story is a git revert of the promotion PR. Nothing else has to be
remembered, and the reverted state is the state that was running.

Production's manual pause is released by the smoke gate rather than by a person,
which is deliberate. Asking a human to approve twice for the same change trains
them to click through both. The human decides whether to start, the measurements
decide whether to finish.

The gates cost time. A commit reaches full production traffic in roughly 20
minutes at the earliest, assuming the promotion PR merges immediately. For an
emergency fix that is too slow, and the escape hatch is a rollback rather than a
faster forward path: reverting the promotion PR returns to the previous tag,
which the rollout can serve immediately because the old ReplicaSet is still
scaled up for `scaleDownDelaySeconds`.

Staging and production run different rollout settings (shorter steps, looser
thresholds in staging), so a canary can pass in staging and fail in production.
That is the intended direction of the asymmetry, and it means staging proves the
analysis query works rather than proving the thresholds are right.
