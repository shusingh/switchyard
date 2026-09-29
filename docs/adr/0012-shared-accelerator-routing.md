# ADR 0012: Simulate shared accelerators; keep estimated_ttft unchanged

- **Status:** Accepted
- **Date:** 2026-09-29

## Context

On the GPU benchmark (four vLLM replicas on one RTX 4090), estimated_ttft
reached a 62.7% cache hit rate and TTFT p50 688 ms, against prefix_affinity's
81.7% and 241 ms. In simulation the two had been equal. The simulator gave
every replica its own compute; the GPU replicas time-share one device.

## Decision

1. **Add a shared-device mode to the simulator.** `simengine -replicas N
   -shared-device` runs N engines on one simulated accelerator that executes
   one engine step at a time. On the agent workload it reproduces the GPU's
   ordering and hit rates (`bench/results/sim-agent-4-shared/`):
   prefix_affinity 77.8% (GPU 81.7%), estimated_ttft 54.1% (GPU 62.7%),
   round_robin 31.3% (GPU 32.7%). Absolute latencies are higher than on the
   GPU, which overlaps work across processes that the model serializes.

2. **Keep estimated_ttft as it is**, and recommend prefix_affinity for
   replicas that share an accelerator. Three changes to the estimator were
   tried in the shared-device simulator and none recovered its cache reuse:

   | Change | Goodput | Cache hit rate |
   |---|---:|---:|
   | Unchanged | 42.7% | 54.1% |
   | Charge each backend for its whole device's queued work | 17.8% | 35.2% |
   | The same, tie band measured above the shared queue | 16.7% | 32.0% |
   | The same, plus the delay a cache miss imposes on waiting requests | 19.0% | 36.1% |

   Disabling the imbalance guard did not change the unchanged policy's result
   (43.0%). The last variant also cost 2.6 points of hit rate on independent
   replicas. All were reverted.

## Consequences

- The simulator can now show policy behavior on shared accelerators, and
  results from it are labeled as such.
- estimated_ttft's advantage, spreading a hot prefix across replicas when its
  owner is overloaded, holds where replicas have their own compute. On a
  shared accelerator, spreading buys no extra compute and costs cache hits;
  prefix_affinity, which moves a session only rarely (32 moves in about 1,490
  follow-up turns against estimated_ttft's 641), is the better choice there.
- A cost model for shared accelerators remains open (design.md section 16).
  Why the device-aware variants failed is not established. What was
  measured: requests stayed spread evenly across replicas, yet sessions
  changed replica more often, not less (926 moves in 1,468 follow-up turns
  for the tie-band variant, against 641 unchanged). Tracing individual
  decisions is the next step if this is picked up.
