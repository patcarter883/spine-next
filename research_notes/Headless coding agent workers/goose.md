# Goose (aaif-goose/goose) as a headless worker under an unattended multi-agent supervisor

Method note: most findings come from a shallow clone of `https://github.com/aaif-goose/goose` at commit `cfb67d72` (committed 2026-11-02, workspace version 1.57.0), read on 2026-11-03. Links below use `https://github.com/aaif-goose/goose/blob/main/<path>` (abbreviated as **[repo:path]**). These are primary-source reads of the code and of the in-repo docs (`documentation/docs/...`, the source for the docs site). The GitHub API was not reachable from this session, so star, contributor and issue counts come from web pages or secondary sources and are marked as such. I downloaded the release binary to measure its size, but the sandbox did not allow me to execute it. **No runtime RAM or startup measurements were taken.**

Repo base URL: https://github.com/aaif-goose/goose

## Headless interfaces (goose run, recipes, server/ACP, SDK) and structured output

### Takeaway
Goose has good headless surfaces. `goose run` takes `-t`/`-i`/stdin, a recipe, `--no-session`, `--max-turns`, `--output-format text|json|stream-json`, and provider/model overrides. `goose acp` gives full ACP over stdio and `goose serve` gives ACP over HTTP/WebSocket. The old `goosed` REST server is gone and ACP is now the programmatic API. The stream-json event set is small: message, notification, error, complete with tokens and cost.

