# spine-next

This is Spine v2: a deterministic harness that supervises parallel coding agents in isolated git worktrees. A Kubernetes-style reconciler owns task state, and only deterministic gates (build, test, lint and AST tools exiting 0) can mark work complete.

The design is [RFC 0001](docs/rfcs/0001-spine-v2-go-reconciler.md).

## Quick start (MVP)

Requirements:

- Linux.
- Go 1.27.3 or newer, with a C compiler (for the tree-sitter gates).
- git 2.38 or newer.
- Claude Code's ACP adapter: `npm install -g @agentclientprotocol/claude-agent-acp`.
- Claude credentials: `ANTHROPIC_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN`, or a logged-in `~/.claude`.

```sh
go install github.com/patcarter883/spine-next/cmd/spine@main
cd your-repo
spine init                      # scaffolds .spine/config.yaml and .spine/gates.yaml
git add .spine && git commit -m "Add Spine config"   # gates are read from the base branch
spine doctor --agent            # checks git, store, gates and the agent's ACP handshake
spine run --scope 'src/**' "Add input validation to the signup handler"
```

`spine run` creates the task and runs the whole loop without further input:

1. It provisions an isolated git worktree outside your checkout.
2. It runs Claude Code over ACP inside that worktree.
3. It snapshots the result and runs your gates.
4. If a gate fails, it feeds the new failures back to the agent in a repair round. A run that crashes or stalls gets a repair round too, without the gates.
5. Once the gates pass, it squash-merges into `main` with a compare-and-swap. If `main` has moved, the exact merge result is gated first.

If no controller is running, `spine run` starts one inline and exits when the task finishes: exit 0 means integrated. If one is running (`spine controller`), it submits the task and follows its progress.

Other commands:

| Command | What it does |
|---|---|
| `spine list [--all]` | List tasks. |
| `spine status ID [--json]` | Show one task in detail. |
| `spine logs ID [-f]` | Show the task's event log; `-f` follows it until the task finishes. |
| `spine approve ID` | Integrate a task submitted with `--require-approval`. |
| `spine retry ID` | Give an escalated or failed task one more fresh attempt. |
| `spine cancel ID` | Stop the agent and release the worktree. |
| `spine ui` | A web dashboard on `http://127.0.0.1:8765`, loopback only because it has no auth. It shows tasks, gate results and timelines, and can approve, retry or cancel. Pages update live over SSE. It reads the database read-only; commands go through the same inbox as the CLI. |
| `spine controller` | Run the controller in the foreground. It reconciles every task and resumes after a crash or `kill -9`. |
| `spine recover` | Repair state after a crash. |
| `spine gc` | Checkpoint the SQLite WAL. |

What the controller guarantees:

- **Only gates can mark work done.** Gates come from the base branch, so an agent can't weaken them. Storage rejects any `GatePassed`, `Verified` or `Integrated` write from a component that doesn't own that condition.
- **The tree that lands is the tree that passed.** Integration is a compare-and-swap against the exact base tip the verdict was computed on.
- **Approval only holds work back.** It can delay a verified task, never pass a failing one.
- **Failures escalate rather than loop.** Budgets (attempts, rounds, wall clock, tokens) are enforced as a closed set of legal supervisor actions; when none is left, the task escalates to you with its worktree kept.
- **Your checkout is never touched mid-edit.** If `main` is checked out with uncommitted changes to tracked files, integration waits instead of overwriting them.

## Status: MVP (M0–M2 complete; M3 LLM supervisor and M4 dashboard implemented, not yet validated against live models)

### M3: LLM supervisor

The LLM supervisor is opt-in; the rule supervisor stays the default. To turn it on, add this to `.spine/config.yaml`:

```yaml
supervisor:
  type: llm             # default: rules
  model: claude-opus-5-5
  effort: medium        # low|medium|high|xhigh|max
  timeout: 2m
```

