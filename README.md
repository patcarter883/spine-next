# spine-next

This is Spine v2: a deterministic harness that supervises parallel coding agents in isolated git worktrees. A Kubernetes-style reconciler owns task state, and only deterministic gates (build, test, lint and AST tools exiting 0) can mark work complete.

The design is [RFC 0001](docs/rfcs/0001-spine-v2-go-reconciler.md).

## Status: M1 (deterministic tools and gates)

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
