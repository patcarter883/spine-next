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
5. Once the gates pass, it squash-merges into `main` with a compare-and-swap. If `main` has moved, the exact merge result is gated first. Verified tasks merge into a branch one at a time, oldest first, so each merge result is checked once against the tip it lands on instead of being re-checked every time another task lands.

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
| `spine ui` | The web dashboard on `http://127.0.0.1:8765` (see [Dashboard](#dashboard)). |
| `spine hint ID "text"` | Give the next repair round a hint. An escalated task resumes in its kept worktree. |
| `spine accept ID` | Do what the LLM supervisor proposed when it escalated the task (see [M3](#m3-llm-supervisor)). |
| `spine rollback ID SNAPSHOT` | Continue a stopped task from one of its snapshots. |
| `spine plan "goal"` | A planner agent proposes a task breakdown for review; `plan validate FILE` and `plan submit FILE` check and submit a plan. |
| `spine gates check [REV]` | Dry-run the gates on `main` (or REV) with no agent: which gates a task can pass. |
| `spine survey [focus]` | Refresh the repository context (module map labels, `.spine/context.md`) through a task. |
| `spine modules [--write]` | Print (or write) the derived module map. |
| `spine pause [reason]` / `spine resume` | Stop or allow starting new tasks in this repository; running tasks finish. |
| `spine users add NAME --role R` | Add a dashboard user (viewer, operator or admin); prints a token once. Also `users list` and `users remove`. |
| `spine controller` | Run the controller in the foreground. It reconciles every task and resumes after a crash or `kill -9`. |
| `spine recover` | Repair state after a crash. |
| `spine gc [--days N] [--dry-run]` | Checkpoint the SQLite WAL; with `--days`, also remove snapshot refs of tasks finished more than N days ago, stray worktrees and old tool-cache entries. |

What the controller guarantees:

- **Only gates can mark work done.** Gates come from the base branch, so an agent can't weaken them. Storage rejects any `GatePassed`, `Verified` or `Integrated` write from a component that doesn't own that condition.
- **The tree that lands is the tree that passed.** Integration is a compare-and-swap against the exact base tip the verdict was computed on.
- **Approval only holds work back.** It can delay a verified task, never pass a failing one. You approve after the merge result passed its gates, and approval covers the change you reviewed: a later round or attempt asks again.
- **Failures escalate rather than loop.** Budgets (attempts, rounds, wall clock, tokens) are enforced as a closed set of legal supervisor actions; when none is left, the task escalates to you with its worktree kept.
- **Your checkout is never touched mid-edit.** If `main` is checked out with uncommitted changes to tracked files, integration waits instead of overwriting them.

## Local models with Pi

Spine can run the [Pi coding agent](https://www.npmjs.com/package/@earendil-works/pi-coding-agent) (`@earendil-works/pi-coding-agent`) as the worker through Pi's RPC mode. Pi works with local OpenAI-compatible servers such as vLLM, SGLang, llama.cpp and Ollama.

The dashboard can do this setup for you: **Settings → Pi agent** builds the image or installs Pi with npm, writes `models.json` and edits the adapter. By hand, add an adapter to `.spine/config.yaml`:

```yaml
agent: pi
adapters:
  pi:
    protocol: pi-rpc
    version: 1.5.0              # required; Spine refuses any other version
    model: vllm/qwen3-coder-30b # provider/id from Pi's models.json
    tool_timeout: 10m           # default 10m
    max_turns: 80               # default 80
    container:
      image: spine-pi:1.5.0     # docker build -t spine-pi:1.5.0 contrib/pi
      network: host             # so the container reaches a model server on the host
      args: [--memory, 8g, --cpus, "4"]
```

Point Pi at the model server in `~/.pi/agent/models.json` (or the directory in `PI_CODING_AGENT_DIR`):

```json
{"providers": {"vllm": {"baseUrl": "http://127.0.0.1:8000/v1", "api": "openai-completions", "apiKey": "unused",
  "models": [{"id": "qwen3-coder-30b", "contextWindow": 65536, "maxTokens": 8192}]}}}
```

The image has no language toolchains, so an agent can't run a project's builds or tests itself, and may go looking for them. For Go repositories, `contrib/pi/Dockerfile.golang` adds Go and gcc on top of it.

Then run `spine doctor --agent`. It checks the pinned version and that Pi starts in RPC mode and resolves the model, without sending a prompt.

Pi has no sandbox, never asks for approval, has no default timeout for shell commands and no turn limit, and a failed run can still exit 0. Spine adds each guardrail from outside the process:

| Risk | Guardrail |
|---|---|
| No sandbox or approvals | Each run gets a fresh container: `--rm`, your uid, `HOME` in the container, only the task worktree and its private git dir writable, the shared git dir read-only. Agent environment goes through a 0600 env file, not the command line. The container is force-removed after the run, and a restarted controller removes any its predecessor left behind (labelled per repository). `spine doctor` warns when a Pi adapter has no container. |
| Host credentials | Containerized runs get a private copy of `models.json` and `settings.json` only, never `auth.json`, sessions or extensions. Pass a local server's key as an environment variable referenced in `models.json`. |
| Planted extensions | Pi starts with `--no-session --no-approve --no-extensions --no-skills --no-prompt-templates --no-themes --no-mcp`, so files a worker writes into the repository (`.pi/…`) are never loaded by a later round. |
| Shell commands with no timeout | A tool call running past `tool_timeout` is aborted and the same session is told to continue without it. After the third such timeout the run ends. |
| No turn limit | Over `max_turns` assistant turns, the run is aborted with `BudgetExceeded`. |
| Runaway spend | Tokens and cost come from Pi's own accounting, both per message and from `get_session_stats` at the end. Exceeding the task's token budget aborts the run. |
| Hangs | No protocol activity for `stall_timeout` (default 15m) aborts the run. At 80% of the run timeout Pi is steered to wrap up, and at 100% the run is aborted. If an abort isn't honoured within 20 s, the process group is killed. |
| "Exit 0" failures | The outcome comes from the final assistant message's `stopReason` and `agent_settled`, never the exit code. `error` (including exhausted provider retries) counts as a crash, and `length` as budget exceeded. |
| Breaking minor releases | `version` is required and checked against `pi --version`, inside the container when one is configured. A different binary fails fast with "found X, but the adapter pins Y". |

How this was tested:

- **Fake Pi:** a scripted Pi (`internal/agent/fakepi`) covers every guardrail, plus the controller loop with a feedback round.
- **Real Pi:** Pi 1.5.0 itself ran against an OpenAI-compatible fake model server (`internal/agent/pireal_test.go`, enabled by `SPINE_PI_BIN` / `SPINE_PI_IMAGE`):
  - It wrote files and completed.
  - A `sleep 600` was aborted by the tool timeout and the session finished the task.
  - In a container, files were owned by the user, a write to `../escape.txt` never reached the host, and orphaned containers were reaped per repository.
- **CI:** CI runs the real-Pi tests on every push.
- **Not tested:** a real local model (vLLM etc.) hasn't been run; the model server in tests is a scripted stand-in.

## Several repositories: `spine supervisor`

One process can run Spine for every repository you register. Storage stays per repository: each keeps its own database, writer lock, inbox, worktrees and crash recovery in its own `.git`, exactly as with `spine controller`.

```sh
spine repos add ~/src/lib            # name defaults to the directory name
spine repos add ~/src/app --name app
spine supervisor                     # runs every registered repository; dashboard on http://127.0.0.1:8765
spine supervisor status              # per-repository state, restarts, limits in use
spine supervisor install             # writes a systemd user unit that starts it on login
```

What the supervisor adds over separate controllers:

- **One dashboard** with a repository switcher and an "All repositories" view. Broken repositories show in a banner with their error and restart count.
- **Supervisor-wide limits:** concurrent agent runs and gate evaluations are capped across all repositories, so 5 repos don't mean 5 × `parallel` agents. A task waiting for a slot logs "waiting for a supervisor-wide agent slot". Configure the caps in `~/.config/spine/supervisor.yaml` (`addr`, `max_agents`, default 4, `max_gates`, default 2).
- **Cross-repository dependencies:** `spine run --after lib:bump-v2 "use lib v2"` waits until task `bump-v2` in the `lib` repository has integrated. A task waits up to 10 minutes for a dependency that hasn't been submitted yet, local or cross-repository, before escalating.
- **Live registry:** `spine repos add` or `spine repos remove` take effect within seconds, without a restart. Removing a repository stops its controller but leaves its tasks, database and worktrees untouched.

How it isolates failures:

- **Panics:** every goroutine a controller starts recovers panics. A panic stops that repository's controller only. The supervisor reopens the repository from disk, which runs crash recovery like a fresh `spine controller`, and restarts it with backoff (1 s, doubling up to 5 min, reset after 5 minutes of healthy running).
- **Broken repositories:** a repository that can't start, for example because a standalone `spine controller` already holds its lock, is marked broken and retried. The others keep running.
- **One supervisor per user**, enforced by a lock in `~/.config/spine`.

Every other command works the same under the supervisor. `spine run`, `status`, `logs`, `approve`, `retry` and `cancel` inside a registered repository talk to it through that repository's inbox and read-only database.

`TestSupervisorTwoReposLimitAndCrossRepoDep` and `TestSupervisorPanicIsolationAndBrokenRepo` in `internal/e2e` cover the limits, cross-repository ordering, panic restart and broken-repository recovery.

## Dashboard

`spine ui` (one repository) and `spine supervisor` (all of them) serve the same dashboard. It reads each database read-only and sends every change through the repository's inbox, like the CLI.

**Reviewing and steering a task**
- **Changes:** the diff of each snapshot against its base, of one round against another, and of the merge result. A task held for approval opens on exactly what would land, with **Approve and integrate** above it.
- **Rounds:** per repair round, the feedback the agent received, how its run ended (tokens, cost, turns, tool calls, final message), each gate's result, the failures it fixed, introduced or left, and the supervisor's decision.
- **Logs:** full gate output (`go test`, `tsc`, …) from the artifact store, and the agent's final messages.
- **Live:** tool calls and agent messages as they are written.
- **Hints, edits and rollbacks:** a hint goes to the next round, and resumes an escalated task in its kept worktree. A pending task can be edited; a stopped one edited and retried, as a new spec generation. **Continue from #N** restarts from any snapshot: in place, or as a fresh attempt that replays that snapshot's diff onto the current base.

**Planning**
- **Plan:** enter a goal and a planner agent (a task of kind `plan`) writes a breakdown. `spec.validate` sends a broken plan back to it. You then edit tasks, scopes and dependencies, see validation as you type, and submit. Validation checks ids, intents, scope globs, agents, gates, dependencies (including `repo:task`), cycles, and parallel tasks claiming the same paths.
- **Several plans at once:** the planner is shown the repository's unfinished tasks and the tasks other plans propose that are still awaiting review, with their scopes. It may list them in `after` instead of redoing or racing them. Spine then links what the planner missed: a task whose scope can change the same paths as an earlier task in the plan, an unfinished task, or a task an older plan proposes gets an `after` link to it, listed in the plan's notes. Older work never waits for newer, so linked plans cannot form a cycle; **Link overlapping tasks** in the plan review applies the same rule after edits. A dependency on a task that is only proposed is an error at submit, so submit plans oldest first.
- **Graph:** tasks and `after` links across repositories, with each task's state (running, ready, blocked, needs you, failed, done) and the critical path.
- **Templates:** saved task shapes, from `.spine/templates.yaml` (shared through git) or saved from the form.
- **Gates:** the new-task form shows the base's `.spine/gates.yaml`, and you can pin a subset for one task.
- **Agent picker:** lists every adapter, including the built-in **Pi** (`pi-rpc`, version-pinned) with its model and container, and a warning when it would run without one.

**Monitoring**
- **Dashboard:** in flight, waiting on you, merged and failed today, throughput per day, and wait times.
- **Costs:** tokens and cost per day, repository and task, against budgets.
- **Quality:** first-round pass rate, rounds and attempts per task, escalation and failure reasons, top failing gates, and rules vs LLM supervisor decisions (how often the next round improved).
- **Supervisor:** global agent and gate slots with holders and the queue (limits can be changed live), and per-repository worktree slots, ready tasks, health and restarts.
- **Health:** `spine doctor` (optionally with the agent handshake), disk, database and WAL size, stuck leases and stray worktrees, and garbage collection.
- **History:** search by text, phase, author and date; commit links when the remote is GitHub, GitLab, Bitbucket or Codeberg.
- **Notifications:** approvals, escalations, failures, broken repositories, ready plans and budget thresholds, delivered to webhooks (Slack-compatible or JSON), email (SMTP), the desktop, and the browser. Configure them under **Settings** (`~/.config/spine/notify.yaml`).

**Operations**
- **Settings:** add and remove repositories; edit `.spine/config.yaml` through a form or as YAML (validated, comments kept; the controller restarts under the supervisor); change supervisor limits.
- **Pi agent:** under **Settings** (admins), build the worker image or run the npm install at the adapter's pinned version with the output shown live; add model servers to `models.json` (list a server's models to test it, and other fields in the file are kept); set the adapter's version, model, container and limits; and run the same probe as `spine doctor --agent`. Commands are fixed: a request only picks the method. API keys are never sent back to the browser, and a `!command` key can only be set by editing the file. An edit to a tracked `.spine/config.yaml` must be committed before tasks can integrate into a checked-out base branch; the dashboard says so when it applies.
- **Bulk actions:** approve, retry, cancel or hint many tasks at once.
- **Pause and resume:** pause or resume a repository.

**Access**
- **Users:** without users, the dashboard serves loopback only and records changes as `<you> (local)`. With users (`spine users add NAME --role viewer|operator|admin`), every request needs a token, either as `Authorization: Bearer` or through the sign-in page. Viewers read, operators act on tasks, and admins manage repositories and settings. Commands carry the user's name.
- **Audit:** every change is appended to `~/.config/spine/audit.log` (shown under **Audit**).
- **Remote and read-only:** a non-loopback `--addr` is refused unless users exist. `--tls-cert/--tls-key` serve HTTPS, and `--read-only` rejects every change whoever asks. `spine supervisor status` reads `SPINE_TOKEN`.

`internal/e2e/ui_test.go` drives these features through a real controller and a scripted agent. It covers diffs, rounds, logs, hints, rollback, the live stream, pausing, editing, bulk actions, GC and a planner run. `internal/ui/auth_test.go` covers roles, sessions, read-only mode and the audit log.

## Repository context

Agents work better with a few facts about the repository: how to build and test it, where things live, and which parts change together. Spine derives most of this and keeps the rest short and reviewed. It does not inject long reference docs into prompts.

**Onboarding**
1. **Gates check.** `spine gates check [REV] [--file .spine/gates.yaml]` dry-runs the base's gates (or a draft) on `main` or REV, with no agent.
   - It shows which gates pass, which have pre-existing failures that a baseline policy excuses, and which only judge a task's diff.
   - It also flags which gates fail outright, so that no task could ever pass them. This is the prerequisite: fix those, or switch them to `policy: baseline`.
   - The dashboard's **Health** page has the same check.
2. **Survey.** `spine survey [focus]` (or **Run survey** on the Health page) submits a survey task:
   - Spine derives `.spine/modules.yaml` in the task's worktree.
   - An agent adds short `label`s and `caveats` and writes `.spine/context.md`.
   - The `scope`, `nonempty` and `spine.context` gates check the result, and it waits for your approval like any change.
   - `spine.context` rejects edits to derived fields and a `context.md` over its token cap.

**`.spine/modules.yaml`** is derived, not written. `spine modules` prints it; `--write` puts it in your checkout.
- **Modules:** boundaries come from `go.mod`, `package.json`, `pyproject.toml` and the source layout. Imports between modules come from go/parser for Go and tree-sitter for TypeScript, JavaScript and Python.
- **Tests:** each module's own tests, plus the other tests that import it.
- **Co-change:** modules that changed together in the last 1,000 commits.

Like `gates.yaml`, it is read from the base branch only. It is used in three places:
- **Planner prompts:** planners get a capped rendering of the map.
- **`spec.validate`:** two tasks that may run in parallel are flagged when they change the same module, or modules that usually change together. A scope that changes a module but none of its tests is flagged too.
- **Scope suggestions:** the new-task form and the plan editor suggest scopes from the intent.

**`.spine/context.md`** is a factual summary: build and test commands, layout, conventions and no-go areas.
- **Cap:** `context.notes_tokens` (default 1,200 tokens) is a hard limit.
- **Who gets it:** adapters with `context: true` (the default for `pi-rpc`) get it in every prompt; Claude (ACP) is off by default (`adapters.claude.context: true` turns it on).
- **Staleness:** `spine doctor` and the Health page warn when the map is more than `context.stale_after` commits behind the base (default 200, counting only commits outside `.spine/`).

**Measuring before it becomes a default**
- **What's recorded:** each agent run records what context it received.
- **Control arm:** `context.experiment: 0.3` runs a stable 30 % of tasks without context.
- **Comparison:** the **Quality** view compares the two groups on plan-rejected rounds per planner task, the first-round pass rate of local-model (Pi) tasks, and tasks hitting merge conflicts.

```yaml
# .spine/config.yaml
context:
  notes_tokens: 1200
  modules_tokens: 1500
  stale_after: 200
  experiment: 0        # fraction of tasks run without context, for measurement
adapters:
  claude: {context: true}   # opt Claude in
```

`TestRepositoryContext` in `internal/e2e` covers the whole flow. A survey whose agent edits a derived field is sent back by the gate, then fixed and approved. Afterwards the worker prompt carries `context.md`, the planner prompt carries the labelled map, and the dashboard's gates check, context status, suggestions and module-aware validation all see it.

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

1. When a round fails, Spine sends the model a typed observation: the budget, round history, new failures only (each marked as in a file the change touched or not), the end of an undiagnosed gate's output, the files the attempt changed, whether the last round changed anything, its earlier decisions and people's hints on this attempt, and the rule supervisor's suggestion.
2. The model must answer with a JSON object matching a schema (structured output). The schema's `action` enum lists only the currently legal actions.
3. The reconciler re-checks legality. If the model call errors, the model refuses, or the output is invalid or illegal, the rule supervisor decides instead.
4. Every decision event records which supervisor decided, plus the fallback reason if there was one.
5. An escalation can carry a proposal: a hint to resume with, a fresh attempt with a hint, or globs to add to the scope before retrying. The dashboard shows it on the task with an **Accept** button, `spine status` prints it, and `spine accept ID` sends it. Accepting sends the ordinary `hint`, `retry` or `edit` command, so nothing happens that you couldn't do by hand. **Quality** shows how many proposals were accepted.

The model never writes status, and it cannot choose Verify or Merge.

The supervisor can use a different provider from the agents, for example a local model while Claude does the coding, or the reverse. Name a provider from Pi's `models.json`, which must use `openai-completions` and support `response_format: json_schema` (llama.cpp through Lemonade does):

```yaml
supervisor:
  type: llm
  provider: local
  model: Qwen3.6-35B-A3B-MTP-GGUF
  timeout: 5m           # allow for a local model loading and thinking
```

It is a direct API call with no tools, not a coding agent, and `effort` doesn't apply. `$NAME` keys are read from the controller's environment; a `!command` key is refused rather than run, so that decision falls back to the rules. `spine doctor --agent` checks the provider, and **Settings** can select it.

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