Credentials come from the environment, the same way the Anthropic SDK finds them: `ANTHROPIC_API_KEY`, `ant auth login`, and so on. How it works:

1. When a round fails, Spine sends the model a typed observation: the budget, round history, new failures only, and the rule supervisor's suggestion.
2. The model must answer with a JSON object matching a schema (structured output). The schema's `action` enum lists only the currently legal actions.
3. The reconciler re-checks legality. If the model call errors, the model refuses, or the output is invalid or illegal, the rule supervisor decides instead.
4. Every decision event records which supervisor decided, plus the fallback reason if there was one.

The model never writes status, and it cannot choose Verify or Merge.

Tests run against a fake Messages API (`internal/reconciler/llm_test.go`, `internal/e2e/llm_test.go`). The M3 exit criterion, an A/B comparison with the rule supervisor on the corpus (pass rate at least equal, fewer tokens per task), needs live model runs and has not been done.


### M2: controller, ACP agent runner, CLI

| Package | What it does |
|---|---|
| `internal/acp` | An Agent Client Protocol v1 client: JSON-RPC 2.0 over newline-delimited stdio. Methods and shapes come from the `@agentclientprotocol/sdk` 2.x `schema/v1`. |
| `internal/agent` | The ACP runner. Spine advertises no `fs` or `terminal` capabilities, so the agent edits the worktree itself; see the policy list below. It also renders the typed prompt. `fakeacp` is a scripted ACP agent for tests. |
| `internal/reconciler` | The level-triggered controller (RFC §4): a per-key workqueue, `Reconcile`, asynchronous agent and gate operations, `RuleSupervisor`, and integration that gates the merge result. In-flight operations are persisted, so a restarted controller turns orphans into crashed outcomes. Commands are staged and applied inside the task's own reconcile. |
| `internal/storage` | Adds tasks with resource-version CAS, condition-ownership enforcement, an append-only event log, and read-only opens for the CLI. |
| `internal/inbox` | Atomic command files from the CLI to the controller. The controller is the database's only writer. |
| `internal/config`, `internal/app`, `cmd/spine` | Operator config, component wiring, and the CLI. |
| `internal/e2e` | End-to-end tests of the whole controller against real repositories; see the scenario list below. |

The ACP runner's policies:

- **Permissions:** tool calls are allowed only when their locations are inside the worktree.
- **Interruptions:** on a timeout, stall or exceeded token budget, the runner sends `session/cancel`.
- **Process group:** reaped before `Run` returns.

The end-to-end scenarios:

- **M2 exit criterion:** 8 parallel tasks, integrated through CAS with a clean primary checkout and a `main` that passes its own tests. They include a feedback repair round, an agent crash, an agent that commits on its own, and two tasks that conflict.
- **Approval and moved base:** an approval hold, followed by a merge-result gate failure on a moved base and a fresh attempt.
- **Escalation and retry:** no-progress escalation, then retry.
- **Restart:** the controller is restarted mid-run.
- **Cancel:** stops the agent and releases the worktree.

Verified by hand with the built binary:

- `spine run` against a scratch repository.
- `kill -9` of a running `spine controller` mid-agent-run. The agent died with it via Pdeathsig, and a new controller recorded the crash, ran a repair round, gated and integrated.

**Verified live with the real adapter** (`@agentclientprotocol/claude-agent-acp` 0.99.1, Claude Code over ACP v1):

- `spine doctor --agent` performed the real handshake. It reported ACP v1 with modes `default, acceptEdits, plan, auto`.
- `spine run --scope '*.go' "Add a function Goodbye ... with a table-driven test"` ran on a scratch Go repository with gates parse, scope, nonempty, build, unit (baseline) and api.
  - Claude Code edited the worktree and ran `go build`/`go test` under a permission Spine allowed because it stayed inside the worktree. It ended its turn after 56k tokens.
  - Every gate passed, and the change landed on `main` through CAS.
  - The squashed commit adds `Goodbye` and a passing table-driven test.
  - The lease worktree was removed.

