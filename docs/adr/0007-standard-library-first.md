# ADR 0007: Prefer the standard library; justify every dependency

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

Each dependency adds supply-chain risk, upgrade work, and concepts a reader must learn. Go's standard library covers HTTP serving and clients, structured logging, fast hashing, and testing.

## Decision

Use the standard library by default: `net/http`, `log/slog`, `hash/maphash`, `testing`. Each external dependency needs an ADR or an entry in coding-standards.md section 13 stating why the standard library is insufficient. Pre-approved: Prometheus client, a YAML parser for config, `go-cmp` and `goleak` in tests.

## Consequences

A small, auditable dependency tree and code that reads like idiomatic Go. Some conveniences (routing frameworks, assertion libraries) are deliberately given up.
