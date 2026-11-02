# spine-next

This is Spine v2: a deterministic harness that supervises parallel coding agents in isolated git worktrees. A Kubernetes-style reconciler owns task state, and only deterministic gates (build, test, lint and AST tools exiting 0) can mark work complete.

The design is [RFC 0001](docs/rfcs/0001-spine-v2-go-reconciler.md).

## Status: M0 (isolation and crash recovery)

| Package | What it does |
|---|---|
| `internal/exec` | The only `os/exec` wrapper. Every child runs as a process-group leader; `Run` never returns while anything in the group is alive. Output is capped and the environment is allow-listed. |
| `internal/storage` | SQLite (`modernc.org/sqlite`, pure Go). One writer process enforced by an exclusive flock, `BEGIN IMMEDIATE` writes, WAL verified at open, `synchronous=FULL` (RFC §7.1). Holds leases, process groups, operation intents and snapshots. |
| `internal/worktree` | The WorktreeManager (RFC §3.2): `Create`, `Snapshot` (temp index, never touches the worktree index), `Rollback`, `Preflight` (`merge-tree --write-tree`), `Merge` (`update-ref` compare-and-swap), `Teardown` (fail-closed, snapshots before removal), `Recover` and `Check`. |
| `internal/chaos` | Crash harness. It SIGKILLs a child running random worktree operations, then recovers and checks every invariant. |
| `internal/doctor`, `cmd/spine` | `spine doctor`, `spine recover`, `spine gc` |
| `third_party/gascity` | Vendored Gas City worktree, git and process primitives ([UPSTREAM.md](third_party/gascity/UPSTREAM.md)) |

## Build and test

Requires Go ≥ 1.27.3 and git ≥ 2.38. Linux is the supported platform.

```sh
CGO_ENABLED=0 go build ./cmd/spine
go test ./...                                    # includes a 40-iteration chaos run
SPINE_CHAOS_ITERS=1000 go test -run 'TestChaos$' -timeout 90m ./internal/chaos   # M0 exit criterion
```
