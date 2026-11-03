# OpenCode as a headless worker agent under an unattended multi-agent supervisor (state as of 2026-11-03)

Method note: most findings come from a shallow clone of `github.com/sst/opencode`, which now resolves to `anomalyco/opencode`. The clone was taken at commit `1bf08ae4` (2026-11-02, version 2.2.5). Doc citations are to the V2 docs source in that repo (`services/www/src/docs/content/*.mdx`). That source renders on opencode.ai/docs; I checked the files, not the rendered site. GitHub API access was blocked in this session, so I have no issue-search or release-API data. The issue findings come from web search snippets and are marked as such.

**THE MOST IMPORTANT CONTEXT: OpenCode V2 (Sept 2026) is a major break from V1.** Most third-party knowledge about OpenCode describes 1.x. Examples include qwen.txt/beast.txt prompts, LSP diagnostics, `permission.bash`, the `@opencode-ai/sdk` package, the `opencode serve` V1 API, `OPENCODE_PERMISSION` and V1 JS plugins. All of these are dated or changed in V2. Older docs for 1.x still sit in the repo under `packages/web/src/content/docs/`.

## Headless interfaces (run / serve / SDK / ACP / attach) and supervisor control

### Takeaway
V2 has a solid headless surface. It includes:
- `opencode run --format json` (NDJSON events, exit codes)
- a background-service architecture with `--standalone` (private server) or `--server <url>` (attach)
- `opencode serve` with an OpenAPI-described API and a typed `@opencode/client` package
- `opencode api <operationId>` for ad-hoc calls
- `opencode acp`

Through the client, a supervisor can subscribe to events, reply to permissions, interrupt sessions, cancel forms, compact, list sessions, and pick the agent and model per run.

### Cited Findings
- Current version is 2.2.5 (2026-10-30). The npm package is `@opencode/cli`, renamed from V1's `opencode-ai`. The brew tap is `anomalyco/tap/opencode-v2`. The README links CI at `anomalyco/opencode` — [README](https://github.com/anomalyco/opencode/blob/dev/README.md), [CHANGELOG](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md)
- Global flags are `--standalone` ("Run with a private server instead of the background service") and `--server <url>` ("Connect to a server URL instead of the background service"). Permission flags are `--auto` ("Auto-approve permissions that are not explicitly denied"), plus hidden `--yolo` and `--dangerously-skip-permissions` — [packages/cli/src/commands/commands.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts)
- `opencode run` flags:
  - `--continue/-c`
  - `--session/-s` ("Session ID to continue, or to create if it does not exist")
  - `--fork`
  - `--worktree/-w [name]` ("Start a new session in a new git worktree")
  - `--model/-m provider/model#variant`
  - `--agent`
  - `--format default|json`
  - `--file/-f` (up to 100)
  - `--title`
  - `--archived`
  - `--thinking`

  Source: [commands.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts)
- `opencode serve` takes `--hostname`, `--port`, `--cors`, plus hidden `--service` and `--stdio`. Other subcommands include `acp`, `api` ("Make a request to the running server", taking an OpenAPI operation ID or method+path), `session list --format json`, session export/import, and `debug agents|config|heap-dump`. — [commands.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts), [docs cli/commands.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/cli/commands.mdx)
- The docs recommend standalone mode for CI: `opencode run --standalone --model anthropic/claude-sonnet-5-5 "..."`. They say the private server then receives the API key from the environment — [cli/commands.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/cli/commands.mdx)
- JSON mode writes one `JSON.stringify({type, timestamp, sessionID, ...})` line per event to stdout. Event types include `text`, step_start/step_finish and tool parts. Internally, run subscribes to the server event stream (`client.event.subscribe`). The exit code is 1 on errors, failures or a cancelled form, and 130 on interrupt — [packages/cli/src/run/noninteractive.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/run/noninteractive.ts)
- The client API surface used by run includes:
  - `permission.reply({sessionID, requestID, decision: "once"|"reject", message})`
  - `permission.list`
  - `session.interrupt`
  - `session.get`
  - `session.form.cancel`

  Events seen include `permission.asked`, `form.created`, `session.inbox.delivered`, `session.execution.interrupted` and `session.execution.failed`. Source: [noninteractive.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/run/noninteractive.ts)
