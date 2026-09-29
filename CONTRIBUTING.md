# Contributing to Switchyard

## Prerequisites

| Tool | Version | Notes |
|---|---|---|
| Go | 1.27 or newer | Matches the `go` directive in `go.mod` |
| golangci-lint | v2 | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |
| GNU Make | 4.x | On Windows, install with `scoop install make` and use Git Bash |
| vLLM (optional) | See `deploy/vllm/` | Only for GPU benchmarks. Linux or WSL2 with an NVIDIA GPU |

Nothing in the default build or test run needs a GPU or network access. The
simulated engine (`cmd/simengine`) stands in for vLLM in tests.

## First-time setup

```bash
git clone https://github.com/shusingh/switchyard.git
cd switchyard
make setup
```

`make setup` points git at the repository's hooks (`scripts/githooks/`) and
checks that the toolchain is present.

## Everyday commands

| Command | What it does |
|---|---|
| `make build` | Build all binaries into `./bin` |
| `make test` | Unit and integration tests with the race detector |
| `make test-e2e` | End-to-end tests against the built binaries |
| `make lint` | Run golangci-lint |
| `make fmt` | Format code |
| `make bench` | Run microbenchmarks with allocation counts |
| `make check` | Everything CI runs; run before every push |
| `make help` | List all targets |

## Repository layout

See [design.md section 5](docs/engineering/design.md#repository-layout) for the
full layout and the rules behind it. In short: binaries live in `cmd/`, all
implementation lives in `internal/`, tests sit next to the code they test, and
whole-system tests live in `test/e2e/`.

## Engineering documents

| Document | Purpose |
|---|---|
| [Design](docs/engineering/design.md) | Architecture, algorithms, and benchmark method |
| [Benchmarks](docs/benchmarks.md) | Method, results, and commands to reproduce them |
| [Operations](docs/operations.md) | Running the router in front of real model servers |
| [Coding standards](docs/engineering/coding-standards.md) | How code in this repo is written and reviewed |
| [ADRs](docs/adr/README.md) | Architecture decisions and their rationale |

## Making a change

1. Branch from `main`: `git switch -c feat/short-topic`.
2. Keep each commit to one logical change that builds and passes tests.
3. Follow the [coding standards](docs/engineering/coding-standards.md); section
   17 is the self-review checklist.
4. Write commit messages in the Conventional Commits style described in
   [coding standards section 16](docs/engineering/coding-standards.md#16-git-and-commits).
5. Run `make check` before pushing.
6. If a change alters architecture or behavior, update the design doc in the
   same commit, and record significant decisions as a new ADR.
