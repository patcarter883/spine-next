# Vendored from Gas City

| | |
|---|---|
| Upstream | https://github.com/gastownhall/gascity |
| Commit | `4ebacb166847160421b3f08d2b0e447a699eb632` (2026-11-02) |
| License | MIT, see [LICENSE](LICENSE) (Copyright (c) 2026 Gas City Contributors) |
| Policy | One-time fork (RFC 0001 §9.4). Upstream is diffed once per Spine release, and fixes are cherry-picked deliberately. It is not a tracked dependency. |

## Packages

| Package | Upstream path | Files |
|---|---|---|
| `worktree` | `internal/worktree` | all |
| `git` | `internal/git` | `git.go`, `worktree_ops.go`, `revision.go` and their tests. `clone*.go` and `sshkeepalive*.go` are omitted, which removes the `gitcred`/`gchome`/`remotesource` dependency chain. |
| `processgroup` | `internal/processgroup` | all, including `processgrouptest` |
| `execgrace` | `internal/execgrace` | all |
| `pidutil` | `internal/pidutil` | all |
| `pathutil` | `internal/pathutil` | all |
| `testutil` | `internal/testutil` | **rewritten**: only `CanonicalPath`, `InitGitRepo` and `RunGit`, the subset the vendored tests use |

## Mechanical changes

- Import paths were rewritten from `github.com/gastownhall/gascity/internal/` to `github.com/patcarter883/spine-next/third_party/gascity/`.
- `testenv_import_test.go` files were deleted. They only blank-import Gas City's test environment package.

## Patches

1. **`git`: kill the process group on cancel** (`git.go` `runCtx`, `rungroup_unix.go`, `rungroup_windows.go`). Upstream kills only the `git` process when a call's context ends (`git.go:529`, with `WaitDelay`), so a hook, credential helper or `ssh` child can outlive it. The patch starts git as a process-group leader, SIGKILLs the group on cancel (RFC 0001 §5.3), and on Linux sets `Pdeathsig=SIGKILL` so git dies with a killed supervisor (`pdeathsig_linux.go`). The chaos harness found the need for the last part: an orphaned `git worktree add` kept writing while recovery ran.

The upstream test suites of all vendored packages pass unmodified on the vendored copy: `go test ./third_party/...`.
