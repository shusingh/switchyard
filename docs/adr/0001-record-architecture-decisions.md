# ADR 0001: Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

Design choices in this project involve real trade-offs (tokenization, hashing, cost models, measurement method). A reviewer or future maintainer needs to know not just what was decided but why, and what was rejected.

## Decision

Record every significant architectural decision as an ADR in `docs/adr/`, numbered sequentially, using the Context, Decision, Consequences format. ADRs are immutable once accepted: a changed decision gets a new ADR that supersedes the old one, and the old one's status is updated to point at it. `docs/engineering/design.md` section 15 indexes all ADRs.

## Consequences

Decisions stay reviewable and traceable. Writing an ADR costs a few minutes per decision, which is small next to re-litigating a choice later.