The M3 A/B comparison of the LLM and rule supervisors on the corpus has not been run yet.

### Deviations from RFC 0001, decided while building M2

- **A merge conflict, or a failure that only shows up on the merged tree, gets a fresh attempt from the new base.** The RFC's in-place rebase within the patch budget is deferred. Feedback carries the conflicting paths.
- **Each round starts a new agent process with a complete typed prompt**, rather than resuming the ACP session. The worktree keeps the previous round's changes.
- **The CLI never writes the database.** It drops atomic command files in `<git-common-dir>/spine/inbox`; the controller applies them idempotently and only then deletes them.
- **M0 recovery now kills only orphaned git processes in the primary checkout.** M0 killed every orphaned process whose cwd was in the repository, which included an operator's daemonized dev server. Under Spine's worktree root, every orphan is still killed. `TestRecoverSparesOperatorDaemons` covers this.
- **The mid-run token budget uses ACP `usage_update.used` (context tokens) as a lower bound.** The exact spend comes from the adapter's `PromptResponse.usage`.

## Earlier milestones

### M0: isolation and crash recovery

| Package | What it does |
|---|---|
| `internal/exec` | The only `os/exec` wrapper. Every child runs as a process-group leader; `Run` never returns while anything in the group is alive. Output is capped and the environment is allow-listed. |
| `internal/storage` | SQLite (`modernc.org/sqlite`, pure Go). One writer process enforced by an exclusive flock, `BEGIN IMMEDIATE` writes, WAL verified at open, `synchronous=FULL` (RFC §7.1). Holds leases, process groups, operation intents and snapshots. |
| `internal/worktree` | The WorktreeManager (RFC §3.2): `Create`, `Snapshot` (temp index, never touches the worktree index), `Rollback`, `Preflight` (`merge-tree --write-tree`), `Merge` (`update-ref` compare-and-swap), `Teardown` (fail-closed, snapshots before removal), `Recover` and `Check`. |
| `internal/chaos` | Crash harness. It SIGKILLs a child running random worktree operations, then recovers and checks every invariant. |
| `internal/doctor`, `cmd/spine` | `spine doctor`, `spine recover`, `spine gc` |
| `third_party/gascity` | Vendored Gas City worktree, git and process primitives ([UPSTREAM.md](third_party/gascity/UPSTREAM.md)) |

**M0 exit criterion met:** 1000 kill/recover iterations with seed 20261102 passed in 559 s, with zero invariant violations.

- **Acknowledged:** 733 snapshots and 264 merges, none lost.
- **Recovery actions:** 374 process groups reaped, 260 requested leases discarded, 261 leases torn down, 26 stale locks removed, 7 interrupted merges completed, 3 interrupted snapshots recovered and 2 dirty-checkout rescues.

### M1: tool registry and gate engine

| Package | What it does |
|---|---|
| `internal/tools` | The ToolRegistry (RFC §3.3). It validates args against each tool's JSON Schema, canonicalizes them, and keys the cache on content: tree OID, plus merge-base and base trees for base-dependent tools, plus an environment key for hermetic tools. It also bounds concurrency, enforces timeouts and caps results. Results are cached in memory or in SQLite (`tool_cache`). |
| `internal/tools/diff` | `diff.stats`, `diff.nonempty`, `diff.scope` (`**` globs, deny list, both sides of a rename), `diff.applycheck` (temporary index) and `git.preflight` (`merge-tree --write-tree`). |
| `internal/tools/build` | `cmd.run` with the `go-build`, `gotest-json`, `pytest-junit`, `eslint-json`, `tsc`, `ruff-json`, `php-lint` and `regex` parsers, plus `go.build`, `go.vet` and `go.test`. See the `cmd.run` behaviour list below. |
| `internal/tools/ast` | `ts.parse`, `ts.symbols` and `ts.api_contract`. Tree-sitter grammars cover Go, Python, TypeScript/TSX/JS and PHP; queries live in `queries/*.scm`. Go symbols come from `go/parser`. The import check covers Go, Python and TypeScript. |
| `internal/gate` | The gate engine (RFC §5.4). Gates come from `.spine/gates.yaml` **at the base commit** and run in order, stopping at the first failure. Trees are materialized only on a cache miss. Verdicts are baseline-relative; see the rules below. |
| `internal/corpus` | The M1 exit criterion: 31 tasks with ground-truth verdicts over Go, Python, TypeScript and PHP fixtures. The fixtures carry real debt (failing tests, lint and type errors) and run the real toolchains. It also checks that the same tree under a different commit, in a second worktree, is served entirely from the cache. |