- The V2 server API is an intentional breaking change. Integrations must move to `@opencode/client`. The server uses HTTP Basic auth with `OPENCODE_PASSWORD` (`OPENCODE_SERVER_PASSWORD` still works), and the username is always `opencode`. `OPENCODE_SERVER_USERNAME` was removed — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Manual compaction over the API: `opencode api post /api/session/<id>/compact --data '{}'`. The call is asynchronous; follow the `session.compaction.*` events — [compaction.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/compaction.mdx)
- The repo has a full ACP implementation in `packages/cli/src/acp/`. Its modules include permission, elicitation, sessions, replay, compaction and turn — [repo tree](https://github.com/anomalyco/opencode/tree/dev/packages/cli/src/acp)
- Packages in the monorepo include `@opencode/sdk` (plugin/tool SDK with effect, promise and workerd builds), `@opencode/client`, `@opencode/server`, `@opencode/core` and `@opencode/tui` — [packages/](https://github.com/anomalyco/opencode/tree/dev/packages)
- Third-party write-ups say V2 runs one background service that the terminal, desktop and web apps all connect to — [capitalandcompute.net](https://capitalandcompute.net/blog/opencode-2-0-what-changed/) (secondary)

### Inferences
- For one-worktree-per-task, the cleanest pattern is one of two:
  - (a) `opencode run --standalone --format json` per worktree. This gives process isolation and exit codes, at the cost of one server per worker.
  - (b) One long-lived `opencode serve` that the supervisor drives through `@opencode/client`, with sessions bound to different directories ("locations"). This saves memory but makes every worker share one process's fate.
- The default for plain `opencode run` without `--standalone` is to attach to a shared per-user background service. That is surprising under a supervisor, because environment variables such as API keys and config may come from the service rather than the worker process. Use `--standalone` or an explicit `--server`.

### Gaps
- I did not read the generated OpenAPI reference (`/api`) to list every endpoint, including whether there is a "set permission rules per session" endpoint.
- The SSE endpoint path is not confirmed. The client abstracts it.
- I could not confirm the V1 SDK name `@opencode-ai/sdk` or its deprecation status from npm.

## Unattended operation: permissions, non-interactive "ask", hangs, timeouts

### Takeaway
V2 permissions are an ordered last-match-wins list of `{action, resource, effect}` rules. If no rule matches, the effect is `ask`. In V2, non-interactive `run` never hangs on a prompt. It automatically rejects permission asks, and the model sees "Continue without this action". It also cancels `question` forms with model-visible feedback. `--auto` approves anything not explicitly denied. Defaults are permissive in one important respect: `external_directory` is now allowed by default.

### Cited Findings
- Rule format: `"permissions": [{ "action": "shell", "resource": "git push *", "effect": "deny" }]`. The last matching rule wins. Config layers are loaded lowest-priority first, then global, then agent rules last. "If no rule matches, OpenCode uses `ask`." Wildcards are `*` (which also matches `/`) and `?`. A shell pattern ending in ` *` also matches the bare command. A multi-resource operation is denied if any resource is denied, otherwise it asks if any resource asks — [permissions.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/permissions.mdx)
- Action names are `read`, `edit` (covers edit, write and patch), `glob`, `grep`, `shell` (a scanner-produced command string; compound commands may produce several), `subagent`, `skill`, `question`, `webfetch`, `websearch`, `external_directory`, `<server>_<tool>` for MCP, and `execute` (Code Mode). "`doom_loop` and `lsp` are not current V2 Core permission actions." — [permissions.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/permissions.mdx)
- V1→V2 renames: `bash`→`shell`, `task`→`subagent`, `write`/`patch`→`edit`. "V1 asks before accessing paths outside the project. V2 does not: `external_directory` is allowed by default." — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Non-interactive behaviour, from source:
  - Without `--auto`, each `permission.asked` gets the reply `decision: "reject"` with the message "This non-interactive run cannot ask the user for permission, so the request was rejected. Continue without this action."
  - The run continues, and the tool fails as model-visible output.
  - `question` forms are cancelled with "make reasonable assumptions and state them."
  - With `--auto` the reply is `decision: "once"`.
  - In V1-compat mode (`run/v1.ts`), a rejection instead interrupts the session.

  Source: [noninteractive.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/run/noninteractive.ts)
- Timeouts:
  - `shell` defaults to a 2-minute foreground timeout, overridable per call through `timeout` in ms. Background commands have no timeout unless one is set.
  - `glob` and `grep` have 30 s search timeouts.
  - `webfetch` allows up to 120 s.
  - MCP has separate `timeout.catalog` and `timeout.execution` settings.

  Source: [tools.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/tools.mdx), [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Config can be injected by environment variable: `OPENCODE_CONFIG_CONTENT` is read by core config and by the CLI server-process launcher. `OPENCODE_CONFIG_DIR` and `OPENCODE_DB` also exist — [packages/core/src/config.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/config.ts), [packages/cli/src/server-process.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/server-process.ts)
- `OPENCODE_PERMISSION` has no references in V2 source under `packages/core`, `packages/cli` or `packages/server`. It appears only in the V1 docs (`packages/web/src/content/docs/*/cli.mdx`) — grep of the repo at 1bf08ae4
- `policies` (admin-enforced) are separate from `permissions`. "Overriding a setting cannot stop users from adding providers, MCP servers, agents, or plugins, so express admin-enforced restrictions as `policies`" — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)

### Inferences
- For unattended workers, set an explicit baseline. Suggested rules:
  - a final `{"action":"*","resource":"*","effect":"allow"}`-style rule, or `--auto`, plus explicit denies (`git push *`, `external_directory`, `webfetch` as wanted)
  - deny `question`, so the model never tries to ask
- `--auto` alone leaves `external_directory` allowed (the V2 default). A worker can therefore touch sibling worktrees unless that is denied. This is a real risk with one worktree per task.
- The well-known V1 problem of hanging forever on an "ask" in headless mode looks addressed in `run` for V2. A custom driver over `serve` must still answer `permission.asked` events itself, or the session waits ("Wait for a decision from the client").

### Gaps
- I could not search GitHub issues for V2-specific hang or timeout reports, because GitHub API access was blocked.
- Whether V2 has an environment variable equivalent to `OPENCODE_PERMISSION` is unverified (none found by grep). `OPENCODE_CONFIG_CONTENT` with a `permissions` array looks like the replacement.

## System prompt and tool schema size; disabling tools / overriding prompts

### Takeaway
V2 drastically simplified per-provider prompts. The base prompt is about 750 chars plus tool guidance. Model-specific overrides exist only for GPT (gpt.txt and gpt-astra.txt), Claude (anthropic.txt, a 300-char append), Kimi, Arcee Trinity and Meta "muse". There is no qwen.txt or beast.txt in V2, so Qwen, GLM, DeepSeek and gpt-oss get the generic prompt. A custom agent's `system` field suppresses the model-specific override. Tools can be denied through permissions, and denying `execute` removes Code Mode.

### Cited Findings
- The generic system prompt (`session/runner/prompt/system.txt`) is 756 bytes. It starts "You are an AI agent running in OpenCode, a coding agent harness..." and includes the `${OPENCODE_TOOL_GUIDANCE}` placeholder — [system.txt](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/runner/prompt/system.txt)
- The per-model prompt files are in `packages/core/src/plugin/system-prompt/`:

  | File | Bytes |
  | --- | --- |
  | anthropic.txt | 300 |
  | gpt.txt | 2,572 |
  | gpt-astra.txt | 3,825 |
  | kimi.txt | 8,701 |
  | meta.txt | 7,853 |
  | trinity.txt | 7,684 |

  Matching works by model-ID substring ("gpt", "claude", "kimi", "trinity", "muse"). The Anthropic prompt is appended; the others override. The override is skipped if the agent defines its own `system` — [plugin/optimize.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/plugin/optimize.ts)
- Plugins that drop grep and glob for GPT and Claude exist but are "disabled intentionally" — [optimize.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/plugin/optimize.ts)
- Built-in tool plugins: artifact, edit, file-diff, glob, grep, image-generate, mcp-resource, opencode, patch, question, read, schedule, shell, skill, subagent, todo, webfetch, websearch, write. Todos are off by default in V2 (`experimental.todo`) — [packages/core/src/tool/plugin/](https://github.com/anomalyco/opencode/tree/dev/packages/core/src/tool/plugin), [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Code Mode:
  - Tools default into "CodeMode" (`codemode` defaults to true). There they are reached through an `execute` tool that runs JavaScript calling namespaced tools.
  - Core tools read, edit, write, shell, glob, grep and subagent set `codemode: false`, so they stay native provider tools. MCP and plugin tools therefore default into Code Mode.
  - "Denying `execute` with resource `*` removes Code Mode and every tool reached only through it."

  Source: [tool/AGENTS.md](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool/AGENTS.md), tool plugin sources, [tools.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/tools.mdx)
- Custom agents use the V2 `agents` map, with `system` (formerly `prompt`), `model: "provider/model#variant"`, `steps` (formerly `maxSteps`), `permissions` and `disabled`. Files go in `.opencode/agents/<name>.md`. Sampling settings such as temperature move to model variants; an agent's `request.body` "is kept but not sent to the model" — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Recent V2.2.5 changes:
  - Calls to a tool named `bash` now run as `shell`.
  - A failed `edit` lookup shows the closest lines.
  - A missed `read` names the nearest existing folder.

  These are model-robustness fixes. Source: [CHANGELOG v2.2.5](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md)

### Inferences
- With Qwen3-Coder, GLM, DeepSeek or gpt-oss, the effective prompt is small. Tool schemas for the roughly 8–10 native tools probably dominate, which is good for 32K–64K-context local models. The supervisor can supply its own `system` through a custom agent.
- Code Mode (JS calling tools) may be a risk for 32B open models that handle plain function calls better than code-wrapped tool use. Consider denying `execute` for local models when MCP tools are not needed.

### Gaps
- I did not measure the total token count of rendered tool schemas plus tool guidance. I also did not confirm whether a `patch`-specific prompt applies to gpt-oss: the "gpt" substring may match "gpt-oss", which would route it to gpt.txt and the patch tool. That is an inference from substring matching and is unverified.

## Edit tool mechanics, LSP diagnostics, formatters

### Takeaway
`edit` is an exact-unique-match `oldString`/`newString` replacement with normalisation fallbacks (Unicode and whitespace normalisation, line-trimmed matching). `patch` (the `*** Update File` format) is exposed only to supported GPT models. Multiedit is not a V2 built-in. **V2 has no LSP diagnostics at all.** Formatters still run after write and edit.

### Cited Findings
- The `edit` docs say: "the old text must match exactly once unless `replaceAll` is true", and "OpenCode may format the file after writing it." The `patch` docs say: "OpenCode exposes `patch` for supported GPT models; other models receive `edit` and `write` instead." — [tools.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/tools.mdx)
- `edit.ts` has a `normalizeForMatch` fallback (Unicode normalisation) and line-wise trimmed and whitespace-collapsed matching. It also detects the case where the replacement is already present. The code contains "TODO: Add LSP notification and diagnostics after LSP runtime exists." in both edit.ts and write.ts — [edit.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool/plugin/edit.ts), [write.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool/plugin/write.ts)
- "V2 accepts and preserves `lsp` configuration, but it does not run language servers, expose LSP tools, or produce LSP diagnostics. Replace workflows that depend on those capabilities with the project's lint, typecheck, or compiler commands." — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Formatters: the `formatter` config is unchanged from V1, and there is a `packages/core/src/formatter/builtins.ts` — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx), [formatter/](https://github.com/anomalyco/opencode/tree/dev/packages/core/src/formatter)

### Inferences
- The V1 nine-replacer fuzzy chain and LSP-after-edit feedback are dated claims. In V2 the supervisor or the worker's instructions should run typecheck and lint explicitly. Not spawning LSP servers per worker also cuts per-worktree memory (V1 had orphaned-LSP memory reports; see the resources section).

### Gaps
- I did not trace the full ordered list of edit fallbacks; only the normalisation helpers are confirmed.

## Provider support (OpenRouter, OpenAI-compatible self-hosted) and local-model tool calling

### Takeaway
Providers use AI SDK packages, now named `package: "aisdk:@ai-sdk/openai-compatible"` with `settings.baseURL` and `apiKey`, plus models.dev metadata (a snapshot is bundled). This covers vLLM, SGLang, llama.cpp and Ollama through their OpenAI-compatible endpoints. Local tool-calling failures that have been reported mostly trace to server-side parsers and chat templates, not to OpenCode.

### Cited Findings
- V2 provider format: `"providers": {"acme": {"package": "aisdk:@ai-sdk/openai-compatible", "settings": {"baseURL": "https://llm.example.com/v1", "apiKey": "{env:ACME_API_KEY}"}}}`. Further details:
  - Model fields are `capabilities.tools`, `capabilities.input` and `capabilities.output`.
  - Variants are an array with `settings` (provider options) and `body` (raw JSON fields such as `temperature`).
  - `options.setCacheKey` becomes `compatibility.supportsPromptCacheKey`.

  Source: [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Core has `models-dev/snapshot.txt` (a bundled models.dev catalogue), `model-retry.ts`, `plugin/tool-input-repair.ts`, and `aisdk.ts`/`aisdk-native.ts` — [packages/core/src](https://github.com/anomalyco/opencode/tree/dev/packages/core/src)
- V2.2.x fixes relevant to proxies and self-hosting:
  - LiteLLM context-window errors now trigger compaction.
  - Reasoning is no longer flattened to text when switching models, which caused request errors on some providers.
  - Compaction caps its prompt at the model window.

  Source: [CHANGELOG v2.2.5](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md)
- Self-hosted Qwen3-Coder-30B-A3B on vLLM with the `qwen3_coder` parser could not call tools, per a report against opencode 0.4.12 (old) — [issue #1809](https://github.com/anomalyco/opencode/issues/1809) (via search snippet)
- llama.cpp added a Qwen3-Coder XML tool-call parser; the model sometimes drops the opening tag — [llama.cpp PR #16755](https://github.com/ggml-org/llama.cpp/pull/16755) (via search snippet)
- Qwen3-Coder-Next behind LiteLLM: streamed `/v1/responses` output did not appear in opencode — [issue #17036](https://github.com/anomalyco/opencode/issues/17036) (snippet). The HF discussion reports that the "write tool is still broken" with llama.cpp until a fix or autoparser branch landed — [HF discussion](https://huggingface.co/unsloth/Qwen3-Coder-Next-GGUF/discussions/2) (third-party)
- Gas City's OpenCode profile lists `opencode/deepseek-v4-flash-free`, `opencode/nemotron-3-super-free` and `opencode/big-pickle` as model choices (OpenCode Zen free models) — [gascity profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go)

### Inferences
- With vLLM or SGLang, use the model-specific tool parser (`qwen3_coder`, the GLM parser, `openai` for gpt-oss) and `--enable-auto-tool-choice`. With llama.cpp use `--jinja` and a recent build. Set `capabilities.tools` and the context limit explicitly for custom models, because models.dev will not know about local deployments.
- OpenRouter is a models.dev provider and should work out of the box (not re-verified for V2).

### Gaps
- I did not verify that OpenRouter is listed in the V2 built-in provider docs (`providers.mdx` not read).
- I found no V2-specific (2.x) reports on local-model tool calling; all the issue evidence predates V2.

## Context management: compaction, pruning, sessions, snapshots

### Takeaway
Auto compaction is on by default. It produces a checkpoint summary and keeps about 15K recent tokens. `buffer` can be a token count or a percentage (new in 2.2.5). The V1 `prune`/`tail_turns` tool-output pruning was removed. Snapshots use a separate internal git object DB, so they do not touch the worktree's index or branches. They are captured per model step and can be disabled with `snapshots: false`.

### Cited Findings
- On compaction, OpenCode "summarizes the entire current conversation. It also keeps the most recent conversation, about 15,000 tokens by default". Later compactions update the existing summary. The settings are `compaction.auto`, `keep.tokens` and `buffer`. Compaction is also available manually through the API — [compaction.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/compaction.mdx), [CHANGELOG v2.2.5](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md)
- "V2 has no native `tail_turns` or `prune` field; both legacy fields are ignored with a warning." — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- How snapshots work:
  - A snapshot is taken before each model step and another after it.
  - Scope is the session's active directory inside a git worktree, covering tracked files and non-ignored untracked files up to 2 MiB.
  - They are stored "in a separate internal Git object database under its data directory. Snapshot capture does not create commits, move branches, or intentionally change your repository's index."
  - Capture is best effort.

  Source: [snapshots.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/snapshots.mdx)
- Sessions are stored in a database (`OPENCODE_DB`, a `database/` module). They can be exported and imported as JSON, forked with `--fork`, and continued with `--session` — [commands.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts), [cli/commands.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/cli/commands.mdx)

### Inferences
- With many worktrees, each per-step snapshot scans or hashes the directory, so the overhead grows with repo size. The supervisor already owns git state, so `snapshots: false` is a reasonable default for workers.
- Without pruning, tool-output-heavy sessions on 32K–64K local contexts will compact more often. Tune `buffer` as a percentage.

### Gaps
- I found no measurements of snapshot overhead on large repos.

## Subagents, custom agents, plugins, MCP, skills, AGENTS.md

### Takeaway
All of these exist in V2, with renames. **V1 plugins do not run in V2**, which directly breaks Gas Town's OpenCode integration, since that is a V1 JS plugin. Only `AGENTS.md` is read; the CLAUDE.md fallback is gone.

### Cited Findings
- `subagent` (formerly `task`) starts a child session with a subagent-mode agent. The default nesting depth is 1 (`experimental.subagent_depth`). Delegated commands now run in the background and report back to the parent — [tools.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/tools.mdx), [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- "V1 plugin implementations do not run in V2." Plugins use a new Effect-based API (`@opencode/plugin`) and live in `.opencode/plugins/<name>/index.ts` — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- MCP config moved to `mcp.servers.<name>`, with `disabled` and `timeout.{catalog,execution}`. MCP tools are exposed through Code Mode by default (see above). 2.2.5 fixed disconnects on stdio messages over 10 MiB — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx), [CHANGELOG](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md)
- Skills live in `.opencode/skills/<id>/SKILL.md`, with an ordered `skills` array of paths and URLs. Instructions come from a global `~/.config/opencode/AGENTS.md` plus ambient `AGENTS.md` files found walking up the tree. "V2 currently only discovers `AGENTS.md`" (no CLAUDE.md fallback) — [migrate-v1.mdx](https://github.com/anomalyco/opencode/blob/dev/services/www/src/docs/content/migrate-v1.mdx)
- Core also has `plugin/goal/*` prompts (kickoff, continuation, judge, reminder) and `schedule` tooling. These suggest built-in long-running "goal" loops — [packages/core/src/plugin/goal/](https://github.com/anomalyco/opencode/tree/dev/packages/core/src/plugin/goal)

### Inferences
- Instruction injection for workers should go through AGENTS.md in the worktree, or through a custom agent `system`, rather than V1 plugin hooks.

### Gaps
- I did not read the goal plugin's semantics.

## Resource overhead and concurrency

### Takeaway
OpenCode runs on Bun (packageManager `bun@1.4.4`). It has a long history of V1 memory-growth reports (multi-GB, OOM kills, orphaned LSP processes). V2 adds debug tooling (`debug heap-dump`, heap snapshots in desktop) and removes the LSP runtime. I found no V2 memory measurements.

### Cited Findings
- The root package.json pins `"packageManager": "bun@1.4.4"`. Gas City lists OpenCode process names as `opencode`, `node` and `bun` — [package.json](https://github.com/anomalyco/opencode/blob/dev/package.json), [gascity profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go)
- Memory issues reported against V1 (via search snippets, not independently verified):
  - [#11399](https://github.com/anomalyco/opencode/issues/11399): unbounded growth, about 15.5 GB CLI process.
  - [#17237](https://github.com/anomalyco/opencode/issues/17237): Linux OOM kill, opencode 1.2.24 with Bun 1.3.10.
  - [#18632](https://github.com/anomalyco/opencode/issues/18632): orphaned LSP processes, about 1.2 GB.
  - [#11868](https://github.com/anomalyco/opencode/issues/11868): 113 GB virtual memory on Windows.
  - PR [#16346](https://github.com/anomalyco/opencode/pull/16346): proposed forced GC; attributes VM growth to Bun/JSC region reservation; unmerged at the time of the snippet.
- V2 includes `opencode debug heap-dump` ("Write a heap snapshot of the running server") and `cpu-profile.ts`/`heap.ts` in the CLI. Desktop 2.1.x added "Capture heap snapshots" — [commands.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts), [CHANGELOG](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md)
- Gas City sets `ReadyDelayMs: 8000` for OpenCode, an 8-second startup wait before the prompt is considered deliverable — [gascity profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go)

### Inferences
- With N standalone workers, expect N Bun processes, each with its own server plus the run client. The shared background service amortises this but is a single point of failure. Budget memory per worker and kill and restart on growth.
- OpenTUI: the TUI is now `@opencode/tui` (Solid/TSX). That is irrelevant to headless `run`/`serve` use.

### Gaps
- I found no measured idle or active RSS for V2, and no V2 issues on concurrency. The GitHub API was blocked.

## Maintenance, org, cadence, licensing, 2026 changes, naming history (Kilo, Crush)

### Takeaway
The project is very active and very large. It has about 224K stars, 30K forks, about 21.9K commits, an MIT license, and the default branch `dev`. The org is now Anomaly (`anomalyco`), and `sst/opencode` redirects there. Releases come almost daily: 2.0.26 on Oct 8 through 2.2.5 on Oct 30, 2026, with three releases on Oct 30 alone. The high churn and the V2 breaking rewrite are the main stability risks.

### Cited Findings
- 224.1K stars, 30.1K forks, 2.4K open issues, 1.4K open PRs, 21,900 commits, MIT, default branch `dev` — [GitHub repo page](https://github.com/anomalyco/opencode) (fetched 2026-11-03)
- The CHANGELOG lists 2.0.26 (10-08), 2.0.27–2.0.32 (10-12 to 10-16), 2.1.0 (10-20), 2.1.2–2.1.4 (10-21 to 10-23), 2.2.0 (10-27), 2.2.1–2.2.2 (10-29) and 2.2.3–2.2.5 (10-30). Copyright is "2025 opencode" — [CHANGELOG](https://github.com/anomalyco/opencode/blob/dev/CHANGELOG.md), [LICENSE](https://github.com/anomalyco/opencode/blob/dev/LICENSE)
- Secondary sources say V2 shipped on about 11–12 Sept 2026, initially as a separate package with V1 as the default, and upgraded with `opencode upgrade --major`. They add that V2 2.2.0 dropped the GitHub Action — [capitalandcompute.net](https://capitalandcompute.net/blog/opencode-2-0-what-changed/), [ai-tldr 2.2.0](https://ai-tldr.dev/releases/anomalyco-opencode-2-2-0/) (secondary; dates conflict slightly)
- The V2 internals are a rewrite on Effect-TS: Effect CLI, an Effect plugin SDK, and the core split into the `core`, `server`, `protocol`, `schema` and `client` packages — [repo tree / package.json scripts](https://github.com/anomalyco/opencode/blob/dev/package.json)
- Crush and naming history (secondary):
  - The original Go project `opencode-ai/opencode` was archived in July 2025.
  - Charm continued it as Crush (launched around 2025-07-29), led by the original creator.
  - The "OpenCode" name stayed with the SST team's TypeScript project.
  - Community allegations about git-history rewriting are unverified.

  Source: [rywalker.com/research/crush](https://rywalker.com/research/crush), [awesome.ecosyste.ms entry](https://awesome.ecosyste.ms/api/v1/projects/github.com%2Fopencode-ai%2Fopencode)
- Forks noted in Gas City's own code: "MiMo Code (Xiaomi's `mimo` CLI) is an OpenCode fork", which uses a `--never-ask` flag — [gascity profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go)

### Inferences
- Pin an exact version (for example `@opencode/cli@2.2.5`) for fleet workers and disable auto-update. The relevant setting is `update: "disable"`, which replaces V1 `autoupdate`. The daily cadence and 2.x API changes make unpinned fleets fragile.

### Gaps
- Kilo CLI's relationship to OpenCode (widely reported as a fork of OpenCode) was not confirmed by any source I retrieved, so treat it as unverified.
- Contributor count and release count were not shown on the page and the API was blocked.
- I found no primary source on the SST→Anomaly org change; the redirect is the only evidence.

## Usage under Gas Town / Gas City; known issues for unattended runs

### Takeaway
Both Gas Town and Gas City have first-class OpenCode presets. Their integration was built against **V1** mechanisms: a V1 JS plugin (`gastown.js`), `OPENCODE_PERMISSION={"*":"allow"}` and an interactive TUI driven through tmux with `--prompt`. Gas City also declares ACP support (`opencode acp`). Against V2 these likely break silently. V1 plugins do not run, and `OPENCODE_PERMISSION` is not referenced in V2 source, so workers may fall back to `ask` and stall in the TUI.

### Cited Findings
- Gas Town lists OpenCode among its orchestrated agents. In its capability matrix, the OpenCode row reads: hooks "Yes (plugin JS)", no resume, `run` subcommand for non-interactive use, process names `opencode, node, bun`. It installs `internal/hooks/templates/opencode/gastown.js` into `.opencode/plugins` — [gastown docs/agent-provider-integration.md](https://github.com/gastownhall/gastown/blob/main/docs/agent-provider-integration.md)
- `gastown.js` is written to the V1 plugin API. It exports `async ({ $, directory }) => {...}` and injects `gt prime` context "via experimental.chat.system.transform" — [gastown.js](https://github.com/gastownhall/gastown/blob/main/internal/hooks/templates/opencode/gastown.js)
- Gas Town code comments note that polecats running non-Claude agents (for example opencode) fail fast in some paths, and that ACP sessions ("opencode agents running outside tmux") are protected from orphan cleanup. A Feb 2026 presentation says OpenCode agents need a nudge after start because they do not auto-prime like Claude Code — [gastown internal/polecat/session_manager.go, internal/util/orphan.go](https://github.com/gastownhall/gastown); [presentation](https://docs.google.com/presentation/d/1_MQQlaUjiUn5XDH6lYPP0ep2PZl_aW07/htmlpresent) (secondary)
- Gas City's built-in `opencode` profile:
  - `Command: "opencode"`, `PromptFlag: "--prompt"`, `ReadyDelayMs: 8000`
  - `AcceptStartupDialogs: false`
  - `Env: {"OPENCODE_PERMISSION": "{\"*\":\"allow\"}"}`, with a comment that this "overrides a project opencode.json's permission rules"
  - `SupportsACP: true`, `ACPArgs: ["acp"]`, `ResumeFlag: "--session"`, `InstructionsFile: "AGENTS.md"`

  Source: [gascity internal/worker/builtin/profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go)
- Gas City keeps active OpenCode conformance work, for example release gate ga-1hzxrg4 (2026-11-01): "OpenCode session lookup through symlinked work directories", which reads OpenCode session files for telemetry and transcripts (`GC_OPENCODE_TRANSCRIPT_DIR`). Its acceptance tests include an `opencode/tmux-cli` worker profile — [release-gates/ga-1hzxrg4](https://github.com/gastownhall/gascity/blob/main/release-gates/ga-1hzxrg4-opencode-symlink-workdir-gate.md), [test/acceptance/worker_inference](https://github.com/gastownhall/gascity/tree/main/test/acceptance/worker_inference)
- The latest commits in the clones are gastown on 2026-10-28 and gascity on 2026-11-03 — local clones

### Inferences
- **High-priority risk:** under OpenCode V2 the Gas Town plugin will not load, and Gas City's permission env var is probably ignored, because `OPENCODE_PERMISSION` has zero references in V2 source. An interactive TUI worker would then hit `ask` (the default when no rule matches) and wait. Mitigations:
  - pin OpenCode 1.x for these presets; or
  - supply V2 `permissions` through `OPENCODE_CONFIG_CONTENT` or a worktree `opencode.json`, and use `opencode run --standalone --auto --format json` or ACP instead of the TUI.
- Gas City's transcript discovery reads OpenCode's on-disk session files. V2 moved to a DB-backed store (`OPENCODE_DB`), so verify that the telemetry reader works with 2.x.

### Gaps
- I could not confirm which OpenCode major version Gas Town or Gas City test against. I also could not confirm whether V2 still honours `OPENCODE_PERMISSION` through a path not caught by grep, such as a schema package or a compatibility shim. Recommend a direct test: `OPENCODE_PERMISSION='{"*":"allow"}' opencode run "touch x"` on 2.2.5.
- I did not find third-party reports of large unattended OpenCode fleets.
