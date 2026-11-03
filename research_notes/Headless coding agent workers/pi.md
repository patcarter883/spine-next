# Pi coding agent as a headless worker under an unattended multi-agent supervisor

Method note: Most findings below were **checked against source**. I cloned `github.com/badlogic/pi-mono`, which now redirects to `github.com/earendil-works/pi`. HEAD was commit `9c9ce85`, dated 2026-11-03, with package `@earendil-works/pi-coding-agent` v1.5.0. I read the docs, CHANGELOG and TypeScript source directly. I also shallow-cloned `gastownhall/gastown` (HEAD 2026-10-28) and `gastownhall/gascity` (HEAD 2026-11-03) to check how they integrate Pi. Items marked "third-party" come from blogs or aggregators and were not checked. Repo URLs below use the canonical `earendil-works/pi` path. Note that the npm name in the assignment, `@mariozechner/pi-coding-agent`, is **dated**: the package was renamed on 2026-05-07.

## Headless interfaces, telemetry, and mid-run steering

### Takeaway
Pi has four interfaces, all checked in source: print (`-p`), a one-shot JSONL event stream (`--mode json`), a long-lived bidirectional JSONL RPC over stdio (`--mode rpc`), and an in-process TypeScript SDK. A supervisor gets per-message token and cost usage, plus tool start/update/end events with durations. Over RPC it can steer, queue follow-ups, abort, compact, switch model or thinking level, and fork sessions mid-run.

### Cited Findings
- **Four modes.** Interactive TUI, Print ("final text on stdout, one invocation"), JSON ("JSONL events on stdout, one invocation"), and RPC ("JSONL commands, responses, and events, long-lived"). The SDK embeds the agent in a Node.js/Bun process. If stdin or stdout is non-TTY and no mode is given, Pi falls back to print mode. — [docs/cli-integration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli-integration.md)
- **Print-mode exit codes** (checked in `src/modes/print-mode.ts`):
  - A final assistant `stopReason` of `error` or `aborted` gives exit 1, with the error on stderr.
  - `length` (output limit hit) prints the partial text but still exits 1.
  - SIGHUP gives 129; other signals give 143.
  - [print-mode.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/modes/print-mode.ts)
- **JSON-mode exit-code caveat.** "A failed or aborted assistant response appears in the event stream but does not by itself produce a nonzero exit status. Inspect the events when success or failure matters." — [docs/cli-integration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli-integration.md)
- **JSON stream framing.** It starts with a session header: `{"type":"session","version":3,"id":...,"cwd":...}`. Framing is strict LF-delimited JSONL. The docs warn that Node `readline` is unsafe because it splits on U+2028/U+2029. Stdout carries only protocol data; logs go to stderr. Slow readers can stall Pi through backpressure. — [docs/json.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/json.md)
- **Event types:**
  - Lifecycle: `agent_start`, `agent_end` (`messages`, `willRetry`), `agent_settled` (`aborted`), `turn_start`, `turn_end` (`message`, `toolResults`).
  - Messages: `message_start`, `message_update`, `message_end` (`message_end` is authoritative).
  - Streamed deltas: `text_delta`, `thinking_delta`, `toolcall_delta`, and `toolcall_end` with the full ToolCall.
  - Tools: `tool_execution_start`, `tool_execution_update`, `tool_execution_end` (`toolCallId`, `toolName`, `args`, `result`, `isError`, `durationMs`).
  - Queue and state: `queue_update` (steering and followUp queues), `thinking_level_changed`, `session_info_changed`, `entry_appended`.
  - Compaction: `compaction_start` and `compaction_end` (reason `manual`, `threshold` or `overflow`, plus `tokensBefore` and `estimatedTokensAfter`).
  - Retries: `auto_retry_start` and `auto_retry_end` (`attempt`, `maxAttempts`, `delayMs`, `errorMessage`, `finalError`).
  - [docs/json.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/json.md)
