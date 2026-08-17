# 1. Record architecture decisions

Date: 2026-08-17

## Status

Accepted

## Context

The interesting part of this repo is not the sample service. It is the delivery
mechanics: what triggers a deploy, what evidence a change has to produce before
it reaches all of production, and who is allowed to stop it. Those choices are
hard to recover from the YAML alone, because YAML records the outcome and not
the alternatives that were rejected.

## Decision

Keep a short architecture decision record for each significant delivery choice,
using the format popularized by Michael Nygard. One file per decision, numbered,
never rewritten once accepted (superseded instead).

## Consequences

A reviewer can read the ADRs and understand why production pauses at 50% and why
the analysis query selects on a service label, without reconstructing the
reasoning from the chart.
