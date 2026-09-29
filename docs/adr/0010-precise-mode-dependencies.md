# ADR 0010: ZeroMQ and msgpack libraries for precise mode

- **Status:** Accepted
- **Date:** 2026-09-29

## Context

Precise mode (design.md section 7.4) drives the prefix index from the KV cache
events vLLM publishes instead of from routing history. vLLM publishes over
ZeroMQ PUB/SUB, as multipart messages whose payload is msgpack. The standard
library has neither a ZeroMQ implementation nor a msgpack codec, and both
protocols are too large to reimplement responsibly.

The phase started with a spike, as the design required: Go reproduces vLLM's
`sha256_cbor` block hashes bit for bit (verified against golden vectors from
vLLM's own functions), so the event keys are usable.

## Decision

- `github.com/go-zeromq/zmq4`: a pure-Go ZeroMQ implementation. It needs no
  C library, so the router stays a static binary that cross-compiles, and the
  llm-d project uses it for the same purpose.
- `github.com/vmihailenco/msgpack/v5`: a widely used msgpack codec that decodes
  into generic values, which suits msgspec's tagged-map encoding of events.

Both are used only by `internal/kvevents`. Approximate mode, the default, does
not load them.

## Consequences

Two dependencies with their own transitive modules enter `go.mod`; they are
reviewed with `govulncheck` before releases. If precise mode were removed, both
would go with it.