- **"Done" means `agent_settled`, not `agent_end`.** `agent_end` can be followed by retry, overflow recovery, compaction, steering or follow-up work. — [docs/json.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/json.md)
- **Token and cost telemetry.** Each `message_update` carries a cumulative `usage` object: `{input, output, cacheRead, cacheWrite, totalTokens, cost:{input, output, cacheRead, cacheWrite, total}}`. It "can remain zero until completion when a provider does not report usage while streaming." — [docs/json.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/json.md)
- **Billed vs. estimated cost.** v1.5.0 (2026-11-02) added `Usage.cost.totalFromProvider` to tell billed totals apart from catalog estimates. — [CHANGELOG 1.5.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **RPC commands** (from the headings in rpc-commands.md):
  - Prompting: `prompt` (with optional `streamingBehavior: "steer"`), `steer`, `follow_up`, `abort`, `clear_queue`, `new_session`.
  - State and model: `get_state`, `get_messages`, `set_model`, `cycle_model`, `get_available_models`, `set_thinking_level`, `set_steering_mode` and `set_follow_up_mode` (e.g. `one-at-a-time`).
  - Compaction and retry: `compact` (with `customInstructions`), `set_auto_compaction`, `set_auto_retry`, `abort_retry`.
  - Shell: `bash` (streams `bash_execution_update`), `abort_bash`.
  - Sessions: `get_session_stats` (session file and ID, message, toolCall and toolResult counts, tokens, cost, context-window usage), `export_html`, `switch_session`, `fork`, `clone`, `get_fork_messages`, `get_entries`, `get_tree`, `get_last_assistant_text`, `set_session_name`, `get_commands`.
  - [docs/rpc-commands.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-commands.md)
- **Request/response correlation.** Commands take an optional `id` that the response echoes. A successful `prompt` response returns `data.disposition` (`started`, `queued` or `handled`), which means "accepted", not "completed". — [docs/rpc.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md)
- **TypeScript client.** `RpcClient` (exported) spawns Pi and correlates responses. `promptAndWait()` subscribes before sending, which avoids a race with fast completions. — [docs/cli-integration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli-integration.md)
- **Extension dialogs over RPC.** `select`, `confirm`, `input` and `editor` become `extension_ui_request` records and block until the client answers with `extension_ui_response`. A dialog with a `timeout` resolves itself to a default when it expires. `custom()` returns undefined in RPC. — [docs/rpc-extension-ui.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-extension-ui.md)
- **Package layout.** pi-agent-core is `@earendil-works/pi-agent-core` ("Agent runtime with tool calling and state management") and the LLM layer is `@earendil-works/pi-ai`. A new **experimental** `@earendil-works/pi-durable` commits "conversations, model turns, tool calls ... to storage before anything is shown. If the process dies mid-turn, reopening the storage picks the work up." — [README](https://github.com/earendil-works/pi/blob/main/README.md), [packages/durable/README.md](https://github.com/earendil-works/pi/tree/main/packages/durable)

### Inferences
- For a Gas Town-style supervisor, RPC mode is the best fit. It runs one long-lived process per worktree, needs no TUI scraping, can deliver mid-run nudges through `steer` and `follow_up`, and offers cost polling through `get_session_stats`. JSON mode is a good choice for fire-and-forget one-shot jobs, but the supervisor must parse `stopReason` itself rather than trust the exit code.
- pi-durable could make crash recovery much simpler, but it is experimental and its API is unstable.

### Gaps
- I did not run Pi, so I have no measured event volume or RPC latency.
- I did not verify a turn-count or max-steps limit in the CLI. A grep for `maxTurns` found nothing, so it appears not to exist. A supervisor would have to enforce limits by watching events and sending `abort`, or by writing an extension.

## Unattended operation: permissions, sandboxing, tool restriction, timeouts

### Takeaway
Pi is YOLO by design: "it does not ask for approval before every tool call," and it has no built-in sandbox. Isolation is the operator's job (container, VM, Docker Sandboxes, OpenShell, or the Gondolin extension). Tools can be restricted with `--tools`, `--exclude-tools` or `--no-tools`, and with `tool_call` extension hooks that can block or rewrite calls. The bash tool has **no default timeout**.

### Cited Findings
- "Pi can read, change, and execute files with the permissions of the account that started it, and it does not ask for approval before every tool call." It recommends running entirely in a container or VM as "usually the strongest practical option". Its security policy treats "lack of a built-in sandbox" as outside the security boundary. — [docs/security.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)
- **Project trust** gates loading of `.pi/settings.json`, `.pi/mcp.json`, `.pi/extensions`, skills, prompts, `.pi/SYSTEM.md` and project `.agents/skills`.
  - Print, JSON and RPC modes cannot prompt. With the default `defaultProjectTrust: "ask"`, protected project resources are **skipped** unless you pass `--approve`, `--no-approve`, `"always"`, or an extension decides.
  - AGENTS.md and CLAUDE.md load **regardless** of trust.
  - [docs/security.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)
- **Isolation guides** cover plain Docker, Docker Sandboxes, OpenShell, and "Route tools through Gondolin" (an extension that runs only the tools inside the boundary). — [docs/containerization.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/containerization.md)
- **Tool restriction:**
  - `--tools read,grep,find,ls` replaces the selection, and `+name`/`-name` adjusts it.
  - `--exclude-tools` disables tools by pattern, and `--no-tools` / `--no-builtin-tools` remove them.
  - `--tools` does not remove MCP tools unless an entry starts with `mcp__`.
  - [docs/cli.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md)
- **Extension `tool_call` hook.** It "can block or mutate input" (`return { block: true, reason }`). Blocked calls still emit `tool_execution_end` with an error. — [docs/extensions.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md)
- **Shipped example guards:** `permission-gate.ts` (confirms dangerous bash such as `rm -rf` and `sudo`), `protected-paths.ts` (blocks writes to `.env`, `.git/`, `node_modules/`), `dirty-repo-guard.ts`, `git-checkpoint.ts`, `auto-commit-on-exit.ts`, `plan-mode`. — [examples/extensions/README.md](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/examples/extensions)
- **Timeouts:**
  - Bash tool schema: `timeout: "Timeout in seconds (optional, no default timeout)"`, with output truncated to the last N lines or KB and the full output saved to a temp file. — [src/core/tools/bash.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/bash.ts)
  - Provider request timeout: `retry.provider.timeoutMs`, defaulting to `httpIdleTimeoutMs`.
  - Retry: `retry.maxRetries` 3, `baseDelayMs` 2000, `maxAgentDelayMs` 60000.
  - [docs/settings.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/settings.md)
- v1.5.0 fixed `pi.exec()` hanging when a child ignored SIGTERM (it now sends SIGKILL after 5 s), and aborted bash on macOS sometimes running the next command in a `a; b` list. — [CHANGELOG 1.5.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **Network quieting:** `PI_OFFLINE` disables automatic network activity, including model-catalog refreshes. `PI_SKIP_VERSION_CHECK` disables the pi.dev version request. — [docs/environment-variables.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/environment-variables.md)

### Inferences
- Running one worktree per container fits Pi's model. The supervisor must provide the sandbox and per-command and wall-clock timeouts, because the model can launch a long-running or hanging bash command with no timeout. A small extension that injects a default bash timeout, or the `tool_call` hook rewriting `timeout`, would close that gap.
- Untrusted repos cannot load project extensions headless by default, which is a safe default for an orchestrator. Pass `--approve` deliberately when the supervisor's own `.pi/extensions` hooks need to load. Gas Town and Gas City pass `-e` explicitly instead (see below).

### Gaps
- I did not test whether the `bash` tool honors a default timeout set from settings; I found no such setting.

## Default tools, system prompt size, edit tool mechanics

### Takeaway
There are four default tools: `read`, `bash`, `edit` and `write`. `grep`, `find`, `ls` and `powershell` are available, and `codemode`/`tool_search` are inactive built-ins. I reconstructed the default system prompt from source: about 2,190 characters, about 280 words, roughly 550 tokens. Adding the four tool schemas probably puts the total near 1,000 tokens, consistent with a third-party "under 1,000 tokens" claim. The edit tool does exact, unique, non-overlapping multi-edit replacement, with a limited fuzzy fallback for whitespace and Unicode punctuation.

### Cited Findings
- **Default tools.** "Tools to include in prompt. Default: [read, bash, edit, write]." The full built-in set is `read`, `bash`, `powershell`, `edit`, `write`, `grep`, `find`, `ls`, plus `codemode` and `tool_search`, which are registered inactive. — [src/core/system-prompt.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/system-prompt.ts), [docs/settings.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/settings.md)
- **Prompt structure, verbatim from source:**
  - Preamble: "You are a coding assistant running inside pi, a coding agent harness. Help users inspect files, run commands, and modify code."
  - `<tools>`: one-line snippets for each tool, then "Additional tools may be available."
  - `<rules>`: per-tool guidelines such as "Use read to examine files instead of cat or sed.", "Each edits[].oldText must exactly and uniquely match the original file...", "You can inspect PI_* environment variables...", "Be concise in your responses", "Show file paths clearly when working with files".
  - `<docs>`: pointers to Pi's own docs.
  - Optional `<addendum>` (APPEND_SYSTEM), `<project_context>` (AGENTS.md etc. in `<project_instructions path=...>`), `<skills>`, and `<cwd>`.
  - No date, git status or OS info appears in the default sections.
  - [src/core/system-prompt.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/system-prompt.ts), [tool snippets in src/core/tools/*.ts](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/src/core/tools)
- **My size count (checked).** I reconstructed the default prompt with the four default tools, no context files and no skills, using a typical global-install docs path. The result is 2,191 characters and 279 words, about 547 tokens at chars/4. This excludes tool JSON schemas and descriptions. The four tool descriptions plus parameter descriptions add an estimated 1.5 to 2k characters, about 400 to 500 tokens (rough estimate). v1.5.0 changelog: "Tightened system prompt". — reconstruction from [system-prompt.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/system-prompt.ts)
- **Third-party size claim:** "System prompt plus tool definitions: under 1,000 tokens." — [eesel.ai](https://www.eesel.ai/blog/pi-coding-agent) (third-party, consistent with my count)
- **Edit schema.** `{path, edits:[{oldText, newText}]}`. Each `oldText` "must be unique in the original file and must not overlap"; edits are "matched against the original file, not incrementally." The tool also accepts a legacy single `oldText`/`newText`. — [src/core/tools/edit.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/edit.ts)
- **Matching:** "Find oldText in content, trying exact match first, then fuzzy match." The fuzzy step normalizes NFKC, trailing whitespace per line, smart quotes, Unicode dashes and special spaces. It does **not** normalize indentation or leading whitespace.
- **Edit errors returned to the model:**
  - "Could not find edits[i] in path. The oldText must match exactly including all whitespace and newlines."
  - "Found N occurrences ... must be unique. Please provide more context."
  - Overlapping edits are rejected.
  - [src/core/tools/edit-diff.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/edit-diff.ts)
- **Write serialization.** Edits go through a `withFileMutationQueue`, which serializes concurrent writes to a file. Sibling tool calls run in parallel by default after sequential preflight. — [edit.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/edit.ts), [docs/extensions.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md)
- **Tool argument validation.** Since v1.5.0, validation uses `@earendil-works/pi-schema`, which coerces numeric strings and wraps single values into arrays for all tools, MCP included. Errors read like `- limit: expected number, got string`. — [CHANGELOG 1.5.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **Read limits.** The read tool truncates at DEFAULT_MAX_LINES or DEFAULT_MAX_BYTES and tells the model to page with `offset`/`limit`. It supports images. — [read.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/read.ts)

### Inferences
- A small prompt plus a multi-edit tool with argument coercion is a good fit for 32B to 70B open-weight models. It means short prefill, and type coercion absorbs common sloppy tool-call arguments such as stringified numbers.
- Because `edits` is an array of objects, models with weak nested-array tool-calling could struggle. The legacy single `oldText`/`newText` form is still accepted.

### Gaps
- I did not run a tokenizer on the full request (system prompt plus tool schemas). The token figures are character-based estimates.

## Provider support: OpenRouter, custom OpenAI-compatible endpoints, reasoning fields, local-model quirks

### Takeaway
Provider support is broad and mature. OpenRouter is built in, including routing preferences and session-affinity headers. vLLM, SGLang, Ollama and LM Studio are configured in `~/.pi/agent/models.json`, using `api: "openai-completions"` and an extensive `compat` block. llama.cpp is a first-class built-in integration with router management. Reasoning formats can be selected for each server family. The changelog shows a steady stream of provider-quirk fixes.

### Cited Findings
- "For Ollama, LM Studio, vLLM, SGLang, and other compatible servers, configure a compatible endpoint in `models.json`." — [docs/models.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md)
  - Example fields: `{providers:{ollama:{baseUrl, api:"openai-completions", apiKey, models:[{id}]}}}`.
  - `apiKey` and headers accept `$ENV`, literal values, or `!command` (run at request time).
  - `modelOverrides` patches built-in models. Since v0.73.1 the file accepts JSONC (comments and trailing commas).
- **Supported API adapters:** `openai-completions`, `openai-responses`, `anthropic-messages`, `bedrock-converse-stream`, `mistral-conversations`, plus a classifier API. Per-model `samplingParams` are merged into the request body for OpenAI-compatible APIs. — [docs/models-configuration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models-configuration.md)
- **`openai-completions` compat flags** (most are auto-detected from the URL):
  - Request shape: `supportsDeveloperRole`, `supportsReasoningEffort`, `supportsUsageInStreaming`, `supportsFinishReason` (when false, Pi infers stop/toolUse), `maxTokensField`.
  - Message-ordering quirks: `requiresToolResultName`, `requiresAssistantAfterToolResult`, `requiresThinkingAsText`, `requiresReasoningContentOnAssistantMessages`.
  - Reasoning control: `thinkingFormat` (`openai`, `openrouter`, `deepseek`, `together`, `baseten`, `zai`, `qwen`, `chat-template`, `qwen-chat-template`, `string-thinking`, `ant-ling`), `chatTemplateKwargs`, and `thinkingTokenBudgetField` (`thinking_token_budget` for vLLM, `thinking_budget` for Qwen/SGLang, `thinking_budget_tokens` for llama.cpp).
  - Tool definitions and system messages: `supportsStrictMode`, `supportsOpenAIGrammarTools`, `supportsMidConvoSystemMessages`.
  - Routing and caching: `openRouterRouting`, `sendSessionAffinityHeaders` (default true for OpenRouter), `vllmPriority`, `cacheControlFormat`.
  - [docs/models-configuration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models-configuration.md)
- **llama.cpp.** "Pi integrates directly with the llama.cpp router... Pi's `/llama` command manages the router." There are dedicated docs for llama.cpp and NVIDIA RTX llama.cpp. — [docs/models.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md), [docs/llama-cpp.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/llama-cpp.md)
- **Recent local and open-model quirk fixes (CHANGELOG):**
  - OpenAI Responses streams from servers that omit `output_index`, "such as llama.cpp", ran mixed-up tool calls. These streams now **end with an error** (#9974, v0.99.x).
  - llama.cpp models with `enable_thinking` chat templates ignored Pi's thinking level (#9528).
  - Qwen3.8-flash thinking was replayed as plain text (#10047).
  - GLM requests failed with "Expected at most one leading ThinkChunk" (#9674).
  - `--model provider/id` failed for llama.cpp provider IDs containing slashes (#11394, v1.4/1.5).
  - [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- Since v1.0.0, `--provider` without `--model` fails with an error instead of silently running another provider's default model (#10236). — [CHANGELOG 1.0.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- Gas City's built-in Pi profile ships curated model choices such as `--provider ollama-cloud --model gpt-oss:20b`. — [gascity internal/worker/builtin/profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go)
- **Third-party:** a Hacker News user said Pi was the only local-model setup that worked well for them, because its small system prompt avoids long prefill on a laptop. — [eesel.ai summary](https://www.eesel.ai/blog/pi-coding-agent) (secondhand; I did not find the original comment)

### Inferences
- With vLLM or SGLang serving Qwen3-Coder, GLM, DeepSeek or gpt-oss, expect to set `api: "openai-completions"`, `thinkingFormat` (e.g. `qwen-chat-template` or `chat-template`), and possibly `requiresReasoningContentOnAssistantMessages` or `requiresThinkingAsText`, depending on the server's chat template. Auto-detection is URL-based, so a self-hosted URL on an IP or custom host will probably fall back to generic defaults. Explicit `compat` settings are advisable.
- Prefer `openai-completions` over `openai-responses` for llama.cpp, because of the `output_index` issue.

### Gaps
- I found no Pi-maintained compatibility matrix or benchmarks for specific 32B to 70B open-weight models.
- I did not inspect the open issues list (GitHub API access was blocked in this session). Coverage of current local-model bugs is therefore limited to fixed items in the CHANGELOG.

## Context management: compaction, sessions, AGENTS.md, SYSTEM.md

### Takeaway
Auto-compaction is on by default. It triggers when context exceeds the window minus 16,384 tokens and keeps about 20,000 recent tokens. Sessions are JSONL trees (header version 3) that support fork, clone, branching and resume. AGENTS.md and CLAUDE.md are loaded; SYSTEM.md replaces the prompt and APPEND_SYSTEM.md appends to it.

### Cited Findings
- **Compaction defaults:** `compaction.enabled` true, `reserveTokens` 16384, `keepRecentTokens` 20000, with per-model overrides in `compaction.modelOverrides`. — [docs/settings.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/settings.md)
- **Compaction behavior.**
  - Trigger: `contextTokens > contextWindow - reserveTokens`, checked between turns after tool results, and before a new prompt.
  - Overflow recovery: a provider overflow error or an early `length` stop can trigger one compact-and-retry.
  - Branch summarization exists for `/tree` navigation, and file operations are tracked cumulatively.
  - [docs/compaction.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/compaction.md)
- **Session control flags and commands:** `--no-session`, `--session-dir`, `PI_CODING_AGENT_SESSION_DIR`, `sessionDir`. RPC has `fork`, `clone`, `get_tree`, `switch_session`, and `new_session` with `parentSession`. — [docs/settings.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/settings.md), [docs/rpc-commands.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-commands.md)
- **Context files:** `AGENTS.override.md`, `AGENTS.md`, or `CLAUDE.md` from the agent directory and project directories. `--no-context-files` / `-nc` disables them. `<agent-dir>/SYSTEM.md` or `.pi/SYSTEM.md` replaces the default prompt; `APPEND_SYSTEM.md` adds to it. A trusted project file takes precedence. The CLI equivalents are `--system-prompt` and `--append-system-prompt`. — [docs/configuration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/configuration.md), [docs/cli.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md)
- Since v0.73.x, context files are wrapped in XML tags (`<project_instructions path=...>`) instead of Markdown headings, to reduce boundary confusion (#4541). — [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)

### Inferences
- For workers with a 32k to 64k context, the 16k reserve plus 20k kept tail is a large fraction of the window. Tune both through `compaction.modelOverrides`.
- Gas City already uses Pi's `session_compact` event to trigger its own handoff (see the maintenance section).

### Gaps
- I did not verify the AGENTS.md lookup order (ancestor walk vs. cwd only) in source.

## Extensibility: extensions, custom tools, skills, subagents, MCP

### Takeaway
Pi is extension-first. TypeScript/JS extensions can subscribe to lifecycle events, block or mutate tool calls, register tools, commands, providers and virtual models, and rewrite the system prompt. There are skills, prompt templates and an npm package ecosystem. Subagents are deliberately **not built in**; they ship as an example extension. MCP was long refused, but it became a built-in extension in v0.99.0 (2026-09-29), together with "codemode" (a QuickJS JS sandbox) and `tool_search`.

### Cited Findings
- **Extension API.**
  - Events include `session_start`, `before_agent_start` (can replace the system prompt), `tool_call` (block or mutate), `tool_result`, `tool_execution_end`, `session_compact`, `project_trust`, and provider header hooks.
  - Registration: `pi.registerTool`, commands, `pi.registerVirtualModel`.
  - Tool features: `exposure` (`direct`, `model-only`, `codemode`, `deferred`, `hidden`), `ctx.executeTool()` for nested calls.
  - [docs/extensions.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/extensions.md), [CHANGELOG 0.99.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **v0.99.0 built-ins.** "Added codemode, tool search, and MCP support as built-in extensions. The `codemode` tool runs model-written JavaScript in a QuickJS sandbox that calls pi's tools." MCP supports stdio and streamable HTTP, with OAuth. `--no-extensions` also disables these built-ins. — [CHANGELOG 0.99.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md), [docs/mcp.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md)
- **Codemode token savings (v1.0.0).** About 40% fewer prompt tokens: "a GPT-5.6 request shrinks from about 5,300 to 3,300 tokens" with default tools plus codemode. — [CHANGELOG 1.0.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **Subagents** exist only as `examples/extensions/subagent/` (agents, prompts, `index.ts`), with changelog fixes to that example through v0.84.x. In v1.4.0, codemode workers queue for "in-process subagents". — [examples/extensions/subagent](https://github.com/earendil-works/pi/tree/main/packages/coding-agent/examples/extensions/subagent), [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **Ecosystem size (third-party):** 11,394 npm packages tagged for Pi, and 5,553 in the package catalog. — [eesel.ai](https://www.eesel.ai/blog/pi-coding-agent)
- **Mario's earlier MCP figures (third-party summary):** Playwright MCP is about 21 tools and 13.7k tokens; Chrome DevTools MCP is 26 tools and 18k tokens. These were his rationale for the earlier "no MCP" stance. — [eesel.ai](https://www.eesel.ai/blog/pi-coding-agent)

### Inferences
- The lack of built-in subagents suits an external orchestrator: the supervisor owns fan-out. Note that the MCP and codemode built-ins are on by default as extensions. Disable them with `-ne` or config for a minimal and deterministic worker, and re-enable selected ones with `-e builtin:<name>`.

### Gaps
- I did not count the full extension event list, which is long.

## Resource overhead and startup time

### Takeaway
Pi requires Node.js 22.19 or newer. I found no official startup-time or memory benchmarks. Gas Town and Gas City hard-code an **8 s readiness delay** for Pi's TUI under tmux. Headless modes avoid the TUI, but I found no measured numbers for them.

### Cited Findings
- "Pi requires Node.js 22.19 or newer." Pi also runs under Bun (there is a `src/bun` folder). — [README](https://github.com/earendil-works/pi/blob/main/README.md), [package.json engines](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/package.json)
- **Gas Town's Pi preset:** "Pi's Node.js TUI takes several seconds to initialize before it can receive tmux input... `ReadyDelayMs: 8000`". `ProcessNames` is `pi`, `node`, `bun`. — [gastown internal/config/agents.go](https://github.com/gastownhall/gastown/blob/main/internal/config/agents.go)
- **Past startup work in the CHANGELOG:**
  - Provider SDKs are lazy-loaded on first use, for faster startup (#2297).
  - The version check runs in parallel instead of blocking startup for up to 1 s.
  - Skipping `node_modules` in skill scans fixed about 60 ms of delay.
  - v1.0.0 cut per-message transcript memory to about one fifth.
  - v1.5.0 fixed CPU and memory blowup from re-parsing large streamed tool calls on every delta (#9265).
  - [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- The eesel.ai article reports no startup-time or memory figures. — [eesel.ai](https://www.eesel.ai/blog/pi-coding-agent)

### Inferences
- Expect a per-worker footprint typical of a Node process (likely tens to low hundreds of MB RSS). This is unverified, so measure it. For headless use, set `PI_OFFLINE=1` and `PI_SKIP_VERSION_CHECK=1` to avoid network calls at startup.

### Gaps
- I have no measured startup latency, RSS, or CPU per idle worker.

## Maintenance, governance, adoption, orchestrator integration

### Takeaway
Pi is very actively maintained: releases every few days, 1.0 on 2026-10-01, and 1.5.0 on 2026-11-02. It is MIT-licensed and now owned by Earendil Inc. (Armin Ronacher's company), with Mario Zechner still the dominant committer. Pi is the engine under OpenClaw. **Both Gas Town and Gas City ship built-in Pi presets** that use extension-based hooks.

### Cited Findings
- **Releases (CHANGELOG):** v1.0.0 2026-10-01, 1.0.1 to 1.0.4 2026-10-03 to 10-05, 1.1.0 10-07, 1.2.0 10-12, 1.3.0 10-15, 1.4.0 10-21, 1.5.0 11-02. Before that came 0.99.0 on 09-29 and 0.80.x to 0.87.x from June to September. — [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **License and copyright:** MIT, "Copyright (c) 2025 Mario Zechner". — [LICENSE](https://github.com/earendil-works/pi/blob/main/LICENSE)
- **Rename:** v0.74.0 (2026-05-07): "Updated repository links and package references for the move to `earendil-works/pi-mono` and `@earendil-works/*` package scopes". v0.73.1 handled the self-update migration from `@mariozechner/pi-coding-agent`. — [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- **Commit activity (my count, git log since 2026-10-01 in a depth-200 clone):** Mario Zechner 139, Petr Houška 24, Armin Ronacher 13, robopipi[bot] 11. This is partial history, so it shows the shape rather than full totals. — local clone of [earendil-works/pi](https://github.com/earendil-works/pi)
- **Monorepo packages:** pi-ai, pi-agent-core, pi-coding-agent, tui, mcp, codemode, durable, telemetry, chord, schema, evals. — [README](https://github.com/earendil-works/pi/blob/main/README.md)
- **Third-party figures** (eesel.ai, edited October 2026):
  - 112,973 stars, 14,339 forks, 296 contributors.
  - 5.2M npm downloads in a week.
  - Earendil acquired Pi on 2026-04-08.
  - Earendil's backers include the founders of n8n and OpenClaw.
  - Armin's RFC: "Pi remains MIT licensed and that will not change", but some future features may be source-available or proprietary.
  - The Pi 1.0 HN thread reached 1,686 points.
  - [eesel.ai](https://www.eesel.ai/blog/pi-coding-agent), [trendingtopics.eu](https://www.trendingtopics.eu/pi-1-0-earendil/)
  - I could not check the stars figure: the GitHub API was blocked in this session.
- **OpenClaw.** The Pi README says: "See OpenClaw for a real-world integration." Armin is quoted (third-party): "what's under the hood of OpenClaw is a little coding agent called Pi." — [README](https://github.com/earendil-works/pi/blob/main/README.md), [eesel.ai](https://www.eesel.ai/blog/pi-coding-agent)
- **Gas Town's `AgentPi` preset (checked in source):**
  - Command `pi -e .pi/extensions/gastown-hooks.js`, `SessionIDEnv: PI_SESSION_ID`.
  - `ResumeFlag: ""  // No resume support yet`, `SupportsForkSession: false`.
  - Non-interactive mode uses `-p` with `--no-session`. `PromptMode: "arg"`, `ReadyDelayMs: 8000`, `InstructionsFile` AGENTS.md.
  - The hook template lives at `internal/hooks/templates/pi/gastown-hooks.js`.
  - [gastown agents.go](https://github.com/gastownhall/gastown/blob/main/internal/config/agents.go)
- **Gas City's `pi` worker profile (checked in source):**
  - `pi -e .pi/extensions/gc-hooks.js`, `ResumeFlag: "--session"` with flag-style resume, `ReadyDelayMs: 8000`, AGENTS.md.
  - Curated model options such as Ollama Cloud gpt-oss-20b.
  - The hook file maps `session_start` → `gc prime --hook`, `session_compact` → `gc prime --hook` plus `gc handoff --auto "context cycle"`, and `before_agent_start` → `gc hook --inject` plus queued nudges and unread mail.
  - Its comment notes that "pi has no session_start hook surface that can inject into the model's context, so the SessionStart output is parked... and drained into the system prompt by the next before_agent_start." It targets the "Pi 0.70+ extension API".
  - [gascity profiles.go](https://github.com/gastownhall/gascity/blob/main/internal/worker/builtin/profiles.go), [gascity gc-hooks.js](https://github.com/gastownhall/gascity/blob/main/internal/bootstrap/packs/core/overlay/per-provider/pi/.pi/extensions/gc-hooks.js)
- Gas Town's README lists the built-in presets as claude, gemini, codex, cursor, auggie, amp, opencode, copilot, pi and omp. Its older INSTALLING.md omits pi. — [gastown README](https://github.com/gastownhall/gastown), [INSTALLING.md](https://github.com/gastownhall/gastown/blob/main/docs/INSTALLING.md)

### Inferences
- Integration risk with Gas Town and Gas City is low because presets exist. Both, however, drive Pi's **TUI through tmux** (8 s readiness delay, prompt passed as an argument) rather than RPC mode. Gas Town's preset still declares no resume support; Gas City's supports `--session`. A custom supervisor could do better by using `--mode rpc`.
- The release cadence is very fast, with breaking changes in minor versions (e.g. 1.5.0 changed tool-schema validation). Pin versions; the pi.dev installer pins dependencies and `pi update` is opt-in.

### Gaps
- I could not verify the live star count, contributor count or open-issue count.
- I found no published reports of large-scale unattended Pi fleets under Gas Town.

## Known issues and limitations relevant to unattended runs

### Takeaway
The main gaps are operational, not functional: no sandbox, no default bash timeout, no turn or step cap, a JSON-mode exit code that does not reflect model failure, fast-moving APIs, and URL-based compat auto-detection that may misfire on self-hosted endpoints.

### Cited Findings
- No built-in sandbox and no approval gating. — [docs/security.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)
- The bash tool has "no default timeout". — [bash.ts](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/tools/bash.ts)
- In JSON mode a failed or aborted response does not set a nonzero exit; in print mode a `length` stop does exit 1. — [docs/cli-integration.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli-integration.md)
- Headless modes skip untrusted project resources unless `--approve` is given, while AGENTS.md always loads, so prompt-injection exposure stays. — [docs/security.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/security.md)
- llama.cpp Responses streams that lack `output_index` now fail with an error rather than silently mixing tool calls. — [CHANGELOG](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- Past hangs fixed in 1.5.0: `pi.exec` with SIGTERM-ignoring children; `RangeError` crashes on huge command output (now capped at 256 MiB). — [CHANGELOG 1.5.0](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/CHANGELOG.md)
- RPC readers must drain stdout or Pi stalls, and must not use Node `readline`. — [docs/rpc.md](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md)

### Inferences
- A robust worker wrapper needs:
  - Container isolation per worktree.
  - `--mode rpc` with `--no-session`, or a supervisor-managed `--session-dir`.
  - Explicit `--provider` and `--model`, with `models.json` `compat` set for each self-hosted server.
  - `-ne` with only the needed `-e builtin:` extensions, plus a small extension that enforces a default bash timeout and blocks protected paths.
  - Supervisor-side caps on wall clock, tokens and cost (from `get_session_stats` or the usage in `message_end`), enforced with `abort`.
  - `PI_OFFLINE=1`.

### Gaps
- I could not search the open GitHub issues (API blocked), so there may be unreported open bugs affecting headless or local-model runs.