`cmd.run` behaviour:

- **Environment:** allow-listed variables only, with no `GIT_*`. Each run gets its own `HOME` and `TMPDIR`, and compiler caches point back at the user's.
- **Timeout:** reported as an uncached `Timeout` verdict.
- **Death by signal:** treated as an infrastructure error, not a verdict.
- **Full logs:** stored in the artifact store by digest.

Baseline-relative verdict rules:

- New error fingerprints fail the gate.
- Errors that already fail on base are excused.
- A failure whose output can't be parsed, or was truncated, is never excused.
- `Fixed` reports debt the change paid down.

**M1 exit criterion met:** all 31 corpus verdicts match ground truth, and identical trees are served from the cache with zero tool executions. A negative control (flipping one expectation) fails the run as intended.

### Deviations from RFC 0001, decided while building M1

- **Gates are read from the base commit, never from the snapshot.** A worker cannot weaken the gates that judge its own change.
- **Base-dependent cache keys use trees** (snapshot, merge base, base) instead of commit OIDs. Identical content then shares results across commits and worktrees. The gate engine passes them as `Invocation.BaseKey`.
- **`diff.nonempty` reports only `NoChange`.** No commits beyond the merge base implies an identical tree.
- **`tools.Result` gained three fields:**
  - `Data`: a typed payload, such as the `ts.symbols` table.
  - `Unparsed`: the tool failed without producing error diagnostics.
  - `NoCache`: for example, timeouts.

  `Cache` methods take a context and return errors. `GatePolicy` is a string (`absolute` or `baseline`) so YAML stays readable.
- **Tools run against materialized trees, not the lease worktree,** so a cached result really is a function of the tree. Ignored dependency directories are symlinked in from the worktree via `baseline.link` in `gates.yaml`, and are assumed to follow the lockfiles.
- **By default the base side of a baseline gate runs only when the snapshot has failures to excuse.** It is cached per base tree for every task, and `Engine.AlwaysBaseline` makes it always run so `Fixed` is always filled.
- **Go API diffs are syntactic** (`go/parser`), not type-checked with `go/types`. They ignore parameter names, unexported fields and constant values.
- **Import checks are conservative.** Third-party Python imports are not checked, because import names don't map to distribution names, and PHP imports are not checked at all, because autoloading is configured per project.
- **Without CGO, the tree-sitter languages return `ErrUnavailable`,** an infrastructure error and never a verdict. Go files still parse through `go/parser`.

## Build and test

Requires Go ≥ 1.27.3, git ≥ 2.38 and a C compiler: tree-sitter needs CGO from M1 on (RFC §8.1). Linux is the supported platform. The corpus also uses `ruff`, `pytest`, `tsc`, `eslint` and `php`. It skips any language whose tools are missing, unless `SPINE_CORPUS_REQUIRE=1`, which CI sets.

```sh
CGO_ENABLED=0 go build ./cmd/spine
go test ./...                                    # includes a 40-iteration chaos run and the M1 corpus
CGO_ENABLED=0 go build ./cmd/spine                # pure-Go build: tree-sitter tools report ErrUnavailable
SPINE_CHAOS_ITERS=1000 go test -run 'TestChaos$' -timeout 30m ./internal/chaos   # M0 exit criterion
```
