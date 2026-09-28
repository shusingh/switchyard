# Switchyard: Project Plan

The live checklist. Tick items as they land (in the same commit as the work),
and keep the **Status** block current. Architecture lives in
[DESIGN.md](DESIGN.md); how to write code lives in
[CODING_STANDARDS.md](CODING_STANDARDS.md); session-to-session context lives in
[HANDOFF.md](HANDOFF.md).

---

## Status

| Field | Value |
|---|---|
| Current phase | **Phase 0: Environment and scaffold** (not started) |
| Last completed | Planning (design, plan, standards, handoff written) |
| Next action | Upgrade Go to 1.27, install WSL2, then scaffold the module (Phase 0) |
| Blockers | WSL2 install needs an administrator shell and a reboot (owner action) |
| Last updated | 2026-09-28 |

---

## Outcome we are building toward

A public repo, `github.com/shusingh/switchyard`, containing:

1. A production-quality router binary with six routing policies, admission
   control, correct streaming, and full observability.
2. A simulator and open-loop load generator that make every experiment
   reproducible.
3. A benchmark report measured on real vLLM replicas, comparing Switchyard's
   `estimated_ttft` policy against standard baselines under stated conditions.
4. A README and a portfolio write-up, and one honest resume line phrased as a
   benchmark against named baselines (never as an improvement to a prior
   system).

## Definition of done (applies to every phase)

- `go build ./...`, `go vet ./...`, `golangci-lint run`, and
  `go test -race ./...` all pass.
- New behavior has tests; hot-path code has benchmarks.
- DESIGN.md is updated if behavior or architecture changed, with a decision-log
  entry for any real decision.
- PLAN.md checkboxes and Status block are updated.
- HANDOFF.md reflects the new state and the next concrete action.
- Commits follow CODING_STANDARDS.md (author is the repo owner, no co-author
  trailers, no tool attributions).

---

## Phase 0: Environment and scaffold

Goal: a working toolchain on this machine and an empty-but-real repo with CI.

**Toolchain (owner actions flagged)**

- [ ] Upgrade Go from 1.23.5 to 1.27.x (`winget upgrade GoLang.Go` or the
      installer from go.dev). *Owner action.*
- [ ] Install golangci-lint v2 and confirm `golangci-lint run` works.
- [ ] Install WSL2 with Ubuntu 24.04 (`wsl --install -d Ubuntu-24.04` from an
      admin shell, then reboot). *Owner action.*
- [ ] In WSL2: confirm `nvidia-smi` sees the RTX 4090, install `uv`, create a
      venv, install vLLM, record versions (vLLM, CUDA, driver, torch).
- [ ] **Spike:** run 4 vLLM replicas of a small instruct model on the one GPU,
      each with a capped KV cache, on ports 8001 to 8004. Confirm `/health`,
      `/metrics`, `/tokenize`, streaming chat completions, and the prefix-cache
      counters from Windows via `localhost`. Record exact flags in
      `deploy/vllm/README.md`. Decide replica count and model (update DESIGN
      section 14.3 and log the decision).
- [ ] Check whether the installed vLLM exposes a cache reset endpoint (for
      resetting caches between benchmark trials).

**Repository**

- [ ] `go mod init github.com/shusingh/switchyard`, `go 1.27` directive.
- [ ] Directory skeleton per DESIGN section 5 (only create packages as they
      get code; no empty placeholder packages).
- [ ] `.golangci.yml` per CODING_STANDARDS.md.
- [ ] `.gitignore` (binaries, `bench/results/**/*.jsonl`, trace downloads,
      `.env`).
- [ ] `LICENSE` (MIT or Apache-2.0: owner to choose).
- [ ] `README.md` stub: one paragraph plus "status: in development".
- [ ] Task runner: `Taskfile.yml` (go-task) or a small `scripts/` set that
      works on Windows and Linux. Targets: `build`, `test`, `lint`, `bench`,
      `sim-up`, `vllm-up`.
- [ ] GitHub Actions: build, vet, lint, `go test -race` on Linux; build check
      on Windows.
- [ ] Create the GitHub repo and push. *Owner confirms before pushing public.*

Exit criteria: CI green on an empty-but-linted module; vLLM replicas reachable
from Windows; versions recorded.

---

