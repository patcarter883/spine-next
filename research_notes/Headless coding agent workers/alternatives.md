# Alternative headless CLI coding agents (other than Pi, Goose, OpenCode) as Gas Town / Gas City workers with OpenRouter + self-hosted open-weight models

Research date: 2026-11-03. Budget-limited pass (~19 tool calls). Many items come from search-result summaries rather than full-page reads; these are marked "(search summary)" and should be re-verified. Items marked "UNVERIFIED (training knowledge)" are not sourced and are listed only in Gaps.

## Which runtimes do Gas Town / Gas City support, and how do they drive them?

### Takeaway
Gas Town drives agents as interactive TUIs in tmux sessions, with Claude Code as the default and best-integrated runtime (hooks for startup and mail injection). Built-in presets cover claude, gemini, codex, kiro, cursor, auggie, amp, opencode, copilot, pi and omp. Runtimes without hooks, such as Codex, get a typed "startup fallback". Gas City is the SDK extracted from Gas Town. Its runtime provider abstraction includes tmux, subprocess, exec, ACP and Kubernetes, so headless and ACP workers fit it more naturally than they fit Gas Town.

### Cited Findings
- Gas Town README lists built-in agent presets: `claude`, `gemini`, `codex`, `kiro`, `cursor`, `auggie`, `amp`, `opencode`, `copilot`, `pi`, `omp`. Claude Code is the default. — [gastownhall/gastown README](https://github.com/gastownhall/gastown)
- The official install docs name only three built-in runtimes (claude, gemini, codex) plus custom agent aliases. This conflicts with the README's longer list, and the README is probably newer. — [Installing Gas Town](https://docs.gastownhall.ai/installing/) (search summary)
- tmux is "Required for `gt up` and the tmux-backed roles (Mayor, Witnesses, Refineries, polecats)". It is optional only in minimal mode, where you run runtime instances yourself. — [gastown README](https://github.com/gastownhall/gastown)
- Claude uses hooks in `.claude/settings.json` (passed with `--settings`) for mail injection and startup. Copilot uses lifecycle hooks in `.github/hooks/gastown.json`. "For runtimes without hooks (e.g., Codex), Gas Town sends a startup fallback after the session is ready." The fallback runs `gt prime`, optionally `gt mail check --inject`, and `gt nudge deacon session-started`. Copilot uses a 5-second ready delay instead of prompt detection. — [gastown README](https://github.com/gastownhall/gastown)
- Codex picks up role instructions only if you set `project_doc_fallback_filenames = ["CLAUDE.md"]` in `~/.codex/config.toml`. The Kiro preset launches `kiro-cli chat --trust-all-tools`. Copilot uses `--yolo`. — [gastown README](https://github.com/gastownhall/gastown)
- Custom agents can be set three ways:
  - globally: `gt config agent set claude-glm "claude-glm --model glm-4"`
  - per rig: a `runtime` entry in `settings/config.json` with `provider`, `command`, `args`, `prompt_mode` (the Codex example uses `"none"`)
  - per command: `gt sling <bead> <rig> --agent cursor`

  — [gastown README](https://github.com/gastownhall/gastown)
- A "New runtime?" discussion exists on the Gas Town repo (contents not read). — [Discussion #606](https://github.com/gastownhall/gastown/discussions/606)
- Gas City runtime providers: tmux, subprocess, exec, ACP, Kubernetes, behind a Provider interface in `internal/runtime/` (start/stop/status/metadata). Each role is a folder holding an `agent.toml` (provider, pool, timeouts) plus a prompt template. The controller reconciles `city.toml`. — search summary of [gascity mirrors](https://github-users.yhajoa.workers.dev/gastownhall/gascity/blob/main/engdocs/architecture/controller.md) and [docs.gascity.com](https://docs.gascity.com/getting-started/coming-from-gastown)
- Gas City places skills for Claude (`.claude/skills/`), Codex (`.agents/skills/`), Gemini (`.gemini/skills/`) and OpenCode (`.opencode/skills/`). "Providers whose convention isn't confirmed (copilot, cursor, pi, omp) are skipped for now." Agents "typically pull new mail into context each turn via a hook". `gc session nudge <agent> "msg"` types text into a running agent. — [Gas City capabilities guide](https://docs.gascity.com/guides/capabilities-for-coding-agent-users.md)
- Dates conflict: a March 2026 Gas Town survey describes Gas City as planned, while another source says it was announced on 2026-04-24. — search summary ([gastown.dev survey](https://gastown.dev/docs/research/w-gc-004-agent-framework-survey))
- Gas Town was open-sourced in January 2026 under the MIT license. — search summary ([Daniel Vaughan blog](https://codex.danielvaughan.com/2026/04/08/gas-town-multi-agent-factory/))

### Inferences
- Gas Town's tmux and nudge model favors agents with a solid interactive TUI that accepts typed input and has some hook or startup mechanism. Claude Code is best, Copilot/Codex/Gemini/OpenCode/Pi are viable, and Kiro is partial. Pure "exec" agents such as mini-swe-agent, `codex exec`, `droid exec` or `cline --json` do not fit Gas Town's polecat model directly. They fit Gas City's exec, subprocess or ACP providers.
- Among the candidates in scope, only Codex, Gemini, Amp, Cursor, Copilot, Kiro and Auggie have official presets. Codex is the only fully open, self-hostable-model-capable non-Claude preset besides the excluded Pi and OpenCode. Gemini CLI is Apache-2.0, but it is tied to the Gemini API unless a fork is used.
- An ACP-speaking agent (Kimi Code CLI, and reportedly Gemini CLI, Goose, OpenCode and others) could plug into Gas City's ACP provider without tmux scraping.

### Gaps
- I did not read the Gas City config reference, so I could not see how `provider = "acp"` or `"exec"` is configured, or which agents are tested with it.
- I could not confirm whether Qwen Code, Crush, OpenHands or Cline are known to be used as Gas Town runtimes via custom aliases. No reports found.

## Candidate-by-candidate: license, BYOK, headless/structured output, permissions, maintenance

### Takeaway
Two candidates stand out as fully open, BYOK-friendly and well suited to headless use:
- **OpenAI Codex CLI** (Apache-2.0, Rust): `exec --json`, OS-level sandbox, an official Gas Town preset. Since February 2026 it only speaks the Responses API, which is a real obstacle for vLLM/SGLang/llama.cpp backends.
- **Qwen Code** (Gemini CLI fork): native OpenAI-compatible support, `--output-format stream-json`, and an SDK/stdin stream mode.

Three more are strong as headless workers but lack Gas Town presets: OpenHands CLI/SDK (`--json` JSONL, LiteLLM, auto-approve in headless mode), Cline CLI 2.0 (`-y`/`--json`) and mini-swe-agent (bash-only, no tool-calling API, robust with weak tool parsers).

Several are weaker or closed. Factory Droid BYOK is CLI-only and reportedly broken in `droid exec`. Crush is FSL (source-available) and its headless story is unclear. Aider's releases stalled after February 2026. Kimi's legacy Python CLI is archived in favor of Kimi Code CLI.

### Cited Findings

**OpenAI Codex CLI**
- `codex exec PROMPT` is the headless path. The `exec/` crate is described as a "headless" CLI for automation. The sandbox is chosen with `sandbox_mode = "workspace-write"` and similar values. — search summary ([forge mirror of codex-rs docs](https://forge.lthn.ai/core/core-agent-ide/src/commit/cc58f1086dbe7ee5ecbf1ff1ef76f05852bc7cf5/codex-rs/config.md))
- Codex has a built-in `oss` provider for Ollama and LM Studio (equivalent to `-c model_provider="oss"`), which validates that the local server is running. One integration guide shows `codex exec --json --config 'model_provider="ollama"' --config 'model="gpt-oss:20b"'`, tested on codex-cli 0.116.0 and Ollama 0.18.2. — search summary ([hex.pm cli_subprocess_core guide](https://preview.hex.pm/preview/cli_subprocess_core/show/guides/developer-guide-codex-backends.md))
- **Important (dated, verify):** Chat Completions support (`wire_api = "chat"`) was deprecated in December 2025 and reportedly removed in February 2026. Configs that set it now fail hard, and `responses` is the only supported value. Provider IDs `openai`, `ollama` and `lmstudio` are reserved. — [openai/codex Discussion #7782](https://github.com/openai/codex/discussions/7782); [DEV: Codex CLI dropped chat-wire](https://dev.to/creeta/codex-cli-dropped-chat-wire-opencodex-picks-up-the-routing-2emb); [OpenRouter Codex tutorial](https://openrouter.ai/blog/tutorials/codex-cli-openrouter/)
- Workarounds are Responses-to-Chat translating proxies (codex-proxy, OpenCodex, Opper router). One report says LM Studio's `/v1/responses` worked non-streaming via curl but failed under Codex streaming. — [codex-proxy](https://github.com/lininn/codex-proxy); [Opper codex router](https://opper.ai/blog/codex-router) (search summary)
- Codex CLI with GPT-5.5 is shown at #4 (82.2% ±2.2) on a Snorkel-hosted Terminal-Bench 2.0 snapshot, behind NexAU-AHE, LemonHarness and Capy. The snapshot is about 32 days old. — [Snorkel TB2 leaderboard](https://snorkel.ai/leaderboard/terminal-bench-2-0/) (search summary)

**Qwen Code (QwenLM/qwen-code, Gemini CLI fork)**
- Headless mode exists for scripts and CI. `--output-format stream-json` emits NDJSON with a `type` field per line (`assistant`, `result` with usage stats). `--include-partial-messages` adds partial assistant messages. `--input-format stream-json` gives bidirectional SDK use and requires stream-json output. Setting `output.format: "stream-json"` in settings silently forces headless mode. — [Qwen Code headless docs](https://qwenlm.github.io/qwen-code-docs/en/features/headless); [headless.md in repo](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/headless.md); [PR #8966](https://github.com/QwenLM/qwen-code/pull/8966)
- Harness-variable data point from the Qwen3-Coder-Next tech report (80B-A3B, Terminal-Bench 2.0): Terminus2-json 36.2, Terminus2-xml 34.2, Claude Code 30.9, Qwen Code 25.8. The model vendor's own CLI scored lowest. — [Qwen3-Coder-Next Technical Report, arXiv 2603.00729](https://arxiv.org/html/2603.00729v1) (search summary)

**OpenHands CLI and Software Agent SDK**
- Headless mode runs without the TUI for CI, scripting and batch jobs, taking a task via `-t` or `-f`. It always auto-approves all actions (`--llm-approve` is unavailable). `--json` emits JSONL, one event per line (action and observation records). — [OpenHands CLI headless docs](https://docs.openhands.dev/openhands/usage/cli/headless)
- Community reports (unofficial):
  - Non-JSON status lines also appear on stdout, so parse only lines starting with `{`.
  - `LLM_*` env vars are read only with `--override-with-envs`.
  - The CLI can hang on first run without a settings file.

  — [Hermes agent OpenHands skill page](https://hermes-agent.nousresearch.com/docs/zh-Hans/user-guide/skills/optional/autonomous-ai-agents/autonomous-ai-agents-openhands)
- The OpenHands Software Agent SDK (MLSys 2026 paper) is a redesign of the agent core with local-first execution that can move to sandboxed or remote setups. It routes models via LiteLLM, which reaches 100+ providers including Ollama and llama.cpp. — [MLSys 2026 paper](https://proceedings.mlsys.org/paper_files/paper/2026/hash/8ae9cf363ea625161f885b798c1f1f78-Abstract-Conference.html); [Bhakthan summary](https://bhakthan.substack.com/p/the-openhands-software-agent-sdk)
- Third-party reviews recommend models of roughly 32B or larger for usable local results. — [DEV review](https://dev.to/jovan_chan_9500711396d4e6/openhands-review-2026-the-76k-star-coding-agent-5pa) (third-party claim)

**mini-swe-agent (SWE-agent team)**
- The agent class is about 100 lines of Python. Its only tool is bash, and it does not use the LM tool-calling interface. Models are reached via LiteLLM, OpenRouter, Portkey and others. It is now at v2 (PyPI 2.2.8) and reports more than 74% on SWE-bench Verified (Gemini 3 Pro). It is the reference harness for the SWE-bench "bash only" leaderboard. — [mini-swe-agent GitHub](https://github.com/SWE-agent/mini-swe-agent); [PyPI](https://pypi.org/project/mini-swe-agent/); [LiteLLM docs](https://docs.litellm.ai/docs/projects/mini-swe-agent.md)

**Cline CLI 2.0**
- Released around February 2026. `cline -y "task"` (YOLO, auto-approves all writes and commands) and `cline --json "task"` both force headless mode. Piped stdin also triggers headless mode. Requires Node 20 or newer. Per npm, `--yolo` enables `submit_and_exit` and disables spawn/team tools. The launch promoted free Kimi K2.5 and MiniMax M2.5. — [Cline CLI 2.0 blog](https://cline.bot/blog/introducing-cline-cli-2-0); [Cline docs](https://docs.cline.bot/cline-cli/getting-started); [npm cline](https://www.npmjs.com/package/cline)
- A third-party orchestrator found that `--yolo` forces headless mode and breaks TUI launches, and suggests `--auto-approve true` for an interactive bypass-permissions mode. This is relevant to a tmux-driven Gas Town use. — [agent-orchestrator issue #4944](https://github.com/OrchestratorInc/agent-orchestrator/issues/4944)

**Factory Droid**
- BYOK: `customModels` in `~/.factory/settings.json`, and OpenRouter (`https://openrouter.ai/api/v1`) is supported. Custom models are CLI-only. "Only Anthropic and OpenAI models accessed via their official APIs are fully tested and benchmarked." — [Factory BYOK docs](https://docs.factory.ai/model-independence/byok)
- `droid exec` headless mode offers text, JSON and stream-jsonrpc output. — search summary ([innfactory](https://innfactory.ai/en/ai-harness/droid/))
- A GitHub issue reports that subagents fail on every BYOK model, and links ticket FAC-12288, "Droid Exec headless mode doesn't support custom models". Current status unknown. — [Factory-AI/factory issue #691 (mirror)](https://gh.r-mj.com/Factory-AI/factory/issues/691)
- Droid is proprietary (closed source). — inference from the vendor product. A license page was not checked.

**Charm Crush**
- License is FSL-1.1-MIT, source-available with a non-compete clause; each release converts to MIT after two years. Custom OpenAI- and Anthropic-compatible providers are declared via JSON config with `"type": "openai"`. — [Crush README mirror](https://git.secluded.site/crush-mirror/blob/test/README.md); [rywalker research](https://rywalker.com/research/crush)
- I found no documentation of a headless `crush run` command's flags or structured output. One third-party source mentions a headless REST API. — search summary

**Aider**
- The last PyPI release is v0.86.2 (2026-02-12). A tracker shows a repo push on 2026-05-22. A community fork, `aider-ce`, is at 0.88.26 on PyPI, but its status (official or independent) is unconfirmed. — [PyPI paul-gauthier](https://pypi.org/user/paul-gauthier/); [aider-ce PyPI](https://pypi.org/project/aider-ce/0.88.26/); [releasealert](https://releasealert.dev/github/Aider-AI/aider)

**Kimi CLI / Kimi Code CLI (Moonshot)**
- The legacy Python `MoonshotAI/kimi-cli` (Apache-2.0) is archived as "no longer maintained". Its successor is Kimi Code CLI (`MoonshotAI/kimi-code`, a TypeScript rewrite, reportedly MIT). `kimi acp` starts a multi-session ACP agent server. — [kimi-cli GitHub](https://github.com/MoonshotAI/kimi-cli); [kimi acp docs](https://www.kimi.com/code/docs/kimi-code-cli/reference/kimi-acp.html); [Decodo guide](https://decodo.com/blog/kimi-code-cli)
- Kimi K3 is reported at 88.3% on Terminal-Bench 2.1 using the Kimi Code harness. — search summary of third-party blogs, unverified

### Inferences
- **Codex CLI is the strongest open alternative for Gas Town specifically.** It has an official preset, is Apache-2.0, has strong sandboxing, offers `exec --json` and the app-server, and the harness is top-tier on TB2. Its weak point for this use case is Responses-API-only. OpenRouter supports Responses, but vLLM/SGLang/llama.cpp Responses endpoints and their streaming tool calls are less battle-tested than Chat Completions, so expect to need a proxy or careful version pinning. Codex's `apply_patch` freeform format was tuned for GPT models. With 32B-70B open models, expect more malformed patches. This is an inference; I found no measured data.
- **Qwen Code is the most natural fit for Qwen3-Coder and Qwen3.x local models.** It is OpenAI-compatible native and has Claude-Code-like stream-json. The Qwen tech report nonetheless shows it scoring below Terminus-2 and Claude Code with the same model, so the harness matters.
- **For "exec"-style workers under Gas City,** OpenHands (`--json`), Cline (`--json -y`), Qwen Code (stream-json) and mini-swe-agent are all viable. mini-swe-agent is the most robust option when a serving stack's tool-call parser is flaky, because it needs no tool-call parsing. It is not a Claude Code-class interactive agent, though: no TUI, and no Gas Town hooks or mail.
- Factory Droid and Amp are poor fits for a self-hosted-model requirement. Droid is closed, untested with BYOK and reportedly broken in exec mode. Amp's BYOK status is unverified (see Gaps).

### Gaps
- **Amp (Sourcegraph):** BYOK status, license and headless JSON were not researched. Amp has a Gas Town preset. My training knowledge says it is not BYOK (UNVERIFIED).
- **Gemini CLI:** OpenAI-compatible forks, `--output-format stream-json` and ACP were not researched. It has a Gas Town preset.
- **Not researched:** Kilo CLI (reportedly an OpenCode fork; UNVERIFIED), Roo Code (IDE-first; CLI status unknown), Continue CLI (`cn`), Mistral Vibe CLI, Codex app-server protocol details, Crush headless flags.
- **No adoption metrics** (stars, commit cadence in late 2026) were collected for any tool.

## Benchmarks where the harness is the variable

### Takeaway
The best harness-controlled evidence for open models comes from vendor tech reports. Qwen3-Coder-Next on TB2 varies by about 10 points across Terminus-2, Claude Code and Qwen Code. Open-model TB2 numbers are mostly reported on Terminus-2. I found no clean TB2 comparison of Codex, OpenCode, Crush, etc. using the same open-weight model. Benchmark version drift (TB 2.0 vs 2.1, plus references to 3.0 and 4.0) complicates comparisons.

### Cited Findings
- Qwen3-Coder-Next (80B-A3B) on TB2: Terminus2-json 36.2, Terminus2-xml 34.2, Claude Code 30.9, Qwen Code 25.8. — [arXiv 2603.00729](https://arxiv.org/html/2603.00729v1)
- Kimi K2.6 is reported at 66.7 on TB2 with Terminus-2. GLM-5.2 is reported at 81.0 on TB 2.1 with Terminus-2, and 82.7 with its "best reported harness". — search summary of third-party blogs ([Atlas Cloud](https://www.atlascloud.ai/blog/tips/kimi-k2-6-vs-glm-5-1-vs-qwen-3-6-plus-vs-minimax-m2-7-coding-2026), [kingy.ai](https://kingy.ai/news/best-open-weight-ai-models-in-2026-glm-5-2-vs-deepseek-v4-vs-kimi-k2-6-vs-qwen-vs-mistral/)), unverified
- Snorkel TB2 snapshot top entries: NexAU-AHE + GPT-5.5 84.7%, LemonHarness 84.5%, Capy + GPT-5.5 83.1%, Codex CLI + GPT-5.5 82.2%. — [Snorkel](https://snorkel.ai/leaderboard/terminal-bench-2-0/) (search summary)
- The tbench.ai leaderboard page now headlines Terminal-Bench 4.0, and its rows load dynamically, so I could not read them. Several papers use "Terminal-Bench 2.1", described as a minor evaluation update to TB2 with 89 tasks. — [tbench.ai](https://www.tbench.ai/leaderboard/terminal-bench/2.0); search summary of arXiv papers
- A 2026 survey argues that TB2 results do not support a harness-only interpretation, because the model dominates. — [arXiv 2606.20683](https://arxiv.org/pdf/2606.20683)
- "Harness-maxxing" commentary claims that a small open model beat a frontier model through harness engineering. — [Sukhareva substack](https://msukhareva.substack.com/p/how-a-small-open-model-beat-a-frontier) (opinion, not read)

### Inferences
- For 32B-70B open models, simple harnesses (Terminus-2-style JSON/XML; mini-swe-agent bash-only) can match or beat feature-rich product CLIs. Before committing, the user should run their own small harness-vs-harness eval on representative beads.

### Gaps
- I found no TB2 results for Codex CLI, Crush, Cline, OpenHands or Droid paired with open-weight 32B-70B models.
- I could not read the live tbench.ai leaderboard rows.

## Tool-calling pitfalls with vLLM / SGLang / llama.cpp / Ollama

### Takeaway
Most "agent X doesn't work with local model Y" failures happen in the serving layer: the tool-call parser, the chat template, or the reasoning parser interacting with tool calls. Common failures are tool calls left in plain content, tool calls emitted inside `<think>` and lost, degenerate output on long contexts, and Harmony (gpt-oss) header leakage in multi-step chains. These failures hit every harness that relies on native tool calls. They hit Codex hardest, because it is Responses-only and streaming. mini-swe-agent is least affected.

### Cited Findings
- **Qwen3-Coder on vLLM:**
  - The `qwen3_coder` parser sometimes leaves tool calls in plain content; issue #22975 was closed as not planned. — [vLLM #22975](https://github.com/vllm-project/vllm/issues/22975)
  - On long inputs, output degenerated into repeated "!" tokens with `qwen3_coder`. Switching to `qwen3_xml` fixed that, but arguments were sometimes missing. — search summary of vLLM issue
  - A patched parser was needed in one report, and vLLM 0.10.0 ignored `--tool-parser-plugin`. — [HF Qwen3-Coder-30B-A3B discussion #19](https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct/discussions/19)
- **vLLM 0.19 with Qwen3.5:** using `--reasoning-parser qwen3 --tool-call-parser qwen3_coder` can lose tool calls when the XML tool_call is emitted inside `<think>`. — [vLLM #39056](https://github.com/vllm-project/vllm/issues/39056)
- **Parser choice:** community guidance says current vLLM should use `qwen3_xml` and older builds `qwen3_coder`. This is third-party advice. — [nrrso Qwen3 chat template fixes](https://huggingface.co/nrrso/Qwen3-Chat-Template-vLLM-Fixes); [Qwen3-Coder-Next parser discussion](https://huggingface.co/Qwen/Qwen3-Coder-Next/discussions/17)
- **Community chat templates** claim to fix two agent-loop failures: aborting after mixed text plus tool call, and repeated failing calls. Some need vLLM's `from_json` filter and do not work on llama.cpp. — [Qwen Fixed Chat Templates](https://huggingface.co/scchow/Qwen-Fixed-Chat-Templates)
- **llama.cpp:**
  - PR #16755 added a dedicated parser for Qwen3-Coder's XML tool format; its author says it "essentially fixed" tool calling. — [llama.cpp PR #16755](https://github.com/ggml-org/llama.cpp/pull/16755)
  - Tool calling requires `--jinja`. An older issue shows Qwen2.5-Coder erroring when tool results were sent back under `--jinja`. — [llama.cpp #12279](https://github.com/ggml-org/llama.cpp/issues/12279)
- **gpt-oss / Harmony:**
  - The vLLM HarmonyParser can turn a stray `commentary to=assistant` header into a bogus tool call named `assistant<|channel|>analysis`. llama.cpp added a rule for this (#21286). — [vLLM #58825](https://github.com/vllm-project/vllm/issues/58825)
  - The OpenAI cookbook says gpt-oss function calling works via both Responses and Chat Completions on vLLM. An earlier vLLM issue noted tool calling was supported only on the Harmony/Responses endpoint. — [OpenAI cookbook: run gpt-oss with vLLM](https://cookbook.openai.com/articles/gpt-oss/run-vllm); [vLLM #22604](https://github.com/vllm-project/vllm/issues/22604)
  - Chained multi-tool calls can degrade into raw Harmony text in content, and serving-side templates can 400 on some content-block formats. — [Code With Karani](https://www.codewithkarani.com/blog/gpt-oss-120b-harmony-tool-calls-400-bad-request) (third-party)
  - One team found vLLM's Harmony parsing fragile while llama.cpp handled gpt-oss-20b fine; this dates from September 2025. — [Visokio](https://visokio.com/2025/09/19/lessons-from-the-trenches-why-llama-cpp-works-best-today/)
  - vllm-mlx (a separate project) reports that gpt-oss tool calls are never returned on streaming or `/v1/messages` paths. — [vllm-mlx #859](https://github.com/waybarrios/vllm-mlx/issues/859)

### Inferences
- **Pin and test the serving engine version, parser flag and chat template together per model.** Validate multi-turn streaming tool calls, not just single calls, before running unattended.
- **Codex's dependence on Responses streaming** makes it the harness most exposed to serving-layer gaps. Chat-Completions harnesses (Qwen Code, OpenHands/LiteLLM, Cline, Crush) are less exposed. mini-swe-agent sidesteps parsers entirely.
- **For an unattended supervisor,** add a watchdog that detects "tool call leaked into text" or "turn ended with no tool call and no completion" and re-nudges or restarts. These are the dominant local-model failure modes listed above.

### Gaps
- SGLang-specific tool-parser issues were not researched.
- I found no source on how Ollama's Responses endpoint behaves with current Codex.
