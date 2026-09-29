# Switchyard

A prefix-cache-aware router for LLM inference, written in Go.

Switchyard sits in front of a pool of OpenAI-compatible model servers such as
[vLLM](https://github.com/vllm-project/vllm) and sends each request to the
replica that can serve it fastest. It weighs how much of the prompt each
replica already holds in its KV cache against how busy that replica is, and
routes on the predicted **time to first token**.

**Status:** feature-complete for its first release; formal GPU benchmarks in
progress. See [the plan](docs/engineering/plan.md).

## Why

Engines like vLLM cache the KV state of recent prompts and reuse it when a
new prompt starts the same way. A request whose first 30,000 tokens are
already cached only computes the rest.

Real traffic shares long prefixes. Agents resend the same system prompt and
tool definitions every step, and conversations resend their history every
turn. A cache-blind load balancer scatters related requests across replicas,
so each replica recomputes prefixes another one already holds. Pure affinity
fails the other way: a popular prefix piles onto one replica while the rest
sit idle.

Switchyard makes that trade explicitly, per request.

## How it works

```text
client ─> admission ─> prefix key ─> index match ─> policy ─> proxy ─> vLLM replica
          (tenants,    (hash chain   (which replica  (lowest    (streaming,
           fair queue)  of the        probably has   predicted   retries)
                        prompt)       which blocks)  TTFT)
```

1. **Admission.** Each tenant has a token budget; when the pool is saturated,
   requests wait in per-tenant queues served by weighted fair queuing, and
   overload is shed early with `429` and `Retry-After`.
2. **Prefix key.** The request is decoded once into a canonical byte form
   (model, tools, messages) and hashed in fixed-size blocks, each hash
   covering everything before it. Requests that share a prefix share leading
   hashes. No tokenizer is needed.
3. **Index.** The router remembers which blocks it sent to which replica,
   within each replica's real KV capacity, with LRU eviction, a TTL, and
   per-replica generations that discard beliefs when a replica restarts.
4. **Policy.** `estimated_ttft` predicts, for every replica, the time to first
   token: fixed overhead, plus the replica's queued prefill work and this
   request's uncached tokens at the replica's prefill throughput, plus a
   decode penalty. It picks the lowest. Throughput is learned per replica
   from observed latencies, so the model corrects itself; the prediction is
   in seconds, so it can be checked against reality on every request.
5. **Proxy.** Responses stream back event by event. A client disconnect
   cancels generation on the replica. Transient failures are retried on
   another replica before anything reaches the client, within a retry
   budget, and failing replicas are taken out of rotation by circuit
   breakers.

The full design, including the decisions and the alternatives rejected, is in
[docs/engineering/design.md](docs/engineering/design.md) and
[docs/adr/](docs/adr/README.md).

## Routing policies

| Policy | Chooses | Role |
|---|---|---|
| `estimated_ttft` | Lowest predicted time to first token | Switchyard's policy |
| `prefix_affinity` | Longest cached prefix, ties by load | Cache-only baseline |
| `least_loaded` | Fewest in-flight requests | Load-only baseline |
| `p2c` | Less loaded of two random replicas | Load-only baseline |
| `round_robin` | Next in rotation | Common default |
| `random` | Uniformly at random | Control group |

Every policy runs through the same request path, and prefix tracking runs
under all of them, so comparisons are fair.

## Quick start (no GPU needed)

Requires Go 1.27 and GNU Make.

```bash
make build
scripts/sim-cluster.sh start 4 estimated_ttft
curl -N http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"sim-model","stream":true,"messages":[{"role":"user","content":"Hello"}]}'
scripts/sim-cluster.sh stop
```

`sim-cluster.sh` starts four simulated engines (`cmd/simengine`) behind the
router. The simulated engine models vLLM's KV cache, chunked prefill, and
continuous batching, so routing behavior can be studied without hardware.

Run a workload and see the results:

```bash
scripts/bench-sim.sh bench/results/demo 4 "round_robin estimated_ttft" \
  -workload agent -duration 2m -rate 0.5
```

## Running against vLLM

See [deploy/vllm/README.md](deploy/vllm/README.md) for the reference setup
(four replicas on one RTX 4090 under WSL2), then:

```bash
make vllm-up
./bin/switchyard -config configs/local-vllm.yaml
```

Point any OpenAI client at `http://localhost:8080/v1`. Every option is
documented in [configs/switchyard.example.yaml](configs/switchyard.example.yaml).

## Observability

- `GET /metrics`: Prometheus metrics, including TTFT and TPOT per backend,
  queue wait, rejections, retries, the fraction of each prompt believed
  cached, TTFT prediction error, the router's own decision time, and live
  per-backend load and learned throughput.
- One structured JSON access log line per request, with predicted and actual
  TTFT. Prompt and completion content is never logged.
- `server.explain_headers: true` adds `X-Switchyard-*` headers describing each
  routing decision.

## Benchmarks

The load generator (`cmd/loadgen`) replays synthetic agent sessions,
shared-prefix traffic, and the public [Mooncake](https://github.com/kvcache-ai/Mooncake)
production traces open-loop, so latency includes queueing as real clients see
it. Results, methodology, and exact commands to reproduce them will be in
`docs/benchmarks.md`.

Router overhead, measured on Linux against an instant backend at 1,000
requests per second: **104 µs added at p50, 472 µs at p99.**

## Development

```bash
make setup   # install git hooks, check the toolchain
make check   # tidy, lint, race-enabled tests: what CI runs
make help    # every target
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and the
[coding standards](docs/engineering/coding-standards.md).

## License

[MIT](LICENSE)
