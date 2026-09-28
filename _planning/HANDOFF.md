# Switchyard: Session Handoff

Read this file first at the start of every session, then follow the start
checklist. Update it at the end of every session (or whenever a meaningful
chunk of work lands) so the next session starts with full context and nothing
is lost.

---

## Start-of-session checklist

1. Read this file top to bottom.
2. Read the **Status** block in [PLAN.md](PLAN.md) and the current phase's
   checklist.
3. Skim [CODING_STANDARDS.md](CODING_STANDARDS.md) sections 2 and 16 (content
   rules and commit rules). Read the rest when writing code in an unfamiliar
   area.
4. Read the [DESIGN.md](DESIGN.md) sections relevant to the next action.
5. Run `git status` and `git log --oneline -10` to confirm the repo matches the
   state described below.
6. If the environment facts below look stale (tool versions, WSL state),
   re-check them before relying on them.

## End-of-session checklist

1. Update **Current state**, **Next actions**, and **Session log** below.
2. Update the PLAN.md Status block and tick finished items.
3. Add any decision to the DESIGN.md decision log.
4. Commit docs with the work (or as a `docs(planning): ...` commit).

---

## Current state

- **Phase:** Planning complete. Phase 0 (environment and scaffold) not started.
- **Repo:** `D:\Code\switchyard`, git initialized on `main`. Contains only
  `_planning/`. No Go module yet. No GitHub remote yet.
- **What exists:** DESIGN.md (architecture, cost model, benchmark method,
  references), PLAN.md (phases 0 to 9), CODING_STANDARDS.md, this file.

## Next actions (in order)

1. **Owner:** upgrade Go to 1.27.x (`winget upgrade GoLang.Go`), then confirm
   `go version`.
2. **Owner:** install WSL2 with Ubuntu 24.04 from an administrator PowerShell:
   `wsl --install -d Ubuntu-24.04`, then reboot.
3. Install golangci-lint v2; confirm it runs.
4. Phase 0 vLLM spike: four replicas on the RTX 4090 with capped KV caches;
   record flags and versions in `deploy/vllm/README.md`.
5. Scaffold the module and CI (Phase 0, Repository section).
6. Owner decisions still open: license (MIT or Apache-2.0); when to create the
   public GitHub repo.

## Environment facts (verified 2026-09-28)

| Item | Value |
|---|---|
| OS | Windows 11 Home (10.0.26200) |
| GPU | NVIDIA GeForce RTX 4090, 24 GB, driver 610.74 |
| Go | 1.23.5 installed; **target 1.27.x** (latest stable 1.27.1, 2026-09-01) |
| Python | 3.12.10 (Windows) |
| WSL | **Not installed** (required for vLLM) |
| Docker | Not installed (not required; vLLM runs in a WSL venv) |
| Git identity | `shusingh <ksingh.shubh@gmail.com>` (global) |
| Shells | Git Bash and PowerShell 7 available |

## Working rules and gotchas

- **Commits:** author is the repo owner only. No co-author trailers or tool
  attributions. The local `commit-msg` hook in `.git/hooks/` enforces this; if
  the repo is re-cloned, reinstall the hook (see CODING_STANDARDS section 16).
- **No code-generation tool references** anywhere in code, docs, or commits
  (CODING_STANDARDS section 2). Domain terms like LLM, model, inference are fine.
- **Honest results:** results are benchmarks against named baselines under
  stated conditions. Never phrase them as improving a pre-existing system.
  Simulator numbers are always labeled as simulated.
- **Shell working directory:** tool shells may reset to another directory
  between commands. Use absolute paths or `git -C D:/Code/switchyard ...`.
- **Windows and Linux split:** the router, simulator, and load generator build
  and run natively on Windows. vLLM runs only inside WSL2. WSL2 forwards
  `localhost` ports, so the router on Windows can reach replicas on
  `localhost:8001` to `8004`.

## Key decisions so far

See DESIGN.md section 15 for the full log. Summary:

- Go, stdlib first, single binary (D1, D6).
- Approximate byte-block prefix hashing first; precise KV-event mode is a
  stretch goal gated on a hash-compatibility spike (D2).
- Route on estimated TTFT in seconds with an imbalance guard (D3).
- Simulator plus open-loop load generator alongside real GPU benchmarks
  (D4, D5).

## Open questions

- Final model and replica count for the GPU benchmark (decide in the Phase 0
  spike).
- License choice.
- How to reset vLLM prefix caches between benchmark trials (endpoint or
  restart).

## Session log

| Date | Summary |
|---|---|
| 2026-09-28 | Researched prior art (SGLang gateway, llm-d, GAIE, Dynamo, vLLM production-stack, Preble), vLLM prefix caching internals, KV events, metrics, and public traces (Mooncake, agentic traces). Named the project Switchyard. Wrote DESIGN, PLAN, CODING_STANDARDS, and this handoff. Initialized the repo with a commit-msg hook. |
