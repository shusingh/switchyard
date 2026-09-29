# Switchyard: Design

> A prefix-cache-aware router for LLM inference. Switchyard sits in front of a
> pool of OpenAI-compatible model servers (vLLM) and sends each request to the
> replica that can serve it fastest, weighing how much of the prompt that
> replica already has cached against how busy it is.

Status: **Draft v1** (planning phase). This document is the source of truth for
architecture. When an implementation decision changes it, update this file in
the same commit, and record significant decisions as a new ADR (see
[section 15](#15-decision-log)).

---

## Contents

1. [Problem](#1-problem)
2. [Goals and non-goals](#2-goals-and-non-goals)
3. [Background: how prefix caching works](#3-background-how-prefix-caching-works)
4. [Prior art](#4-prior-art)
5. [Architecture](#5-architecture)
6. [Prefix keys: turning a request into block hashes](#6-prefix-keys-turning-a-request-into-block-hashes)
7. [The prefix index](#7-the-prefix-index)
8. [Load signals](#8-load-signals)
9. [Routing policies and the cost model](#9-routing-policies-and-the-cost-model)
10. [Admission control](#10-admission-control)
11. [The proxy path: streaming, cancellation, retries](#11-the-proxy-path-streaming-cancellation-retries)
12. [Backend management and resilience](#12-backend-management-and-resilience)
13. [Observability](#13-observability)
14. [Test and benchmark tooling](#14-test-and-benchmark-tooling)
15. [Decision log](#15-decision-log)
16. [Open questions and risks](#16-open-questions-and-risks)
17. [References](#17-references)

---

## 1. Problem

A single model replica serves a limited number of concurrent requests, so
production deployments run several replicas behind a router. Every request has
two phases:

- **Prefill:** the engine processes the whole prompt and builds its KV cache.
  Cost grows with prompt length. Prefill dominates time to first token (TTFT).
- **Decode:** the engine generates output tokens one at a time, reusing the KV
  cache.

Engines such as vLLM keep the KV cache of recent prompts and reuse it when a new
prompt starts with the same tokens (automatic prefix caching). A request whose
first 30,000 tokens are already cached only prefills the remainder.

Real traffic shares long prefixes:

- Agents resend the same system prompt and tool definitions on every step, and
  the conversation grows by a small delta each turn.
- Chat resends the entire history with each new message.
- Applications share a per-tenant preamble (instructions, retrieved documents).

A cache-blind router (round-robin, random, least-loaded) scatters related
requests across replicas. Each replica recomputes prefixes another replica
already holds, the same prefix is duplicated in several caches, useful entries
get evicted (cache thrashing), and prefill work multiplies. Published
measurements show this is not a marginal effect: under a shared-prefix workload
llm-d measured cache-blind scheduling at roughly half the throughput and two
orders of magnitude worse P90 TTFT than precise prefix-aware scheduling
([llm-d, "KV-Cache Wins You Can See"](https://llm-d.ai/blog/kvcache-wins-you-can-see)).

The opposite extreme is also wrong. Pure affinity (always send a prefix to the
replica that has it) piles a popular prefix onto one replica while others idle.
The router has to **trade cache reuse against load**, request by request.

## 2. Goals and non-goals

### Goals

1. **OpenAI-compatible gateway.** Accept `POST /v1/chat/completions` and
   `POST /v1/completions` (streaming and non-streaming) and proxy them to a pool
   of vLLM replicas without changing response semantics.
2. **Prefix-aware, load-aware routing.** Pick the replica with the lowest
   estimated TTFT, combining predicted cache overlap with live load.
3. **Pluggable policies** so baselines (random, round-robin, least-loaded,
   power-of-two-choices, pure prefix affinity) run through the exact same code
   path and can be benchmarked fairly.
4. **Admission control.** Per-tenant token budgets, a bounded queue with
   deadlines, weighted fairness across tenants, and explicit load shedding
   (`429` with `Retry-After`) instead of unbounded queuing.
5. **Correct streaming.** Server-sent events flushed per event, client
   disconnects cancel upstream work, retries only happen before any byte reaches
   the client.
6. **Operability.** Health checks, circuit breaking, graceful drain, Prometheus
   metrics, structured logs, and per-request routing explanations.
7. **Reproducible benchmarks.** A load generator that replays real traces
   (Mooncake) and synthetic agent sessions open-loop, and a documented
   methodology that anyone can rerun.
8. **Low overhead.** Routing decision plus proxy overhead under 1 ms at p99
   (excluding upstream time) at 1,000 requests per second on a laptop-class CPU.

### Non-goals (for v1)

- Kubernetes integration, service discovery, or a control plane. Backends are a
  static list in config. (Gateway API Inference Extension and llm-d own that
  space; Switchyard is a single binary.)
- Disaggregated prefill/decode, KV cache transfer or offload.
- Multi-model routing, LoRA adapter placement, or model autoscaling.
- Authentication beyond identifying a tenant by API key or header.
- Supporting engines other than vLLM for **precise** mode. Approximate mode works
  with any OpenAI-compatible server.

## 3. Background: how prefix caching works

Facts the design depends on (vLLM v1; verify against the installed version in
Phase 0 and record any differences in a new ADR):

- **Block granularity.** The KV cache is split into fixed-size blocks of tokens
  (`--block-size`, commonly 16). Only **full** blocks are cached and reused.
- **Chained block hashes.** Each block's hash covers the parent block's hash,
  the token IDs in the block, and extra keys (LoRA ID, multimodal hashes, cache
  salt). A block hash therefore identifies the entire prefix up to and including
  that block. Hash algorithm is set by `--prefix-caching-hash-algo`; `sha256_cbor`
  and `xxhash_cbor` are reproducible across languages
  ([vLLM prefix caching design](https://docs.vllm.ai/en/latest/design/prefix_caching.html)).
- **LRU eviction.** Free blocks are reused least-recently-used first. Because a
  request touches its whole prefix at once, earlier blocks of a prefix are never
  less recently used than later ones. Consequence: **a cached prefix is always
  contiguous from the start.** Switchyard's index relies on this (section 7).
- **KV events.** vLLM can publish `BlockStored`, `BlockRemoved`, and
  `AllBlocksCleared` events over ZeroMQ via
  `--kv-events-config '{"publisher": "zmq", "topic": "kv-events"}'`
  ([vLLM KV events example](https://docs.vllm.ai/en/latest/examples/features/kv_events/)).
- **Metrics.** `/metrics` exposes, among others, `vllm:num_requests_running`,
  `vllm:num_requests_waiting`, `vllm:kv_cache_usage_perc`,
  `vllm:prefix_cache_queries` and `vllm:prefix_cache_hits` (counters, in tokens),
  and `vllm:time_to_first_token_seconds`
  ([vLLM metrics design](https://docs.vllm.ai/en/stable/design/metrics/)).
- **Cache capacity is small relative to demand.** A replica's KV cache holds a
  bounded number of blocks. Prefix-aware routing matters most when the working
  set of hot prefixes exceeds one replica's cache but fits in the pool's
  combined cache. The benchmark must create that regime deliberately (section 14).

## 4. Prior art

Switchyard is not novel research. It is a focused, well-measured implementation
of ideas the serving community has converged on. Knowing the prior art shapes
both the design and the honest framing of results.

| System | Cache view | Load view | Selection | Notes |
|---|---|---|---|---|
| **SGLang Model Gateway** | Approximate radix tree per worker, updated from routed requests; LRU eviction every 120 s | Outstanding requests per worker | Highest prefix match if match ratio ≥ `cache_threshold` (0.3) and system balanced; otherwise least-loaded when imbalance exceeds `balance_abs_threshold` (64) and `balance_rel_threshold` (1.5) | Also: circuit breakers, retries with jittered backoff, token-rate limiting, bounded queue returning 429/408. Reported up to 1.9x throughput and 3.8x hit rate. |
| **llm-d / Gateway API Inference Extension** | Approximate: character blocks, chained hash, LRU index of what was routed where. Precise: vLLM KV events over ZMQ plus router-side tokenization | Queue depth and KV cache utilization scraped from model servers | Weighted sum of scorers, max-score picker | Precise mode includes speculative index entries to cover the gap between routing and event arrival. Go implementation (`llm-d-kv-cache`). |
| **NVIDIA Dynamo KV router** | KV events (default) or approximate LRU with TTL | Active prefill tokens and decode blocks per worker | Minimum of an explicit cost: prefill blocks minus credited overlap, plus projected decode blocks; optional softmax sampling with temperature | Closest in spirit to Switchyard's cost model. |
| **vLLM production-stack router** | Prefix-aware and KV-aware (LMCache) modes | Engine stats | Session, prefix, or KV-aware routing | Python. |
| **Preble (ICLR 2025)** | Global radix tree | Per-GPU load | E2: exploit cached prefix when reuse is large relative to new work, otherwise explore by load | Research system; reports 1.5x to 14.5x lower average latency vs SGLang baseline. |

What Switchyard does differently (the portfolio angle):

1. **Routes on estimated TTFT in seconds**, a cost with physical units that can
   be calibrated and checked against measured TTFT, instead of a unitless
   weighted score.
2. **Single static binary with no dependencies on Kubernetes**, so the whole
   system and benchmark run on one workstation.
3. **A simulator that shares the load generator and metrics pipeline** with the
   real benchmark, so policy experiments at 16+ replicas are cheap and the GPU
   runs validate them.
4. **Explainable decisions.** Every response can carry the predicted overlap,
   predicted TTFT per candidate, and the chosen replica, so prediction error is
   measurable.

## 5. Architecture

```text
                  clients (OpenAI-compatible HTTP, SSE)
                                  |
                   +--------------v---------------+
                   |          HTTP server          |  request ID, body limit,
                   |   /v1/chat/completions, ...   |  parse minimal fields
                   +--------------+---------------+
                                  |
                   +--------------v---------------+
                   |       Admission control       |  tenant token buckets,
                   |  (budgets, fair queue, shed)  |  bounded queue, deadlines
                   +--------------+---------------+
                                  |
     +----------------+   +-------v--------+   +--------------------+
     |  Prefix keyer   |-->|   Scheduler    |<--|   Load tracker      |
     | canonical bytes |   | policy + cost  |   | in-flight, pending  |
     | -> block hashes |   |     model      |   | prefill, scraped    |
     +----------------+   +-------+--------+   | vLLM metrics        |
             ^                    |            +---------+----------+
             |            +-------v--------+             ^
     +-------+--------+   |     Proxy      |-------------+
     |  Prefix index   |<--| stream, cancel,|  completion updates
     | (approx, later  |   | retry-before-  |
     |  precise)       |   | first-byte)    |
     +-------^--------+   +-------+--------+
             |                    |
     +-------+--------+   +-------v------------------------------+
     | KV event        |   |      Backend pool                     |
     | subscriber      |   |  health checks, circuit breakers,     |
     | (phase 8)       |   |  generations, metrics scraper         |
     +----------------+   +-------+------------------------------+
                                  |
              +-------------+-----+-------+-------------+
              |             |             |             |
           vLLM #1       vLLM #2       vLLM #3       vLLM #4
```

### Request lifecycle

1. **Accept.** Assign a request ID, enforce the body size limit, and decode only
   the fields the router needs (`model`, `messages` or `prompt`, `tools`,
   `stream`, `stream_options`, `max_tokens` / `max_completion_tokens`). The raw
   body bytes are kept and forwarded unchanged, except for the single
   `stream_options.include_usage` rewrite described in section 11.
2. **Admit.** Identify the tenant, estimate token cost, and either admit, queue
   (bounded, with a deadline), or reject with `429`.
3. **Key.** Compute the chained block hashes of the canonical prompt (section 6).
4. **Score.** For each healthy backend: look up the cached prefix length in the
   index, read live load, estimate TTFT, choose the minimum (section 9).
5. **Reserve.** Increment the chosen backend's in-flight and pending-prefill
   counters and speculatively insert the request's blocks into the index for
   that backend, so concurrent requests with the same prefix see it immediately.
6. **Proxy.** Forward the request, stream the response back event by event,
   record time to first byte and first token, and propagate cancellation.
7. **Settle.** On first token, move the request's prefill estimate from
   "pending" to "decoding". On completion, release counters, record usage and
   latency metrics, and record prediction error (predicted vs actual TTFT).

### Repository layout

The layout follows the official Go guidance for server projects
([Organizing a Go module](https://go.dev/doc/modules/layout)): binaries under
`cmd/`, all implementation under `internal/` so nothing becomes an accidental
public API, and non-Go assets in clearly named top-level directories.

```text
switchyard/
├── cmd/                      one directory per binary; main packages only
│   ├── switchyard/           the router: flags, config, wiring, signal handling
│   ├── simengine/            simulated OpenAI-compatible engine (tests, experiments)
│   └── loadgen/              open-loop workload replayer, recorder, and reporter
├── internal/                 all implementation; not importable by other modules
│   ├── openai/               wire types for the subset of the OpenAI API we touch,
│   │                         error bodies, SSE event reader and writer
│   ├── config/               config types, loading, defaults, validation
│   ├── router/               assembles the router from config; serve and drain
│   ├── server/               HTTP handlers, middleware, retries, request lifecycle
│   ├── admission/            tenants, token buckets, fair queue, shedding
│   ├── prefix/               canonicalization, block hashing, the prefix index
│   ├── scheduler/            policies, TTFT estimator
│   ├── backend/              pool, health checks, load tickets, circuit breakers
│   ├── proxy/                upstream transport, streaming relay
│   ├── telemetry/            logger construction (metrics in Phase 6)
│   ├── sim/                  engine model behind simengine (cache, scheduler)
│   │   └── simtest/          starts simulated engines in tests
│   ├── workload/             trace parsers, generators, prompt synthesis
│   └── loadgen/              open-loop replay, per-request records, summaries
├── test/
│   └── e2e/                  black-box tests that run the real binaries
│                             (build tag `e2e`; excluded from the default test run)
├── configs/                  example configuration files, documented inline
├── deploy/
│   └── vllm/                 scripts and notes for running vLLM replicas
├── bench/
│   ├── scenarios/            versioned benchmark scenario definitions
│   ├── analysis/             scripts that turn raw results into tables and plots
│   └── results/              committed summaries; raw JSONL is git-ignored
├── scripts/                  developer scripts (environment setup, git hooks)
├── docs/
│   ├── adr/                  architecture decision records, numbered, immutable
│   ├── engineering/          design, plan, coding standards, session handoff
│   └── *.md                  user-facing docs: configuration, benchmarks, operations
├── .github/workflows/        CI (added with the first Go package in Phase 1)
├── Makefile                  the single entry point for build, test, lint, bench
├── .golangci.yml             linter configuration (see coding-standards.md)
├── .editorconfig             whitespace and line-ending rules for all editors
├── CONTRIBUTING.md           how to set up, build, test, and submit changes
├── README.md
└── LICENSE
```

Rules that keep the layout honest:

- **Packages are created when they get code**, each with a package comment
  stating its single responsibility (in `doc.go` when it is long). Empty placeholder packages are not
  committed.
- **Tests live next to the code they test** (`foo_test.go` beside `foo.go`).
  Test fixtures live in a `testdata/` directory inside the package, which the Go
  tool ignores. Only whole-system tests that launch real binaries go in
  `test/e2e/`.
- **No `pkg/` directory.** Nothing here is meant to be imported by other
  modules. If that changes, the package moves out of `internal/` deliberately.
- **No grab-bag packages** (`util`, `common`, `helpers`). Shared test helpers
  live in an `internal/<area>/<area>test` package (for example,
  `internal/sim/simtest`), following the standard library's `httptest` pattern.
- **Binaries stay thin.** A `main` package parses flags, builds dependencies,
  and calls into `internal/`. Anything worth testing lives in `internal/`.

### Package dependencies

```text
cmd/switchyard ──> router ──> server ──> admission
                                │  ├───> scheduler ──> backend
                                │  ├───> prefix
                                │  └───> proxy ─────> backend
             (all of the above) ──> openai, config, telemetry

cmd/simengine ──> sim ──> openai        (deliberately not prefix: see section 14.1)
cmd/loadgen   ──> loadgen ──> workload, openai
```

Dependencies point one way; there are no cycles, and nothing in `internal/`
imports a `cmd/` package. Interfaces are declared by the consumer (for
example, `scheduler` declares the small interface it needs from `backend`).
`openai` is a leaf package with no internal dependencies, so the router, the
simulator, and the load generator all speak exactly the same wire format.

## 6. Prefix keys: turning a request into block hashes

### Why not tokenize?

vLLM hashes **token IDs** after applying the chat template. Reproducing that in
the router requires the exact tokenizer and template for the model, which is
heavy (large vocab files, a CGO or Rust tokenizer) and model-specific. Two
options:

- **Approximate mode (v1 default):** hash canonical **bytes** of the request in
  fixed-size byte blocks. The router never knows exact token boundaries, but it
  does not need to: it only needs a stable, prefix-preserving key. Two requests
  share leading byte blocks if and only if they share a leading prefix of
  content, which is exactly the property that predicts shared cached tokens.
- **Precise mode (phase 8, stretch):** obtain token IDs from the replica's
  `/tokenize` endpoint and compute vLLM-compatible block hashes so the index can
  be driven by real KV events. See section 7.4.

### Canonical byte stream

For `/v1/chat/completions`, the canonical stream mirrors the order a chat
template renders content, so that byte-prefix sharing tracks token-prefix
sharing:

```text
"model" 0x00 <model name> 0x1E
"tools" 0x00 <compact JSON of tools array, as received> 0x1E      (if present)
for each message in order:
  <role> 0x00 <name if any> 0x00 <content> 0x1E
```

- Content that is an array of parts contributes text parts in order; non-text
  parts (images) contribute a fixed marker plus a hash of their URL or data, so
  they still differentiate prefixes.
- Tool calls and tool results contribute their JSON as received.
- `0x00` and `0x1E` separators prevent ambiguous concatenations (for example,
  role `"us"` + content `"er..."` versus role `"user"`).
- The model name leads the stream so identical prompts to different models never
  collide.
- **Byte-exact:** no whitespace normalization or JSON re-marshaling of content.
  The engine sees the bytes the client sent, so the key must too. Fields are
  read with `json.Decoder` into `json.RawMessage` where exactness matters.

For `/v1/completions`, the stream is the model name followed by the prompt
string (or each prompt of a batch, which is routed as one request keyed on the
first prompt).

### Block hashing

```text
h[0] = H(seed, block[0])
h[i] = H(h[i-1], block[i])
```

- **Block size:** default 128 bytes (roughly 32 tokens at about 4 bytes per
  token). Configurable. Smaller blocks track the real cache more finely but grow
  the index; Phase 3 benchmarks 64, 128, and 256.
- **Only full blocks** are hashed, matching the engine, which only caches full
  blocks. A trailing partial block is ignored.
- **Cap:** at most `maxBlocks` (default 4,096, about 512 KB of prompt) hashes per
  request. Beyond the cap, matching stops; this bounds CPU and memory per
  request.
- **Hash function:** 64-bit, non-cryptographic, fast. Use `hash/maphash` from the
  standard library (hardware-accelerated, zero dependencies). Its seed is random
  per process, which is fine because hashes live only in memory. Collision
  probability at 64 bits across millions of live blocks is negligible for a
  routing hint; a collision can only cause a suboptimal route, never a wrong
  answer, because correctness is the engine's job.
- **Hot path budget:** hashing 512 KB with maphash is on the order of tens of
  microseconds. Measure in Phase 3 with `go test -bench`.

## 7. The prefix index

### 7.1 What it answers

`Match(hashes) -> map[backend]matchedBlocks`: for each backend, how many leading
blocks of this request are believed to be cached there.

### 7.2 Approximate index (v1)

The index records what Switchyard **sent** where, not what the engine actually
holds.

- **Structure:** a map from block hash to a small slice of nodes, at most one
  per backend, each holding `(backend, generation, lastUsed)`. One
  `sync.RWMutex` guards the index: matches take the shared lock and run in
  parallel, inserts take the exclusive lock. Sharding was the original plan;
  it is deferred until a benchmark shows lock contention (simplest correct
  version first).
- **Insert (on route):** for the chosen backend, upsert every block hash of the
  request with the current time, **before** forwarding, so concurrent requests
  with the same prefix see the belief immediately. Blocks are inserted last to
  first, so the first block is the most recently used and an over-budget
  backend evicts old prefixes from their tails, as vLLM does.
- **Maintained under every policy.** Cache-blind baselines ignore the index but
  still pay for keying, matching, and inserting, so per-request overhead is
  identical when policies are compared.
- **Match:** a single pass over the request's blocks. For block i, one map
  lookup advances every backend whose match so far is exactly i; the pass ends
  at the first block no backend holds. Cost is O(k) lookups for k blocks and it
  allocates nothing. Measured on the development machine: about 44 µs for
  1,024 blocks across 16 backends, 190 µs for 4,096.
  - *Binary search was considered and rejected.* It would be about 20x faster
    on 4,096-block prompts but assumes every cached prefix is contiguous from
    the start. That does not hold exactly: when eviction orders of different
    prefixes interleave, a later block can outlive an earlier one (the
    simulator's cache test reproduces this). The linear pass is exact against
    the index's state and its worst case fits the overhead budget.
- **Capacity and eviction:** each backend has a block budget derived from its
  real KV capacity (configured, or read from vLLM at startup), converted to
  Switchyard blocks. Per-backend LRU order is maintained with an intrusive list;
  inserting beyond the budget evicts the least recently used entries for that
  backend. A TTL (default 10 minutes) evicts entries the engine has almost
  certainly dropped.
- **Invalidation:** each backend has a **generation** number. When a backend
  restarts or its health flaps, its generation increments. Entries with a stale
  generation are treated as absent and lazily removed. This makes "forget
  everything about backend b" O(1).
- **Drift:** the approximate index can believe a prefix is cached after the
  engine evicted it under memory pressure. The cost model tolerates this
  (a wrong belief costs one slower request, and the entry is refreshed), and
  Phase 7 measures prediction error to quantify drift.

### 7.3 Memory bound

Entry size is about 40 bytes plus map overhead. With 4 backends, 1M blocks each
at 128 bytes per block (about 128 MB of prompt text per backend), the index
holds 4M entries, roughly 300 to 400 MB in the worst case. Default budgets are
far lower; the budget is a config value with a documented formula, and the index
exports its size as a metric.

### 7.4 Precise index (`routing.prefix_mode: precise`)

Driven by the engines' KV cache events instead of routing history. The spike
that gated this mode passed: Go reproduces vLLM's block hashes bit for bit.

**Keys.** vLLM hashes each full block of 16 tokens as SHA-256 over the
canonical CBOR encoding of `(parent hash, block token IDs, extra keys)`,
starting from the hash of the string `"vllm-none-hash"`
(`--prefix-caching-hash-algo sha256_cbor`; vLLM's default `sha256` hashes a
Python pickle and cannot be reproduced elsewhere). `prefix.VLLMBlockHashes`
implements this with a hand-written encoder for the small CBOR subset
involved. Golden vectors produced by vLLM's own functions, covering every CBOR
integer width, are checked in `internal/prefix/testdata`. Index keys are the
low 64 bits of each hash, which is also what vLLM publishes when
`VLLM_KV_EVENTS_USE_INT_BLOCK_HASHES` is set.

**Tokens.** The router cannot tokenize by itself without the model's
tokenizer and chat template, so it asks an available engine through
`POST /tokenize` (rotating across engines), sending only the prompt-shaping
fields plus `add_generation_prompt: true`, as chat completions do. If
tokenizing fails, the request is routed with no prefix information rather
than failed. Cost is exported as `switchyard_tokenize_seconds`.

**Events.** One `kvevents.Subscriber` per backend connects to its ZeroMQ
publisher (pure-Go `github.com/go-zeromq/zmq4`, ADR 0010). Messages are
`(topic, 8-byte big-endian sequence, msgpack batch)`; a batch is
`[timestamp, events, rank?]` and each event a map tagged by class. Stored
blocks are inserted into the index, removed blocks forgotten, and a clear
empties the backend's entries. Only GPU-tier blocks are tracked.

**Trust.** Events published while a subscriber is disconnected are lost, so
the backend's entries are cleared after every (re)connection, on any gap in
sequence numbers (which also catches a publisher restart), and on a batch
that cannot be decoded. Routing still inserts speculative entries, covering
the few milliseconds before the engine's own event arrives; `BlockRemoved`
corrects any that turn out wrong.

**Hardening.** The batch decoder walks the payload with msgpack's streaming
API and checks every declared length against the payload size before
allocating. Fuzzing found that decoding into generic values let a 10-byte
payload declare a 1.9-billion-entry map; that input is a regression test.

**Verified live (2026-09-29).** Against the four vLLM 0.30 replicas, the
router's key for a 509-token prompt, computed from `/tokenize` output, equals
the key vLLM published in its `BlockStored` event (`d32b51541f725d2a`, 31
blocks). vLLM publishes events from its engine loop, so an idle engine holds
them until its next step: a cache reset on an idle replica reaches the router
only when that replica next does work. Evictions happen while engines are
busy, so this does not delay the information routing depends on.

**Budget.** The index budget is the engines' real block count
(`kv_capacity_tokens / engine_block_tokens`), and the estimator counts cached
tokens in engine blocks.

## 8. Load signals

| Signal | Source | Freshness | Used for |
|---|---|---|---|
| In-flight requests per backend | Router counters | Exact, instant | Decode load, imbalance guard |
| Pending prefill tokens per backend | Router: sum of estimated **uncached** prompt tokens of requests that have not produced a first token | Exact to the estimate, instant | Queueing term of estimated TTFT |
| Decoding requests per backend | Router: requests past first token, not finished | Instant | Decode interference term |
| `num_requests_waiting`, `num_requests_running` | vLLM `/metrics`, scraped | 250 to 1,000 ms | Sanity bound, detects load from other clients |
| `kv_cache_usage_perc` | vLLM `/metrics` | 250 to 1,000 ms | Pressure signal, index trust |
| Prefill throughput (tokens/s) | Calibrated per backend, updated by EWMA from observed TTFT | Rolling | Converts tokens to seconds |

Router-side counters are primary because they are exact and instantaneous for
traffic that flows through Switchyard. Scraped metrics catch what the router
cannot see (traffic from other clients, engine-internal queueing) and are used
as a floor, not overwritten by.

Token estimates use `len(bytes) / bytesPerToken` with a configurable ratio
(default 4.0), refined per backend from `usage.prompt_tokens` in responses.

## 9. Routing policies and the cost model

All policies implement one interface and run through the same pipeline, so
baselines are fair.

```go
// Policy chooses a backend for a request. Implementations must be safe for
// concurrent use and must not block: Pick runs on every request's hot path.
type Policy interface {
    Name() string
    Pick(req *Request, candidates []*backend.Backend) (*backend.Backend, error)
}
```

`Pick` takes no context because it does no I/O; everything it needs is in
memory. Candidates are the available backends (healthy, breaker not open),
whose live load each backend exposes. `Request` carries the model name, the
prompt's block count, the number of leading blocks believed cached on each
backend (computed by the server from the prefix index before `Pick`), and the
estimated prompt tokens. Policies that predict TTFT record the prediction in
`Request.PredictedTTFT`; for other policies the server computes the same
prediction, so every policy reports prediction error (section 13).

| Policy | Purpose |
|---|---|
| `random` | Control group |
| `round_robin` | The common default |
| `least_loaded` | Fewest in-flight requests |
| `p2c` | Power of two choices on in-flight requests |
| `prefix_affinity` | Longest match wins; ties broken by load. No load override. Shows the hot-spot failure mode |
| `estimated_ttft` | **Switchyard's policy.** Minimum estimated TTFT with an imbalance guard |

### The `estimated_ttft` cost model

For each healthy backend `b` and request `r`:

```text
uncached(b)   = promptTokens(r) - matchedTokens(b, r)
queue(b)      = pendingPrefillTokens(b)
ttft(b)       = (queue(b) + uncached(b)) / prefillRate(b)
              + decodePenalty * decoding(b)
              + staleTrust(b)
```

- `prefillRate(b)` is tokens per second, calibrated at startup from a short
  probe and refined by EWMA from observed TTFT. It makes the cost physical: the
  output is seconds, so predicted and measured TTFT can be compared directly.
- `decodePenalty` captures the fact that a busy decode batch slows prefill
  scheduling (chunked prefill shares steps with decode). Fitted from calibration
  data, default small.
- `staleTrust(b)` discounts cache credit when the backend's KV usage is near
  full (entries are more likely to have been evicted). Starts at zero; enabled
  if Phase 7 shows measurable drift.

Selection:

1. Compute `ttft(b)` for each candidate.
2. **Imbalance guard:** if the most and least loaded candidates differ by more
   than `balanceAbs` in-flight requests **and** by a ratio above `balanceRel`,
   drop candidates above the load ceiling before choosing. This bounds the damage
   from a very hot prefix and from estimation errors (same role as SGLang's
   thresholds).
3. Choose the minimum. Break ties within `epsilon` (default 5 percent)
   uniformly at random to avoid herding.

Why this handles the hot-prefix case: when many requests share one prefix, the
backend holding it accumulates `pendingPrefillTokens`. Once its queue exceeds
the cost of recomputing the prefix elsewhere, the model routes to another
backend, which then caches the prefix too. The hot prefix replicates exactly as
far as load demands, with no special casing.

Why not a weighted score: weights for unitless scores have to be tuned per
workload and cannot be validated. A cost in seconds can be checked against
reality on every request, and a large prediction error is itself a useful
alert.

## 10. Admission control

- **Tenant identity:** `Authorization: Bearer <key>` mapped through config to a
  tenant, or an `X-Tenant-ID` header when running behind a trusted gateway.
  Unknown tenants map to a `default` tenant with its own limits.
- **Token budgets:** each tenant has a token bucket in estimated tokens per
  second (prompt estimate plus `max_tokens`), with burst. On completion the
  charge is corrected using the actual `usage` so budgets track real
  consumption.
- **Global concurrency:** a cap on in-flight requests across all backends,
  derived from the pool's capacity.
- **Fair queue:** when the concurrency cap is reached, requests wait in
  per-tenant FIFO queues served by deficit round robin with tenant weights. A
  single heavy tenant cannot starve others.
- **Deadlines and shedding:** each queued request has a queue deadline (default
  30 s, capped by the client's context). Requests are rejected immediately with
  `429` and `Retry-After` when the queue is full or the estimated wait exceeds
  the deadline. Rejecting early is kinder than timing out late.
- **Errors:** OpenAI-style JSON error bodies (`{"error": {"message", "type",
  "code"}}`) so standard clients surface them properly.

## 11. The proxy path: streaming, cancellation, retries

- **Transport:** one shared `http.Transport` with tuned `MaxIdleConnsPerHost`,
  dial and TLS timeouts, HTTP/1.1 keep-alive to backends. No use of
  `http.DefaultClient`. `httputil.ReverseProxy` is not used, because retries
  before first byte and SSE inspection need finer control; the proxy is a small
  purpose-built component.
- **Streaming:** for `stream: true`, read upstream SSE line by line, write each
  complete event to the client, and flush immediately (`http.ResponseController`
  `Flush`). Events are relayed byte-for-byte.
- **First token detection:** the first `data:` event carrying a non-empty
  content delta marks TTFT. Only that event's JSON is inspected; later events
  are passed through without parsing, except the final usage event.
- **Usage accounting:** if the client did not request
  `stream_options.include_usage`, the router sets it upstream (the only body
  rewrite) and strips the extra usage-only event before relaying, so the client
  sees exactly what it asked for while the router still gets exact token counts.
- **Cancellation:** the upstream request uses the client request's context.
  When the client disconnects, the context is cancelled, the upstream connection
  closes, and vLLM aborts the sequence, freeing GPU capacity. Tested explicitly.
- **Timeouts:** dial, response header (time to first byte), and inter-event
  idle timeouts, each configurable. There is deliberately no overall deadline
  for streams, since long generations are legitimate; the idle timeout catches
  hung streams.
- **Retries:** a request is retried on the next-best candidate **only if no
  bytes have been written to the client** and the failure is retryable
  (connection refused or reset before headers, or a `502`, `503`, or `504`
  response). At most `maxRetries` (default 1) attempts, bounded by a retry
  budget (for example, retries may not exceed 10 percent of requests over a
  sliding window) to avoid retry storms. After the first byte, errors are
  reported in-stream and the stream is closed.

## 12. Backend management and resilience

- **Pool:** static list from config with per-backend capacity hints. Each
  backend has an ID, URL, generation, health state, circuit breaker, and load
  counters.
- **Active health checks:** `GET /health` on an interval (default 2 s, timeout
  1 s); unhealthy after 3 consecutive failures, healthy after 2 consecutive
  successes.
- **Passive outlier detection:** consecutive upstream failures open a
  per-backend circuit breaker (closed, open, half-open with a limited probe).
- **Restart detection:** a health transition from unhealthy to healthy, or a
  reset in a monotonic vLLM counter (for example, `prefix_cache_queries`
  decreasing), bumps the backend generation, which invalidates its index
  entries.
- **Graceful shutdown:** on SIGTERM, stop accepting new requests, let in-flight
  streams finish up to a drain timeout, then cancel what remains.
- **No healthy backends:** respond `503` with an OpenAI-style error.

## 13. Observability

- **Metrics (Prometheus text format on `/metrics`):**
  - `switchyard_requests_total{policy, backend, status}`
  - `switchyard_ttft_seconds`, `switchyard_tpot_seconds`,
    `switchyard_request_duration_seconds` (histograms, by backend)
  - `switchyard_queue_wait_seconds`, `switchyard_admission_rejections_total{reason}`
  - `switchyard_prefix_match_ratio` (histogram of matched / total blocks)
  - `switchyard_ttft_prediction_error_seconds` (histogram of predicted minus actual)
  - `switchyard_backend_inflight`, `switchyard_backend_pending_prefill_tokens`
  - `switchyard_index_entries`, `switchyard_index_evictions_total`
  - `switchyard_route_decision_seconds` (router overhead)
- **Logs:** `log/slog` JSON. One access log line per request with ID, tenant,
  policy, backend, matched blocks, predicted and actual TTFT, status, and
  durations. **Prompt and completion content is never logged**, only sizes and
  hashes.
- **Explain headers (opt-in, off by default):** `X-Switchyard-Backend`,
  `X-Switchyard-Matched-Blocks`, `X-Switchyard-Predicted-TTFT`. The load
  generator enables them to join routing decisions with measured latency.
- **Tracing (phase 6, optional):** OpenTelemetry spans for admit, key, score,
  and proxy, exported over OTLP when configured.

## 14. Test and benchmark tooling

### 14.1 Simulated engine (`simengine`)

An OpenAI-compatible HTTP server that behaves like a vLLM replica closely enough
to exercise routing policies:

- **A KV cache modeled on vLLM's:** a fixed number of 16-token blocks; full
  prompt blocks indexed by chained hash and shared between sequences;
  reference-counted while sequences run; unreferenced blocks evicted least
  recently used, tail of a prefix first; `/reset_prefix_cache`.
- **A scheduler modeled on vLLM v1's:** first-come-first-served admission
  into free cache space; each step spends a prefill token budget (chunked
  prefill) and gives every decoding sequence one token; step time is a fixed
  overhead plus prefill tokens over prefill throughput plus a per-sequence
  decode cost. Queueing, batching, and interference emerge from this; latency
  is never scripted.
- **Independent of the router's keys.** The engine renders requests through
  its own chat template and converts bytes to tokens with its own ratio, then
  hashes 16-token blocks. The router hashes raw request bytes in its own block
  size. If the simulator reused the router's keys, the router's cache
  predictions would be perfect by construction and every simulated result
  would flatter it.
- **Calibrated token accounting.** The default 5.55 bytes per token matches the
  benchmark's synthetic text, which Qwen2.5's tokenizer encodes at exactly one
  token per word (verified word by word through vLLM's `/tokenize`), so
  simulated working sets match the GPU setup's.
- Exposes vLLM-shaped `/metrics` (`num_requests_running`,
  `num_requests_waiting`, `kv_cache_usage_perc`, `prefix_cache_queries_total`,
  `prefix_cache_hits_total`, `cache_config_info`) and `/health`.

Uses: deterministic integration tests in CI (no GPU), fault injection (slow,
failing, restarting replicas), and cheap policy experiments at 8 to 32 replicas.
Simulator results are always labeled as simulated and never presented as GPU
measurements.

### 14.2 Load generator (`loadgen`)

- **Open-loop arrivals** (Poisson or trace timestamps). A closed loop hides
  queueing delay (coordinated omission) and would flatter every policy.
- **Workloads:**
  - `mooncake`: replay `toolagent_trace.jsonl` and `conversation_trace.jsonl`
    from Mooncake (fields: `timestamp`, `input_length`, `output_length`,
    `hash_ids`, one hash ID per 512-token block). Prompts are synthesized so that
    requests sharing leading `hash_ids` share byte-identical leading text,
    deterministically from a seed.
  - `agent`: synthetic agent sessions. N applications, each with a system prompt
    plus tool definitions (several thousand tokens); sessions grow by a
    tool-call and tool-result delta per turn; think times bimodal (median about
    2 s, long tail), matching published analyses of coding-agent traces.
  - `prefix_repetition`: K shared prefixes of length P with unique suffixes,
    matching `vllm bench serve --dataset-name prefix_repetition` for cross-checks.
- **Recording:** one JSONL line per request (scheduled time, send time, TTFT,
  TPOT, end-to-end, tokens, status, backend and explain headers). Analysis
  scripts compute percentiles, throughput, goodput under a TTFT SLO, and cache
  hit rate from vLLM counter deltas.

### 14.3 Benchmark methodology (GPU)

- **Hardware and replicas (ADR 0009):** RTX 4090 (24 GB) under WSL2, four
  vLLM 0.30.0 replicas of `Qwen/Qwen2.5-1.5B-Instruct` on the same GPU, each
  with a fixed 1 GiB KV cache (`--kv-cache-memory-bytes`): exactly 2,340
  blocks of 16 tokens, 37,440 tokens per replica, 149,760 in the pool. Hot
  working sets are sized to exceed one replica's cache and fit in the pool.
  Full versions and flags are recorded in `deploy/vllm/README.md` and must be
  restated with every result.
- **Cache reset:** `POST /reset_prefix_cache` (available with
  `VLLM_SERVER_DEV_MODE=1`) empties each replica's cache between trials, so
  no restart is needed.
- **Procedure:** warm up, then for each policy and each arrival rate in a sweep,
  run 3 trials, reset replica caches between trials,
  and report the median with min and max.
- **Metrics reported:** TTFT p50/p90/p99, TPOT p50/p99, throughput (output
  tokens/s), goodput at a TTFT SLO, pool prefix-cache hit rate, router overhead.
- **Honest caveats, stated in the results:** replicas share one GPU, so they
  contend for compute in a way separate GPUs would not; results compare routing
  policies under identical conditions and are not claims about production
  deployments. Optional validation run on rented multi-GPU hardware (for example
  4x L4) if budget allows.

### 14.4 Testing strategy

- Unit tests (table-driven) for canonicalization, hashing, index, cost model,
  token buckets, fair queue, SSE parsing.
- Fuzz tests for request parsing and canonicalization (no panics, deterministic
  keys, prefix property holds).
- Property tests: shared canonical prefix implies shared leading hashes; index
  match never exceeds inserted length; generation bump hides all entries.
- Integration tests: router plus simengine replicas via `httptest`, covering
  streaming relay fidelity, cancellation reaching the backend, retry before
  first byte only, shedding, drain on shutdown, backend restart.
- Race detector on every test run; goroutine leak checks in integration tests.
- Microbenchmarks with `-benchmem` for keying, index match and insert, and
  policy pick, tracked across phases.

## 15. Decision log

Significant decisions are recorded as architecture decision records in
[`docs/adr/`](../adr/README.md). This table is the index. To change a decision,
write a new ADR that supersedes the old one; do not edit accepted ADRs.

| ADR | Decision | Status |
|---|---|---|
| [0001](../adr/0001-record-architecture-decisions.md) | Record architecture decisions as ADRs | Accepted |
| [0002](../adr/0002-use-go.md) | Use Go for the router, simulator, and load generator | Accepted |
| [0003](../adr/0003-approximate-byte-block-prefix-hashing.md) | Key prefixes by hashing canonical bytes, not tokens | Accepted |
| [0004](../adr/0004-route-on-estimated-ttft.md) | Route on estimated time to first token | Accepted |
| [0005](../adr/0005-build-a-simulated-engine.md) | Build a simulated engine alongside real benchmarks | Accepted |
| [0006](../adr/0006-open-loop-load-generation.md) | Generate load open-loop | Accepted |
| [0007](../adr/0007-standard-library-first.md) | Prefer the standard library; justify every dependency | Accepted |
| [0008](../adr/0008-go-1-27-toolchain.md) | Target the Go 1.27 toolchain | Accepted |
| [0009](../adr/0009-benchmark-replica-configuration.md) | Benchmark on four small replicas with fixed 1 GiB KV caches | Accepted |

## 16. Open questions and risks

| Item | Type | Plan |
|---|---|---|
| Can four vLLM replicas share one 4090 with small, fixed KV caches reliably? | Risk | Phase 0 spike; fallback to 2 or 3 replicas, or a smaller model |
| Byte-to-token ratio varies by content (code, CJK) | Risk | Per-backend EWMA correction from `usage.prompt_tokens`; measure prediction error |
| Approximate index drift under memory pressure | Risk | Quantify in Phase 7 via prediction error and vLLM hit counters; `staleTrust` term if needed |
| Same-GPU contention distorts absolute latencies | Risk | State plainly; compare policies only under identical conditions; optional multi-GPU validation |
| vLLM hash compatibility for precise mode | Risk | Phase 8 starts with a spike; drop if it fails |
| Chat template ordering (tools rendered inside system prompt) may differ by model | Question | Canonical order puts tools before messages; verify on the chosen model in Phase 3 |
| How to reset replica caches between trials | Question | Check for a reset endpoint in the installed vLLM; otherwise restart replicas |

## 17. References

Routing systems and prior art:

- SGLang Model Gateway: https://docs.sglang.io/advanced_features/sgl_model_gateway.html
- SGLang v0.4 cache-aware load balancer: https://www.lmsys.org/blog/2024-12-04-sglang-v0-4/
- llm-d prefix-cache-aware routing: https://github.com/llm-d/llm-d/blob/main/docs/architecture/advanced/kv-management/prefix-cache-aware-routing.md
- llm-d precise prefix cache routing: https://llm-d.ai/docs/well-lit-paths/foundations/precise-prefix-cache-routing
- llm-d benchmark write-up: https://llm-d.ai/blog/kvcache-wins-you-can-see
- llm-d KV cache library (Go): https://github.com/llm-d/llm-d-kv-cache
- Gateway API Inference Extension: https://gateway-api-inference-extension.sigs.k8s.io/
- NVIDIA Dynamo router design: https://docs.nvidia.com/dynamo/knowledge-base/modular-components/router/router-design
- vLLM production-stack prefix-aware routing: https://docs.vllm.ai/projects/production-stack/en/latest/use_cases/prefix-aware-routing.html
- Preble (ICLR 2025): https://arxiv.org/abs/2407.00023

vLLM internals:

- Automatic prefix caching design: https://docs.vllm.ai/en/latest/design/prefix_caching.html
- KV events example: https://docs.vllm.ai/en/latest/examples/features/kv_events/
- Metrics: https://docs.vllm.ai/en/stable/design/metrics/
- `vllm bench serve`: https://docs.vllm.ai/en/stable/cli/bench/serve/

Workloads and traces:

- Mooncake traces (FAST '25): https://github.com/kvcache-ai/Mooncake/tree/main/FAST25-release/traces
- Agentic KV cache trace analysis: https://github.com/gauravapiscean/agentic-kv-cache
- KVCache in the wild (cloud provider characterization): https://arxiv.org/abs/2506.02634

Environment:

- vLLM on Windows via WSL2: https://docs.tokios.com/guides/vllm-windows-wsl2