## Phase 1: Walking skeleton (pass-through proxy)

Goal: requests flow end to end through Switchyard to backends with
round-robin, including correct streaming and cancellation.

- [ ] `internal/config`: typed config (YAML), strict decoding (unknown fields
      rejected), defaults, validation with clear errors. Flags override file.
- [ ] `internal/backend`: static pool, per-backend state, active health checks
      (`/health`, thresholds from DESIGN section 12).
- [ ] `internal/server`: `POST /v1/chat/completions`, `POST /v1/completions`,
      `GET /v1/models` (proxied from a healthy backend), `GET /healthz`,
      `GET /readyz`; request IDs; body size limit; OpenAI-style error bodies.
- [ ] `internal/proxy`: shared transport with explicit timeouts; non-streaming
      relay; SSE relay with per-event flush; client-cancel propagates upstream.
- [ ] `internal/scheduler`: `Policy` interface, `round_robin` and `random`.
- [ ] Minimal `simengine` (fixed latency, fake tokens, SSE) sufficient for
      integration tests.
- [ ] `cmd/switchyard`: wiring, `slog` JSON logger, graceful shutdown with
      drain timeout.
- [ ] Integration tests with `httptest`: streaming fidelity (byte-for-byte),
      cancellation reaches the backend, unhealthy backend skipped, `503` with no
      healthy backends, drain on shutdown.
- [ ] Manual check against real vLLM replicas with the `openai` Python client
      and `curl -N`.

Exit criteria: an OpenAI client pointed at Switchyard cannot tell it apart from
a single vLLM server, except that load is spread.

---

## Phase 2: Simulator and load generator

Goal: a realistic, deterministic test bed and the measurement pipeline, before
any smart routing exists.

- [ ] `internal/sim`: block prefix cache with LRU and fixed capacity; latency
      model (prefill proportional to uncached tokens, step scheduler with batch
      size, decode time rising with batch); vLLM-shaped `/metrics`.
- [ ] `simengine` flags: capacity, prefill rate, decode rate, batch size, fault
      injection (latency spikes, error rate, restart).
- [ ] `internal/workload`: Mooncake trace parser; deterministic prompt
      synthesis from `hash_ids`; `agent` session generator; `prefix_repetition`
      generator.
- [ ] `cmd/loadgen`: open-loop Poisson and trace-timestamp arrivals; streaming
      client that records TTFT and TPOT; JSONL output; explain headers captured.
- [ ] `bench/analysis`: script that turns JSONL into a summary table
      (percentiles, throughput, goodput at SLO, hit rate from counter deltas).
- [ ] Baseline policies: `least_loaded`, `p2c`.
- [ ] First simulated baseline report: random, round-robin, least-loaded, p2c on
      the `agent` workload with 4 and 16 replicas. Commit the summary, not the
      raw JSONL.

Exit criteria: one command runs a scenario end to end on the simulator and
produces a comparable summary table; results are reproducible from a seed.

---

## Phase 3: Prefix keys and the approximate index

Goal: the router knows, per request, how much of the prompt each backend
probably has cached.

- [ ] `internal/prefix`: canonical byte stream for chat and completions
      (DESIGN section 6), chained block hashing with `hash/maphash`, block size
      and block cap from config.
- [ ] Fuzz tests: no panics on arbitrary JSON; deterministic keys; the prefix
      property (shared canonical prefix gives shared leading hashes).
- [ ] Index: sharded map, per-backend LRU with budget and TTL, generations for
      O(1) invalidation, speculative insert on route.
- [ ] Match: linear scan with early exit; benchmark; switch to binary search
      only if it wins meaningfully.
- [ ] `prefix_affinity` policy.
- [ ] Microbenchmarks: keying at 1 KB, 32 KB, 512 KB prompts; match and insert
      at 4 and 16 backends; allocations per op.
- [ ] Block-size sweep on the simulator (64, 128, 256 bytes); record the
      decision.
- [ ] Verify canonical ordering against the chosen model's chat template
      (tools before messages) using real replicas and vLLM hit counters.

Exit criteria: on the simulator, `prefix_affinity` shows a much higher cache hit
rate than the baselines, and its hot-prefix failure mode is visible in the data.

---

## Phase 4: Load-aware cost model

Goal: `estimated_ttft`, the policy that trades cache reuse against load.