### Cited Findings
- `goose run` flags in current source: `-i/--instructions <file|->` (stdin with `-`), `-t/--text`, `--recipe`, `--system` (extra system prompt), `--params k=v`, `--sub-recipe` (repeatable), `--explain`, `--render-recipe`, `-q/--quiet` ("printing only the model response to stdout"), `--output-format` (`text`, `json`, `stream-json`), `--provider`, `--model`, `--interactive`, `--no-session` ("Useful for automated runs"), `--resume`, `--stats`, `--max-turns` (default 1000), `--max-tool-repetitions` (caps consecutive identical tool calls), `--with-extension` / `--with-streamable-http-extension` / `--with-builtin`, `--no-profile` (load only the extensions given on the CLI), `--container <docker id>` (runs extensions inside a container), `--debug` — [repo:crates/goose-cli/src/cli.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/cli.rs)
- `--output-format json` emits `{messages:[...], metadata:{total_tokens, input_tokens, output_tokens, cache_read_input_tokens, cache_write_input_tokens, cost_usd, status}}`. `stream-json` emits NDJSON events tagged by `type`: `message` (the full Message object), `notification` (`extension_id` plus `log{message}` or `progress{progress,total,message}`), `error{error}`, and `complete{total_tokens, input/output/cache tokens, cost_usd}` — [repo:crates/goose-cli/src/session/mod.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/session/mod.rs) (structs `JsonOutput`, `StreamEvent`, `NotificationData`)
- The docs recommend `stream-json` for "progress monitoring, long-running tasks" and `json` for CI — [repo:documentation/docs/guides/running-tasks.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/running-tasks.md)
- Other subcommands in the CLI: `acp` ("Run goose as an ACP agent server on stdio"), `serve` ("Start ACP server over HTTP and WebSocket"; options `--tls`, `--tls-cert-path/--tls-key-path`, `--dangerously-unauthenticated` (skips the `GOOSE_SERVER__SECRET_KEY` requirement), `--allowed-origin`, `--enable-scheduler`, plus optional p2p "roam" exposure), `recipe`, `schedule`, `skills`, `plugin`, `gateway`, `review` (diff-range code review with per-check models), `local-models`, `mcp` — [repo:crates/goose-cli/src/cli.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/cli.rs)
- The Desktop app "normally runs its own `goose serve` ACP server process in the background". A remote server is started with `GOOSE_SERVER__SECRET_KEY=... goose serve --platform desktop --host 0.0.0.0 --port 3000 --tls` — [repo:documentation/docs/guides/remote-goose-server.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/remote-goose-server.md)
- No `goose-server`/`goosed` crate exists in the workspace any more. The only `[[bin]]` targets are in `goose-cli` (`goose`, `generate_manpages`, `mcp_conformance_driver`) — [repo:crates/goose-cli/Cargo.toml](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/Cargo.toml)
- The official embedding path ("GDK"): "launch the pre-built goose binary and talk to it over ACP… your app sends prompts and receives streamed responses, tool activity, and permission requests. This is also how goose Desktop talks to its backend." The example spawns `goose acp` and uses `@agentclientprotocol/sdk@1.5.0`. stdout carries ACP and stderr carries logs — [repo:documentation/docs/gdk/acp/index.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/gdk/acp/index.md)
- `goose-sdk` crate at version `0.1.0-alpha.13`: UniFFI bindings for Python and Kotlin covering provider construction and streaming completions, plus `ObservabilityHook` request lifecycle events — [repo:crates/goose-sdk/README.md](https://github.com/aaif-goose/goose/blob/main/crates/goose-sdk/README.md)
- Recipes are YAML with parameters, `settings.max_turns`, and sub-recipes. `GOOSE_RECIPE_RETRY_TIMEOUT_SECONDS` and `GOOSE_RECIPE_ON_FAILURE_TIMEOUT_SECONDS` exist for retry and on-failure logic — [repo:documentation/docs/guides/environment-variables.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/environment-variables.md)
- Lifecycle hooks follow the Open Plugins hooks spec and are discovered from `~/.agents/plugins/` or `<project>/.agents/plugins/`. Events: `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `PreToolUse`, `PreToolUseResult`, `PostToolUse`, `PostToolUseFailure`, `BeforeShellExecution`, `AfterShellExecution`, `BeforeReadFile`, `AfterFileEdit`, `SubagentStart`, `SubagentStop`, `Stop`. A `PreToolUse` hook can deny a call (exit 2, or `{"decision":"block"}`), and `on_failure` controls what happens when a hook fails — [repo:documentation/docs/guides/context-engineering/hooks.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/context-engineering/hooks.md)

### Inferences
- A supervisor has two practical integration modes. (a) `goose run --no-session --no-profile --with-builtin developer --output-format stream-json -t "..."` per worktree, then parse NDJSON until `complete` or `error`. (b) A long-lived `goose acp` subprocess per worker, for multi-turn steering and permission requests. Gas City already has an ACP runtime, so (b) maps directly (see the last section).
- Hooks (`PreToolUse` deny, `AfterFileEdit`, `Stop`, `SessionEnd`) let a supervisor add policy and telemetry without patching goose. A `.agents/plugins/` dir can be dropped into each worktree.
- The `json`/`stream-json` events carry no explicit task-outcome or turn-limit-reached reason beyond `metadata.status`, so a supervisor still has to judge success itself (run tests, check the git diff).

### Gaps
- The exact process exit codes for `goose run` were not verified (max turns hit, provider error, tool failure). `std::process::exit(1)` appears on several error paths in `cli.rs`, but I did not trace every outcome.
- I did not enumerate the set of values `metadata.status` can take.

## Unattended operation: GOOSE_MODE, permissions, max turns, timeouts, sandboxing

### Takeaway
The default mode is `auto` (approve everything), and `--max-turns` (default 1000), `--max-tool-repetitions` and a 300 s default per-tool timeout bound the run. Goose has **no built-in OS-level sandbox** for the shell. Isolation is up to the supervisor (container, worktree, user). `--container` only moves extensions into a Docker container. There is an open bug that unattended scheduled recipes fail.

### Cited Findings
- `GooseMode` enum: `Auto` (the `#[default]`, "Automatically approve tool calls"), `Approve` ("Ask before every tool call"), `SmartApprove` ("Ask only for sensitive tool calls"), `Chat` ("no tool calls") — [repo:crates/goose-provider-types/src/goose_mode.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-provider-types/src/goose_mode.rs)
- `GOOSE_MAX_TURNS` defaults to 1000 — [repo:crates/goose-cli/src/commands/configure.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/commands/configure.rs). Subagent turn precedence is: delegate-tool arg, then recipe `settings.max_turns`, then `GOOSE_SUBAGENT_MAX_TURNS`, then `GOOSE_MAX_TURNS` — [repo:documentation/docs/guides/context-engineering/subagents.mdx](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/context-engineering/subagents.mdx)
- The `shell` tool takes an optional `timeout_secs` and otherwise falls back to the configured default extension timeout, `DEFAULT_EXTENSION_TIMEOUT = 300` seconds. The result reports when a process was killed for timeout — [repo:crates/goose/src/agents/platform_extensions/developer/shell.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/agents/platform_extensions/developer/shell.rs), [repo:crates/goose/src/config/extensions.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/config/extensions.rs)
- The `--container` flag runs "extensions (stdio and built-in) inside the specified container… For built-in extensions, goose must be installed inside the container." — [repo:crates/goose-cli/src/cli.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/cli.rs)
- Other safety-related settings: `GOOSE_ALLOWLIST` (an extension allowlist), and security guides for prompt-injection detection and an "adversary mode" — [repo:documentation/docs/guides/environment-variables.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/environment-variables.md), [repo:documentation/docs/guides/security/](https://github.com/aaif-goose/goose/tree/main/documentation/docs/guides/security)
- Open issue #11164 (opened 2026-08-12): "Scheduler unusable for unattended recipes on 1.45.0". Reported symptoms: headless runs fail at the first tool call, the recipe's `goose_mode` is ignored, an interrupted run-now wedges the job, and spawned sessions lack `schedule_id` (title paraphrased by the fetch tool) — [GitHub issues search](https://github.com/aaif-goose/goose/issues?q=is%3Aissue+ollama+tool+calls)
- Open issue #12513 (2026-09-25): "ACP: shell is always delegated to the client terminal (bypassing sandboxes)" — [GitHub issues search](https://github.com/aaif-goose/goose/issues?q=is%3Aissue+ollama+tool+calls)
- Open issue #10619: cancelling a prompt from an ACP provider client does not interrupt the agent, and the turn runs to completion — [GitHub issues search](https://github.com/aaif-goose/goose/issues?q=is%3Aissue+ollama+tool+calls)

### Inferences
- For Gas Town-style polecats, run goose inside the per-task container or VM with `GOOSE_MODE=auto` and a lower `--max-turns`, such as 100–300. Harbor evals cap at 100 turns.
- Issue #11164 matters only if you use goose's own scheduler. An external supervisor calling `goose run` avoids it, but treat it as a warning sign about headless polish.
- ACP cancellation (#10619) being unreliable means a supervisor should be ready to kill the process rather than rely on `session/cancel`.

### Gaps
- I did not verify whether #11164 is fixed in 1.57; it was still listed as open.
- I did not inspect the `permission.yaml` per-tool allow/ask/deny format in detail.
- I did not check whether a wall-clock timeout covers the whole run, as opposed to per tool. I found none, so the supervisor should enforce one.

## Code editing: developer extension tools and edit mechanics

### Takeaway
The current developer extension is minimal: `shell`, `write` (whole file), `edit` (exact, unique `before`/`after` find-and-replace), `tree`, and `read_image`. There is no dedicated file-view tool; the prompt tells the model to use `cat`/`sed`/`rg` via shell. The old `text_editor` tool and the Morph/Relace "editor model" fast-apply feature appear to be gone. There is no fuzzy matching, though a failed match returns similar context.

### Cited Findings
- Tools registered: `write`; `edit` ("Edit a file by finding and replacing text. The before text must match exactly and uniquely. Use empty after text to delete."); `shell`; `tree`; `read_image` — [repo:crates/goose/src/agents/platform_extensions/developer/mod.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/agents/platform_extensions/developer/mod.rs)
- Edit params are `FileEditParams{path, before, after}` and write params are `FileWriteParams{path, content}`. `string_replace` errors on zero matches and on multiple matches ("Found N matches. Please provide more context…"), and the code includes a `find_similar_context` helper for the zero-match case — [repo:crates/goose/src/agents/platform_extensions/developer/edit.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/agents/platform_extensions/developer/edit.rs)
- System instructions: "prefer the flow of using tree to understand the codebase structure and file sizes. When you need to search, prefer rg… Then use cat or sed to gather the context you need, always reading before editing. Use write and edit…" — [repo:crates/goose/src/agents/platform_extensions/developer/mod.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/agents/platform_extensions/developer/mod.rs)
- Other platform extensions present: `analyze` (tree-sitter style code graph), `code_execution` ("code mode"), `todo`, `summon` (subagents), `summarize`, `chatrecall`, `apps`, `scheduler` — [repo:crates/goose/src/agents/platform_extensions/](https://github.com/aaif-goose/goose/tree/main/crates/goose/src/agents/platform_extensions)
- A grep for `morph`, `relace` or `EDITOR_API` finds nothing in the code or docs apart from provider metadata JSON. No `GOOSE_EDITOR_*` variables appear in the env-var docs — [repo:documentation/docs/guides/environment-variables.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/environment-variables.md)
- Open bug #12884 (2026-10-13): "write/edit tools strip XML-like tokens (</>) from tool-call arguments". This would corrupt edits to HTML, JSX or generics-heavy code if it hits — [GitHub issues search](https://github.com/aaif-goose/goose/issues?q=is%3Aissue+ollama+tool+calls)

### Inferences
- These are the same str_replace-style mechanics as Claude Code's or OpenCode's edit tools. Edit quality depends mostly on the model; the harness adds no extra edit help. Exact, unique matching is safe but makes weaker 32B models retry more often.
- Reading files only through shell `cat` costs more tokens and has no line-range tool, unless the model uses `sed -n`. That is a slight disadvantage for small-context local models.

### Gaps
- I found no head-to-head study of edit accuracy (for example Aider's edit benchmark) that includes goose.

## Provider support and local model tool-calling

### Takeaway
Provider coverage is broad. There are native OpenRouter, OpenAI, OpenAI-compatible, Ollama and Anthropic providers, about 50 declarative JSON providers (including LM Studio, llama-swap, DeepSeek, Z.ai/GLM, Moonshot), custom providers in OpenAI/Anthropic/Ollama format, built-in local inference, and an experimental toolshim. Ollama and local models are the area with the most active bugs.

### Cited Findings
- Native provider modules include `openrouter.rs`, `openai.rs`, `openai_compatible.rs`, `ollama.rs`, `anthropic.rs`, `google.rs`, `databricks*.rs`, `azure_foundry.rs`, `snowflake.rs`, and `local_inference.rs` — [repo:crates/goose-providers/src/](https://github.com/aaif-goose/goose/tree/main/crates/goose-providers/src)
- Declarative provider definitions include `lmstudio`, `llama_swap`, `lemonade`, `deepseek`, `zai`, `zai_coding_plan`, `zhipu`, `moonshot`, `minimax`, `groq`, `cerebras`, `together`, `fireworks`, `nvidia`, `ollama_cloud`, `opencode_go/zen`, `vercel_ai_gateway`, `mistral`, and others (about 50) — [repo:crates/goose-providers/src/declarative/definitions/](https://github.com/aaif-goose/goose/tree/main/crates/goose-providers/src/declarative/definitions)
- "Custom providers must use OpenAI, Anthropic, or Ollama compatible API formats", with custom headers allowed — [repo:documentation/docs/getting-started/providers.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/getting-started/providers.md). The env overrides are `GOOSE_PROVIDER__TYPE`, `GOOSE_PROVIDER__HOST` and `GOOSE_PROVIDER__API_KEY` — [repo:documentation/docs/guides/environment-variables.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/environment-variables.md). vLLM and SGLang would be reached through the OpenAI-compatible path; there is no vLLM- or SGLang-specific definition.
- Tool shim is marked "Experimental". It is meant for models that "intermittently output tool calls as plaintext", for local Ollama/llama.cpp models without native tools, and for OpenAI-compatible endpoints "that mix reasoning tags (`<think>`) with tool calls". It needs a separate interpreter model (default Ollama `mistral-nemo`). `GOOSE_TOOLSHIM_BACKEND` takes `ollama|local|llama.cpp`. The docs claim "Most locally-hosted models… will need the shim" — [repo:documentation/docs/guides/tool-shim.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/tool-shim.md)
- Recent local and Ollama issues from GitHub search, as of 2026-11:
  - #13090 (open, 2026-10-22): "Ollama stall timeout aborts long tool calls Ollama is still generating"
  - #11951 (open): Ollama providers never call `/api/chat`, so think/hybrid-reasoning control cannot be reached
  - #12785 (open): Desktop drops thinking effort for non-OpenAI models on OpenAI-compatible providers
  - #12371 and #12403 (closed 2026-09-21): defining XML tool-call envelopes for Ollama streams
  - #10353 (closed): an empty provider turn silently ended the loop
  - #10110 (closed 2026-08-13): multi-turn execution hung after the first response
  - Source: [GitHub issues search](https://github.com/aaif-goose/goose/issues?q=is%3Aissue+ollama+tool+calls)
- Third-party guides say Ollama's default 4096-token context is too small for goose and recommend raising `OLLAMA_CONTEXT_LENGTH`; one lists `qwen3-coder:30b` as a tool-capable choice. These are secondary sources — [localaimaster](https://localaimaster.com/blog/goose-ollama-local-agent), [llmconfigurator](https://llmconfigurator.com/en/tools/agent-harness-matrix/goose)
- Goose's own Harbor eval: `nemotron-3-nano-30b-a3b` solved 1.1% of Terminal-Bench 2 (1/89). The README says "the small model gives up or loses tool-call structure earlier" — [repo:evals/harbor/README.md](https://github.com/aaif-goose/goose/blob/main/evals/harbor/README.md)

### Inferences
- For vLLM or SGLang, use a custom OpenAI-compatible provider with the server's native tool parser enabled (for example `--tool-call-parser qwen3_coder`, `glm45`, or `openai` for gpt-oss) rather than the toolshim. The shim adds a second model hop and is experimental.
- The many Ollama and reasoning-tag issues in recent months suggest local-model support is improving but still fragile. Pin a goose version per fleet and canary new releases.

### Gaps
- I found no primary-source results for goose with Qwen3-Coder, GLM-4.x, DeepSeek or gpt-oss on a coding benchmark.
- There is no OpenRouter-specific known-issue data beyond the existence of `openrouter.rs` and `openrouter_format.rs`.

## Context management, multi-model and subagents

### Takeaway
Auto-compaction triggers at 80% of the context limit or 225k tokens, whichever comes first, and older tool outputs are summarized past a cutoff. The old lead/worker multi-model mode is no longer documented. Multi-model use is now `/model` switching and subagents with their own provider and model (max 5 concurrent, 5-minute default timeout).

### Cited Findings
- `DEFAULT_COMPACTION_THRESHOLD = 0.8`, overridable with `GOOSE_AUTO_COMPACT_THRESHOLD` (0.0 disables it) — [repo:crates/goose-context-management/src/lib.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-context-management/src/lib.rs), [repo:crates/goose/src/context_mgmt/mod.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/context_mgmt/mod.rs)
- `GOOSE_AUTO_COMPACT_TOKEN_LIMIT` defaults to 225000; "the earlier of this and the percentage threshold applies". `GOOSE_TOOL_CALL_CUTOFF` is the number of tool calls kept in full before older outputs are summarized; by default it is computed from the context limit. `GOOSE_CONTEXT_LIMIT`, `GOOSE_INPUT_LIMIT` and `GOOSE_MAX_TOOL_RESPONSE_SIZE` also exist — [repo:documentation/docs/guides/environment-variables.md](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/environment-variables.md)
- The multi-model docs page lists only "Switching Models Mid-Session" (`/model`), "Subagents" and context engineering. A grep for `GOOSE_LEAD_MODEL`, `lead_worker` or "lead/worker" finds nothing in the code or docs at this commit — [repo:documentation/docs/guides/multi-model/index.mdx](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/multi-model/index.mdx)
- Subagents run only in autonomous mode ("disabled in manual approval, smart approval, and chat-only modes"). The concurrency default is 5 (`GOOSE_MAX_CONCURRENT_SUBAGENTS`), the timeout default is 5 minutes, and `GOOSE_SUBAGENT_MODEL` / `GOOSE_SUBAGENT_PROVIDER` set the subagent model. External CLIs such as Codex can be used as subagents — [repo:documentation/docs/guides/context-engineering/subagents.mdx](https://github.com/aaif-goose/goose/blob/main/documentation/docs/guides/context-engineering/subagents.mdx)

### Inferences
- With a 32k–128k local context, 0.8 compaction fires often. The supervisor may prefer short, scoped tasks per worktree to relying on summaries. A summarization pass on a local 32B model may also lose task state.
- If the design depends on a "strong planner plus cheap worker" setup inside goose, lead/worker is no longer available. Use subagents, or have the supervisor pick the model per task.

### Gaps
- I could not confirm from the shallow clone which release removed lead/worker (no history available).
- I did not read the summarization prompt or its quality.

## Extensibility: MCP, recipes, hints

### Takeaway
Goose is MCP-native: stdio and streamable-HTTP extensions can be added per run from the CLI. It also has recipes, skills (Agent Skills standard), plugins with hooks, and it reads both `.goosehints` and `AGENTS.md`.

### Cited Findings
- Context files are `GOOSE_HINTS_FILENAME = ".goosehints"` and `AGENTS_MD_FILENAME = "AGENTS.md"` — [repo:crates/goose/src/hints/load_hints.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose/src/hints/load_hints.rs)
- Per-run MCP: `--with-extension '[name:]ENV=v cmd args'`, `--with-streamable-http-extension 'url timeout=N'`, and `--no-profile` to exclude user defaults — [repo:crates/goose-cli/src/cli.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/cli.rs)
- The context-engineering guides cover custom agents, hooks, plugins, prompt templates, slash commands, subagents, goosehints, persistent instructions and skills — [repo:documentation/docs/guides/context-engineering/](https://github.com/aaif-goose/goose/tree/main/documentation/docs/guides/context-engineering)
- The Gas City docs list Goose among adopters of the Agent Skills standard — [gascity engdocs](https://github.com/gastownhall/gascity/blob/main/engdocs/design/packv2/doc-agent-v2.md)

### Inferences
- Gas Town's per-worktree `AGENTS.md` and CLAUDE.md-style priming would be picked up natively through `AGENTS.md`. Beads or `gt` tooling could be exposed as an MCP extension or simply used via shell.

### Gaps
- I did not check whether `CLAUDE.md` is also read by default; the configurable context file names were not checked.

## Resource overhead

### Takeaway
Measured: the Linux x86_64 release binary is about **208 MB** uncompressed (55.7 MB `.tar.bz2`). That is much larger than a lean Rust CLI, likely because it bundles local inference and many features. I could not run the binary, so there are **no verified RAM or startup numbers**. Claims like "30 MB RAM, <100 ms" are unverified and the binary size makes them doubtful.

### Cited Findings
- `goose-x86_64-unknown-linux-gnu.tar.bz2` from `releases/latest` (2026-11-03) is 55,723,558 bytes and extracts to one `goose` binary of 208,210,048 bytes (file mtime Oct 28 2026). This is my own download — [releases/latest](https://github.com/aaif-goose/goose/releases/latest)
- The workspace includes the `goose-local-inference` crate, a `local_inference.rs` provider and voice/live providers, which plausibly explains the size — [repo:crates/](https://github.com/aaif-goose/goose/tree/main/crates)
- A T2 Linux package of goose-cli v1.41.0 lists about 273 MB installed — [t2linux](https://www.t2linux.com/packages/goose-cli)
- A web search found no third-party RSS or startup benchmark of goose — [search result summary; no goose-specific source]

### Inferences
- With N parallel workers, each runs its own goose process plus any stdio MCP children. The page-cache cost of the shared binary is paid once, but per-process RSS is unknown. Measure with `/usr/bin/time -v goose run ...` before planning fleet density. The model server will dominate memory regardless.

### Gaps
- Peak RSS, idle RSS and cold-start latency for `goose run` are unmeasured, because executing the downloaded binary was blocked in this sandbox.

## Maintenance and governance

### Takeaway
The project is very active: weekly minor releases (v1.57.0 is current), Apache-2.0, and governed under the Linux Foundation's AAIF. Block contributed it at AAIF's launch on 2025-12-09, and the repo moved to `aaif-goose/goose` around 2026-04-07. Stars are about 54k according to secondary sources.

### Cited Findings
- License Apache-2.0; workspace version 1.57.0 — [repo:Cargo.toml](https://github.com/aaif-goose/goose/blob/main/Cargo.toml), [repo:LICENSE](https://github.com/aaif-goose/goose/blob/main/LICENSE)
- Release cadence: "Minor version releases… are typically done once per week". The version-bump action runs every Tuesday, and patch branches are created automatically — [repo:RELEASE.md](https://github.com/aaif-goose/goose/blob/main/RELEASE.md)
- Recent tags from `git ls-remote`: v1.47.0 through v1.57.0, with only one patch (v1.50.1) in that range — [tags](https://github.com/aaif-goose/goose/tags)
- AAIF launched 2025-12-09 with MCP, AGENTS.md and goose. The repo move to `aaif-goose` was announced 2026-04-07 (Michael Neale), and old URLs redirect. Stars: 54.4k on 2026-09-18 per Latent Space wiki, and 54,156 on 2026-09-12 per a review site. Both are secondary sources — [Latent Space wiki](https://latent.space/wiki/aaif/), [rywalker.com](https://rywalker.com/research/goose)
- The repo has `GOVERNANCE.md` and `MAINTAINERS.md`, and the README links the LF Insights health score — [repo:README.md](https://github.com/aaif-goose/goose/blob/main/README.md)
- Changes in 2026 visible in the code: ACP became the primary API (`goose serve` replaced goosed); GDK and `goose-sdk` were added; plugins, hooks and skills were added; the `goose review` command and "roam" p2p were added; lead/worker is no longer documented; the developer toolset was slimmed to write/edit/shell/tree — [repo:crates/goose-cli/src/cli.rs](https://github.com/aaif-goose/goose/blob/main/crates/goose-cli/src/cli.rs)

### Inferences
- A weekly release train with heavy refactoring (the server API was replaced and tools renamed) means a supervisor integration must pin versions. Integration contracts such as stream-json shape and tool names have shifted during 2025–26.

### Gaps
- The exact contributor count was not retrieved (GitHub API unavailable).
- I could not get release dates per tag.

## Known issues and benchmarks

### Takeaway
Goose's own Harbor runs on Terminal-Bench 2 with Sonnet 4.6 show stock goose at 50.6%, code-mode at 57.3%, OpenCode at 52.8% and Pi at 47.2%, all on the same model. That puts goose's harness on par with peers when the model is strong. A 30B-A3B open model scored 1.1%. I found no SWE-bench results for goose.

### Cited Findings
- Harbor snapshot (89-task Terminal-Bench 2, 100-turn cap):
  - Run-name confusion: `sonnet46-full` ("Stock goose… developer,todo") 50.6%. `goose-1.30-sonnet46-full` 50.6%. A run named `claude-sonnet46-full` scored 55.1%, and the README describes it as "harbor's vanilla `Goose` harness (curl-installed)". The labeling is ambiguous, so this row may also be goose.
  - Other goose configurations: `goose-sonnet46-full-code-mode` 57.3%; `sonnet46-summon-full` 55.1%.
  - Other harnesses on the same model: `opencode-sonnet46-full` 52.8%; `pi-sonnet46-full` 47.2%.
  - Small open model: `nemotron-3-nano-30b-a3b` 1.1%.
  - Results are self-reported by the goose team.
  - Source: [repo:evals/harbor/README.md](https://github.com/aaif-goose/goose/blob/main/evals/harbor/README.md)
- Epoch AI's Terminal-Bench page lists Goose among the agents evaluated, but no score was extracted. The tbench.ai leaderboard page rendered no rows when fetched — [Epoch AI](https://epoch.ai/benchmarks/terminal-bench), [tbench.ai](https://www.tbench.ai/leaderboard/terminal-bench/2.0)
- Issues relevant to unattended runs, beyond those listed above:
  - #10256 (closed): `goose serve` crashed with SIGABRT on recursive MCP `outputSchema`
  - #10702 (closed): "not connected" on first extension use
  - #13260 (open): Desktop 1.57.0 replies stored but never rendered
  - Source: [GitHub issues search](https://github.com/aaif-goose/goose/issues?q=is%3Aissue+ollama+tool+calls)

### Inferences
- With frontier models, goose is a competitive harness. With 32B–70B open weights, the harness has little evidence behind it, and its own data point (a 3B-active MoE) is very poor. Expect results close to what the model alone delivers, and run your own Harbor eval; the tooling is in-repo and supports OpenRouter.

### Gaps
- I found no SWE-bench, SWE-bench Verified or Aider results for goose, and no independent Terminal-Bench number with an open-weight 32B–70B model.

## Usage under Gas Town / Gas City or similar orchestrators

### Takeaway
Neither Gas Town nor Gas City ships a goose preset. Gas City's ACP runtime with a custom `acp_command` is the natural integration point, since `goose acp` speaks ACP on stdio. I found no public reports of goose running under Gas Town.

### Cited Findings
- In current `gastownhall/gascity` and `gastown` source, a count of provider-name literals gives: claude 2759, codex 1221, gemini 513, opencode 405, kiro 181, pi 163, cursor 141, copilot 134, amp 59, auggie 44. The single "goose" literal is a polecat name in Gas Town's `namepool.go`, not a runtime — [gascity](https://github.com/gastownhall/gascity), [gastown](https://github.com/gastownhall/gastown)
- Gas City has `internal/runtime/acp`. Provider specs support `acp_command` and `acp_args` ("When transport is 'acp', the ACP-specific command… is used") — [gascity internal/config/launch_command.go](https://github.com/gastownhall/gascity/blob/main/internal/config/launch_command.go)
- Third-party summary: Gas City (announced 2026-04-24, MIT) has runtime providers tmux, subprocess, exec, ACP and k8s. Gas Town defaults to Claude Code, with Codex, Gemini, Cursor, Auggie and Amp presets plus custom presets — [rywalker.com/gas-city](https://rywalker.com/research/gas-city), [nathan-norman.vercel.app](https://nathan-norman.vercel.app/ai-stacks/gas-town)

### Inferences
- Feasible integration: define a custom Gas City provider with `transport=acp`, `acp_command=goose`, `acp_args=["acp","--with-builtin","developer"]`. Alternatively use the exec/subprocess runtime with `goose run --output-format stream-json`. Watch ACP issues #12513 (shell delegated to the client terminal) and #10619 (cancel), which affect exactly this path.

### Gaps
- I did not check whether Gas City's ACP runtime implements the client-terminal capability that goose delegates shell to (#12513). If it does not, shell behavior under Gas City ACP needs testing.
- No community reports of goose under any Yegge-style orchestrator were found.
