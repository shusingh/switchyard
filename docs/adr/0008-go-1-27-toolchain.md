# ADR 0008: Target the Go 1.27 toolchain

- **Status:** Accepted
- **Date:** 2026-09-28

## Context

The local machine had Go 1.23 installed. Go 1.27 was the current stable release in September 2026, and recent releases added features this project uses (`b.Loop` in 1.24, `testing/synctest` in 1.25).

## Decision

Set `go 1.27` in `go.mod` and use a Go 1.27 toolchain locally and in CI.

## Consequences

Access to current language and library features and security fixes. Contributors need a recent toolchain; the `go` directive makes that explicit.