- [ ] Load tracker: in-flight, pending prefill tokens, decoding counts per
      backend, updated at reserve, first token, and completion.
- [ ] vLLM metrics scraper (interval, timeout, parse only needed series) as a
      load floor.
- [ ] Prefill-rate calibration probe at startup plus EWMA refinement from
      observed TTFT; bytes-per-token EWMA from `usage.prompt_tokens`.
- [ ] `estimated_ttft` policy with imbalance guard and randomized tie-breaking
      (DESIGN section 9).
- [ ] Prediction-error metric (predicted minus actual TTFT).
- [ ] Simulator experiments: agent workload, Mooncake replay, and a deliberate
      hot-prefix scenario; rate sweep; all six policies.
- [ ] Tune defaults from data; log each tuning decision with evidence.

Exit criteria: on the simulator, `estimated_ttft` matches `prefix_affinity` on
hit rate where load is balanced and beats it on tail TTFT under a hot prefix.

---

## Phase 5: Admission control and resilience

- [ ] Tenant identification (bearer key map, `X-Tenant-ID`), default tenant.
- [ ] Per-tenant token buckets on estimated tokens, corrected by actual usage.
- [ ] Global concurrency cap; per-tenant FIFO queues with deficit round robin
      and weights; queue deadlines; immediate `429` with `Retry-After` when
      shedding.
- [ ] Retries before first byte only, with a retry budget.
- [ ] Per-backend circuit breaker (closed, open, half-open).
- [ ] Restart detection and generation bump (health flap, counter reset).
- [ ] Idle timeout for streams; header timeout; dial timeout.
- [ ] Fault-injection tests on the simulator: backend crash mid-stream, slow
      backend, flapping backend, overload with two tenants (fairness holds).

Exit criteria: under overload, the router sheds early and fairly, never lets
latency grow unbounded, and survives backend failures without dropping
unaffected streams.

---

## Phase 6: Observability

- [ ] Prometheus metrics per DESIGN section 13 (use
      `github.com/prometheus/client_golang`; justified in the decision log).
- [ ] Access log line per request (no prompt content).
- [ ] Opt-in explain headers.
- [ ] Router overhead benchmark: requests per second and added latency with a
      zero-latency backend; target p99 overhead under 1 ms at 1,000 rps.
- [ ] Optional: OpenTelemetry spans (admit, key, score, proxy) behind config.
- [ ] Optional: Grafana dashboard JSON in `deploy/`.

---

## Phase 7: GPU benchmarks on real vLLM

- [ ] Final replica setup and model recorded (versions, flags, driver).
- [ ] Scenario files in `bench/scenarios/` for: Mooncake toolagent,
      Mooncake conversation, agent sessions, hot prefix.
- [ ] Rate sweep per policy, 3 trials each, caches reset between trials.
- [ ] Validate the simulator: compare simulated and measured trends; note
      where they diverge.
- [ ] Quantify approximate-index drift from vLLM hit counters vs predicted
      matches.
- [ ] Results report `docs/benchmarks.md`: setup, method, tables, charts,
      caveats (shared GPU), and exact commands to reproduce.

Exit criteria: a reproducible report whose headline numbers survive a skeptical
reader's questions about methodology.

---

## Phase 8 (stretch): Precise mode via KV events

- [ ] **Spike first:** compute vLLM-compatible block hashes in Go from
      `/tokenize` output with `sha256_cbor` and a fixed seed; prove equality
      against hashes seen in live `BlockStored` events. Stop here if it fails.
- [ ] ZMQ subscriber (`go-zeromq/zmq4`), msgpack decoding, per-backend ordered
      processing, replay on reconnect.
- [ ] Precise index with speculative entries.
- [ ] Benchmark approximate vs precise on the GPU setup.

---

## Phase 9: Polish and publish

- [ ] README: what it is, architecture diagram, quick start (simulator only,
      no GPU needed), results summary, design links.
- [ ] `docs/`: methodology, configuration reference, operations notes.
- [ ] Release `v0.1.0` with binaries for Linux and Windows (GoReleaser or a
      simple workflow).
- [ ] Portfolio: project page and a write-up on the cost model and the
      measurements.
- [ ] Resume line drafted from measured numbers, phrased as a benchmark with
      named baselines and conditions.
