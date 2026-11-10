# Pi 1.5.0: configuring local models (models.json, compat, thinking, prompt size, compaction)

Researched 2026-11-03/04 from the Pi source at git tag `v1.5.0` (commit `f9a26365568127461c47a07a37dcb42c140a3ac5`, 2026-11-02, github.com/earendil-works/pi). I also checked it against the npm tarball `@earendil-works/pi-coding-agent@1.5.0` and `pi-ai@1.5.0`: the `dist/` code matches, for example `supportsStore: !isNonStandard` is at `pi-ai/dist/api/openai-completions.js:1259` and the system-prompt preamble is identical.

Paths below are relative to the repo root at `v1.5.0`:
- `CA/` = `packages/coding-agent/`
- `AI/` = `packages/ai/src/`

The doc pages are also published as `docs/*.md` inside the npm package.

Limit: running the Pi binary was blocked in this environment, so I could not capture a live request. The prompt and tool sizes in §5 are rebuilt from source strings, not measured on the wire.

---

## TL;DR for Spine with vLLM/SGLang on 127.0.0.1 or a LAN IP

1. **Auto-detection treats any localhost or LAN URL as "standard OpenAI".** A `baseUrl` such as `http://127.0.0.1:8000/v1` matches none of the host checks (`AI/api/openai-completions.ts:1587-1683`). Pi then defaults to:
   - `store: false` sent
   - role `developer` for reasoning models
   - `max_completion_tokens`
   - `thinkingFormat: "openai"`, which sends `reasoning_effort`

   For Qwen, GLM or Devstral templates on vLLM/SGLang, set the compat fields explicitly. Copy what Pi's own llama.cpp provider sets (`CA/src/extensions/llama/provider.ts:149-157`):
   ```
   supportsStore: false, supportsDeveloperRole: false, supportsReasoningEffort: false,
   maxTokensField: "max_tokens", thinkingFormat: "qwen-chat-template"
   ```
2. **The default thinking level is `medium`** (`CA/src/core/defaults.ts:3`). Spine passes no `--thinking` (`internal/agent/pirpc.go:41-42, 240-249`). So a `reasoning: true` model always gets thinking at "medium" unless one of these overrides it:
   - `settings.json` `defaultThinkingLevel`
   - `settings.json` `modelThinkingLevels["provider/id"]`
   - an adapter `args: [--thinking, off]`
   - a `--model provider/id:off` suffix
3. **`reasoning: false` sends no thinking field at all.** A hybrid model such as Qwen3, whose template defaults to thinking, will still think. To force thinking off, do one of these:
   - set `reasoning: true` with `thinkingFormat: "qwen-chat-template"` and level `off`, which sends `chat_template_kwargs.enable_thinking=false`;
   - put `"samplingParams": {"chat_template_kwargs": {"enable_thinking": false}}` on the model. This is merged into every request body (`AI/api/openai-completions.ts:989-992`).
4. **The default compaction settings break a 32K window.** The defaults are `reserveTokens 16384` and `keepRecentTokens 20000` (`CA/src/core/settings-defaults.ts:9-13`):
   - At 32K, compaction triggers at 16,384 tokens of context, but it keeps 20K recent tokens, which is already above the trigger point.
   - Set `compaction.modelOverrides` (see §7).
5. **Fixed prompt overhead is about 1.3–1.6K tokens** before the first user message: a ~2.3K-character system prompt plus ~2.8K characters of tool JSON. Any `AGENTS.md`/`CLAUDE.md` in the worktree or its parent directories comes on top. Spine does not pass `--no-context-files`.

---

## 1. models.json schema (custom OpenAI-compatible provider)

**Location.** The file is `<agent-dir>/models.json`. The agent dir is `~/.pi/agent`, or whatever `PI_CODING_AGENT_DIR` points to (`CA/docs/environment-variables.md:81`). Spine copies only `models.json` and `settings.json` into the container (`internal/agent/pirpc.go:417`).

**Source of truth.** The schema is `CA/src/core/model-config.ts:49-118`. The generated reference is `CA/docs/models-configuration.md`. The JSON schema is published at `https://pi.dev/schemas/models.schema.json` (`models-configuration.md:7`).

### Provider fields (`model-config.ts:90-114`, doc lines 13-23)

| Field | Notes |
|---|---|
| `baseUrl` | Required for a new provider. Include `/v1`. |
| `api` | Required at provider or model level. Values: `openai-completions` (Chat Completions), `openai-responses`, `anthropic-messages`, `google-generative-ai`, `mistral-conversations`, and others (doc line 18). Use `openai-completions` for vLLM, SGLang, llama.cpp, Ollama and LM Studio. |
| `apiKey` | One of three forms:<br>- a literal value;<br>- `"$NAME"` or `"${NAME}"` for an environment variable;<br>- `"!command"`, which runs at request time and is not cached.<br>`$$` gives a literal `$` and `$!` a literal `!` (`CA/docs/models.md:65`; parser in `CA/src/core/resolve-config-value.ts:28-130`).<br>A dummy key is needed for the model to count as "available" (`models.md:65, 211-213`). |
| `headers` | Values resolve the same way as `apiKey`. |
| `authHeader` | Default false. When true, the key is sent as `Authorization: Bearer`. |
| `compat` | Provider-level compat. Model-level compat overrides it per field (`models-configuration.md:107`). |
| `models[]`, `modelOverrides{}` | Model list, and overrides for existing models. |
| `name`, `oauth` | Optional. |

**Credential precedence.** `--api-key`, then `auth.json`, then models.json `apiKey`, then the provider's environment variable (`models.md:23`).

### Model fields (`model-config.ts:49-70`, doc lines 30-57)

| Field | Default | Notes |
|---|---|---|
| `id` | required | Sent as `model` in the request. |
| `name` | | Display name, also matched by `--model`. |
| `api`, `baseUrl` | | Per-model overrides. |
| `reasoning` | `false` | Gates every thinking parameter. |
| `thinkingLevelMap` | | `{off,minimal,low,medium,high,xhigh,max}` to a provider string, or `null` for "unsupported". |
| `input` | `["text"]` | Add `"image"` for vision models. |
| `inputLimits` | | |
| `cost` | | Fields `{input, output, cacheRead, cacheWrite}` (all required if present), plus optional `tiers[]`. |
| `promptCache` | | |
| `contextWindow` | `128000` | |
| `maxTokens` | `16384` | |
| `samplingParams` | | Free-form fields merged into the body. OpenAI APIs only. |
| `samplingParamsByThinkingLevel` | | |
| `headers` | | |
| `compat` | | |

The `contextWindow` and `maxTokens` defaults (128000 and 16384) are wrong for most local deployments, so always set both.

**How `max_tokens` is actually sent.** It is `min(model.maxTokens, contextWindow − estimatedContext − 4096)` (`AI/api/simple-options.ts:15-22, 46`). If `contextWindow` is larger than the server's `--max-model-len`, the server rejects requests once the context gets long. Set it to exactly the server's limit.

### Worked example (vLLM Qwen3, vLLM gpt-oss, SGLang GLM, llama.cpp Devstral, keyed LAN server)

```json
{
  "$schema": "https://pi.dev/schemas/models.schema.json",
  "providers": {
    "vllm": {
      "baseUrl": "http://127.0.0.1:8000/v1",
      "api": "openai-completions",
      "apiKey": "$VLLM_API_KEY",
      "compat": {
        "supportsStore": false,
        "supportsDeveloperRole": false,
        "maxTokensField": "max_tokens",
        "supportsStrictMode": false
      },
      "models": [
        {
          "id": "qwen3-coder-30b",
          "name": "Qwen3 Coder 30B (vLLM)",
          "reasoning": false,
          "input": ["text"],
          "contextWindow": 65536,
          "maxTokens": 8192,
          "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 },
          "samplingParams": { "temperature": 0.7, "top_p": 0.8, "top_k": 20, "repetition_penalty": 1.05 }
        },
        {
          "id": "qwen3-32b",
          "reasoning": true,
          "contextWindow": 32768,
          "maxTokens": 8192,
          "thinkingLevelMap": { "minimal": null, "low": null, "high": null },
          "samplingParamsByThinkingLevel": {
            "off":    { "temperature": 0.7, "top_p": 0.8, "top_k": 20 },
            "medium": { "temperature": 0.6, "top_p": 0.95, "top_k": 20 }
          },
          "compat": {
            "supportsReasoningEffort": false,
            "thinkingFormat": "qwen-chat-template",
            "thinkingTokenBudgetField": "thinking_token_budget"
          }
        },
        {
          "id": "openai/gpt-oss-20b",
          "reasoning": true,
          "contextWindow": 65536,
          "maxTokens": 16384,
          "thinkingLevelMap": { "off": "low", "minimal": "low" },
          "compat": { "thinkingFormat": "openai", "supportsReasoningEffort": true }
        }
      ]
    },
    "sglang-lan": {
      "baseUrl": "http://192.168.1.50:30000/v1",
      "api": "openai-completions",
      "apiKey": "!cat /run/secrets/sglang_key",
      "headers": { "X-Client": "spine" },
      "models": [
        {
          "id": "glm-4.6-air",
          "reasoning": true,
          "contextWindow": 65536,
          "maxTokens": 8192,
          "compat": {
            "supportsStore": false,
            "supportsDeveloperRole": false,
            "supportsReasoningEffort": false,
            "maxTokensField": "max_tokens",
            "thinkingFormat": "chat-template",
            "chatTemplateKwargs": { "enable_thinking": { "$var": "thinking.enabled" } },
            "thinkingTokenBudgetField": "thinking_budget"
          }
        }
      ]
    },
    "llamacpp": {
      "baseUrl": "http://127.0.0.1:8080/v1",
      "api": "openai-completions",
      "apiKey": "local",
      "compat": {
        "supportsStore": false,
        "supportsDeveloperRole": false,
        "supportsReasoningEffort": false,
        "maxTokensField": "max_tokens",
        "supportsStrictMode": false
      },
      "models": [ { "id": "devstral-small", "reasoning": false, "contextWindow": 32768, "maxTokens": 8192 } ]
    }
  }
}
```

Notes on the example:
- `cost` can be omitted for local models; it then counts as $0.
- With Spine's adapter `model: vllm/qwen3-32b`, the `provider/id` form works even when `id` itself contains slashes (CHANGELOG `CA/CHANGELOG.md:84`).
- For gpt-oss, `"off": "low"` makes Pi send `reasoning_effort: "low"` at level off. gpt-oss cannot fully disable reasoning, and `AI/api/openai-completions.ts:957-961` sends a string `off` value.
- The GLM `enable_thinking` kwarg name is my assumption about the GLM-4.5/4.6 chat template, not something Pi documents. Check it against your template.

---

## 2. `compat` for `openai-completions`: every field, and what auto-detects

The field list is in `CA/docs/models-configuration.md:109-139`, the schema in `AI/providers/compat-schema.ts:190-345`, and the resolution in `AI/api/openai-completions.ts:1690-1727`. Explicit compat overrides detected values per field, and model-level compat overrides provider-level compat.

| Field | Detected default for a localhost/LAN URL | Effect (source) |
|---|---|---|
| `supportsStore` | **true**: sends `store:false` | `:823-825` |
| `supportsDeveloperRole` | **true**: the instruction role becomes `developer` **only when `model.reasoning`** | `:1220` |
| `supportsReasoningEffort` | **true** | Lets the `openai`/`qwen`/`zai`/`together`/`baseten`/`deepseek` formats add `reasoning_effort` |
| `supportsUsageInStreaming` | true: `stream_options.include_usage` | `:819-821` |
| `supportsFinishReason` | true | When false, the stop reason is inferred |
| `maxTokensField` | **`max_completion_tokens`** | `:827-834` |
| `requiresToolResultName` | false | Adds `name` to tool messages (`:1418`) |
| `requiresAssistantAfterToolResult` | false | Inserts an empty assistant turn between a tool result and a user message (`:1229, 1440`), for strict Mistral-family templates |
| `requiresThinkingAsText` | false | Replays old thinking as plain text (`:1312`) |
| `requiresReasoningContentOnAssistantMessages` | false (true only for DeepSeek) | Adds empty `reasoning_content` to replayed assistant messages (`:1374-1381`) |
| `thinkingFormat` | **`openai`** | See §3 |
| `chatTemplateKwargs` / `chatTemplateArgs` | `{}` | Values may be literals or `{ "$var": "thinking.enabled" \| "thinking.effort" \| "thinking.budget", "omitWhenOff"?: bool }` (`compat-schema.ts:42-55`; resolver `openai-completions.ts:1037-1060`) |
| `thinkingTokenBudgetField` | unset | `"thinking_token_budget"` (vLLM), `"thinking_budget"` (Qwen/DashScope/SGLang), `"thinking_budget_tokens"` (llama.cpp), sent as a top-level field. It is sent only when thinking is on, and is independent of `thinkingFormat` (`:963-970`). See the budget table below. |
| `supportsThinkingTokenBudget` | false | Legacy alias for the `thinking_token_budget` field (`:997-1003`) |
| `vllmPriority` | unset | Top-level `priority` (`:858-860`). Only meaningful with vLLM `--scheduling-policy priority`. |
| `supportsStrictMode` | **false**: the `strict` key is omitted from tools | `:1494-1503` |
| `supportsOpenAIGrammarTools` | false | Lark/regex custom tools fall back to function tools |
| `supportsMidConvoSystemMessages` / `supportsMidConvoToolAdditions` | false | Later system messages are folded into the leading system prompt |
| `cacheControlFormat` | unset | `"anthropic"` adds cache_control markers |
| `sendSessionAffinityHeaders` / `sessionAffinityFormat` | false / `openai` | |
| `supportsLongCacheRetention` | true | Matters only with `PI_CACHE_RETENTION=long`: it then sends `prompt_cache_key` and `prompt_cache_retention:"24h"` (`:300-308, 811-817`). Set it to false for local servers if you use that variable. |
| `zaiToolStream` | false | z.ai `tool_stream` |
| `openRouterRouting`, `vercelGatewayRouting` | `{}` | Gateway-only |

**Thinking budget sizes.** The defaults are minimal 1024, low 2048, medium 8192 and high 16384 (`AI/api/simple-options.ts:71-76`). The budget is clamped so that at least 1024 tokens remain for the answer (`:68-91`). You can override the sizes with `settings.json` `thinkingBudgets` (`CA/docs/settings.md:17`; `sdk.ts:436`).

### URL and provider auto-detection (`AI/api/openai-completions.ts:1587-1683`)

All matches are substring checks on `baseUrl` (or the built-in provider ID):

| Match | Hosts or provider IDs |
|---|---|
| zai | `api.z.ai`, `open.bigmodel.cn` |
| together | `api.together.ai`, `.xyz` |
| moonshot | `api.moonshot.` |
| openrouter | `openrouter.ai` |
| cloudflare | `api.cloudflare.com`, `gateway.ai.cloudflare.com` |
| nvidia | `integrate.api.nvidia.com` |
| ant-ling | `api.ant-ling.com` |
| cerebras | `cerebras.ai` |
| deepseek | `deepseek.com` |
| xai | `api.x.ai` |
| other | `chutes.ai`, `opencode.ai` |

How each group changes the detected values:
- **"Non-standard" hosts** turn off `store` and `developer` (`:1607-1621`).
- **`max_tokens` hosts** are chutes, deepseek, moonshot, cf-gateway, together, nvidia, ant-ling and zai (`:1623-1631`).
- **`thinkingFormat`** becomes `deepseek`, `zai`, `together`, `ant-ling` or `openrouter` for those hosts, and `openai` for everything else (`:1649-1659`).

**There is no detection for localhost, private IPs, vLLM, SGLang, Ollama or LM Studio.** `http://127.0.0.1:8000/v1` and `http://10.x.x.x/v1` get the "real OpenAI" profile. For any non-OpenAI template, set these explicitly: `supportsDeveloperRole:false`, `supportsStore:false`, `maxTokensField:"max_tokens"` (most servers accept both forms; `max_tokens` is safest), `thinkingFormat`, and `supportsReasoningEffort`.

The built-in `llama.cpp` provider (from `/login llama.cpp` or `LLAMA_BASE_URL`) sets all of these itself (`CA/src/extensions/llama/provider.ts:134-157`). Spine's `--no-extensions` disables that provider, so you have to set them by hand.

---

## 3. Thinking control: from level to request fields

**How the level is chosen** (`CA/src/core/sdk.ts:252-276`):
1. The CLI: `--thinking <level>` or `--model p/id:<level>` (`CA/docs/cli.md:66-70`).
2. Otherwise, the session's stored level.
3. Otherwise, `settings.modelThinkingLevels["provider/id"]` (`settings-schema.ts:218-222`).
4. Otherwise, `settings.defaultThinkingLevel`.
5. Otherwise, `DEFAULT_THINKING_LEVEL = "medium"` (`CA/src/core/defaults.ts:3`).

The chosen level is then clamped to what the model supports. In RPC, `{"type":"set_thinking_level","level":"off"}` changes it mid-session (`CA/docs/rpc-commands.md:274-289`). Levels are `off`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`.

**Clamping** (`AI/models.ts:1259-1291`):
- A `reasoning:false` model supports only `off`.
- A level mapped to `null` in `thinkingLevelMap` is removed.
- `xhigh` and `max` exist only if mapped.
- An unsupported level moves up to the next supported level, or down if there is none above.
- After clamping, `off` becomes `reasoningEffort = undefined` (`AI/api/openai-completions.ts:729-730`).

**What each `thinkingFormat` sends** (`AI/api/openai-completions.ts:865-962`). None of these is sent unless `model.reasoning` is true.

| thinkingFormat | Level ≠ off | Level = off |
|---|---|---|
| `openai` (default) | `reasoning_effort: map[level] ?? level` (`:954-956`) | Nothing, unless `thinkingLevelMap.off` is a string, which is then sent as `reasoning_effort` (`:957-961`) |
| `qwen` | top-level `enable_thinking:true`, plus `reasoning_effort` if `supportsReasoningEffort` | `enable_thinking:false` (`:878-885`) |
| `qwen-chat-template` | `chat_template_kwargs:{enable_thinking:true, preserve_thinking:true}`. No effort is sent; only on/off. | `{enable_thinking:false, preserve_thinking:true}` (`:886-890`) |
| `chat-template` | `chat_template_kwargs` built from your `chatTemplateKwargs`, with `$var` placeholders resolved | same template; `omitWhenOff` drops a key (`:891-895, 1019-1060`) |
| `zai` | `thinking:{type:"enabled",clear_thinking:false}` (+effort) | `thinking:{type:"disabled"}` (`:865-877`) |
| `deepseek` | `thinking:{type:"enabled"}` (+effort) | `thinking:{type:"disabled"}` unless `off:null` (`:913-922`) |
| `together`, `openrouter`, `baseten`, `string-thinking`, `ant-ling` | Provider-specific | `:896-953` |

Separately from the format, a configured `thinkingTokenBudgetField` gets the level's budget, clamped (`:963-970`). After that, `samplingParams` and `samplingParamsByThinkingLevel[level]` are merged last, so they override every named field (`:986-992`; `AI/api/simple-options.ts:24-34`).

### Per model family on local servers

- **Qwen3, Qwen3.5 and other hybrid models (vLLM/SGLang/llama.cpp):**
  - Use `reasoning:true` with `thinkingFormat:"qwen-chat-template"`.
  - Add `thinkingLevelMap` with `minimal/low/high: null` to collapse the choice to off/medium, as Pi's llama.cpp provider does (`provider.ts:142-144`).
  - The level `off` sends `enable_thinking:false`, so thinking is fully off.
  - `preserve_thinking:true` was added so multi-turn tool calls keep their arguments (`AI/CHANGELOG` 0.67.67).
  - Use `"qwen"` only for DashScope-style servers that read top-level `enable_thinking`.
  - **Qwen3-Coder** (non-thinking): set `reasoning:false`.
- **gpt-oss (vLLM/llama.cpp):**
  - Use the default `openai` format with `reasoning_effort` low/medium/high. Map `minimal` to `low` (or null).
  - Reasoning cannot be turned off; map `off` to `"low"`.
  - Pi replays earlier reasoning in the same field the server used (`reasoning_content` for llama.cpp + gpt-oss) (`:1328-1336`).
- **GLM-4.5/4.6/5.x local:**
  - Use `chat-template` with `{enable_thinking: {$var:"thinking.enabled"}}`, or `qwen-chat-template` (it also sends `preserve_thinking`, which templates ignore).
  - Use `zai` only for the z.ai/bigmodel API.
  - The template kwarg name is an assumption to verify; it is not in Pi.
- **Mistral/Devstral:**
  - Through `openai-completions` on vLLM/llama.cpp: set `reasoning:false`. Pi then sends no thinking fields.
  - The native `mistral-conversations` API uses `reasoning_effort` when a `thinkingLevelMap` exists, else `prompt_mode:"reasoning"` (`AI/api/mistral-conversations.ts:198-212`).
  - Devstral templates enforce strict role alternation. If you see "roles must alternate" errors after tool results, set `requiresAssistantAfterToolResult:true` (`openai-completions.ts:1229, 1284, 1440`).

**Can thinking be fully off per model? Yes:**
- (a) Set `reasoning:false`. No fields are sent, but a template that defaults to thinking still thinks.
- (b) Set `reasoning:true` with a format that has an explicit off (`qwen-chat-template`, `qwen`, `chat-template`, `zai`, `deepseek`), plus `modelThinkingLevels: {"vllm/qwen3-32b":"off"}` in `settings.json`, or `--thinking off`.
- (c) Use `samplingParams.chat_template_kwargs` to hard-wire it.

Option (c) is a blunt override: `samplingParams` merges last and replaces the whole `chat_template_kwargs` object.

---

## 4. `/llama` and llama.cpp integration in 1.5.0

Pi has a **built-in extension**, `builtin:llama.cpp`, at `CA/src/extensions/llama/{index,provider,client,ui,huggingface}.ts`. It was added in 0.81.0 (`CA/CHANGELOG.md` 0.81.0). It does two things:

1. **Registers a provider `llama.cpp`** (`provider.ts:24-25, 180-299`):
   - Default URL `http://127.0.0.1:8080`, configured with `/login llama.cpp` or `LLAMA_BASE_URL`/`LLAMA_API_KEY` (`docs/llama-cpp.md:57-77`).
   - Lists the router's **loaded and sleeping models** (plus autoload presets) as `openai-completions` models at `<url>/v1`.
   - `contextWindow` comes from the router's `n_ctx`, then the `-c`/`--ctx-size` argument, then the cache, then `n_ctx_train`, then 128000 (`provider.ts:59-80`). `maxTokens` is set equal to `contextWindow`.
   - `reasoning` is true when the chat template contains `enable_thinking`. Thinking is then off/medium only via `qwen-chat-template`, with the compat shown in §2 (`provider.ts:127-157`).
   - Chat models are also listed as classifier models (`llama-cpp-classify`). Decision models go through `/v1/systemone` (1.1.0).
2. **Registers the `/llama` slash command** (`index.ts:183-229`). It manages a llama.cpp router (`llama serve` without `-m`):
   - load and unload models;
   - search Hugging Face and download;
   - retry after a disconnect.

   It is **TUI-only**: in other modes it prints "/llama is available in interactive mode" (`index.ts:186-189`).

It is **not** llama-swap and does not start servers. It talks to an already-running llama.cpp router (`llama-cpp.md:21-33`). You can disable it with `"extensions": ["-builtin:llama.cpp"]` (`llama-cpp.md:132`).

**Implications for Spine:**
- Spine runs with `--no-extensions`, which also turns off built-in extensions: "Disable extension discovery and built-in extensions" (`CA/src/cli/args.ts:189, 327`).
- So the `llama.cpp` provider and `/llama` **do not exist** in Spine's Pi runs. `/llama` would not work in RPC mode anyway.
- Use a plain models.json provider pointing at llama-server `/v1`, with the compat fields that `provider.ts:149-157` sets.

---

## 5. System prompt and tool schemas in 1.5.0: size before the first user message

**How the prompt is built** (`CA/src/core/system-prompt.ts:128-192`). Sections are joined with `\n\n` (`AI/utils/text.ts:15-21`), and every section except the preamble is wrapped in `<name>…</name>`. In order:
- the preamble (`:155-156`);
- `<tools>`: one line per tool from its `promptSnippet`, plus "Additional tools may be available." (`:157-160`);
- `<rules>` (`:88-125`);
- `<docs>`: Pi doc paths under the package dir (`:162-168`);
- the optional `<addendum>` (from APPEND_SYSTEM.md or `--append-system-prompt`);
- `<project_context>`: context files (`:79-86, 172`);
- `<skills>` (disabled by Spine's `--no-skills`);
- `<cwd>`.

There is no date and no OS info. The CHANGELOG for 1.5.0 says "Tightened system prompt" (`CA/CHANGELOG.md:32`).

**Default tools** are read, bash, edit and write (`CA/src/core/settings-manager.ts:85`, `agent-session.ts:3731-3733`).

**Snippets and guidelines** come from:
- `read.ts:19-22, 95`
- `bash.ts:44-47, 254`
- `edit.ts:42-48, 148`
- `write.ts:15-18, 51-52`

The bash `PI_*` guideline is included because `exposeSessionEnvironment` defaults to true (`bash.ts:249-256`).

**Reconstructed prompt** for the npm-global path `/usr/local/lib/node_modules/@earendil-works/pi-coding-agent` (what the `contrib/pi` image uses) and a 44-character cwd. The script is in the scratchpad (`scripts/sysprompt.py`).

```
You are a coding assistant running inside pi, a coding agent harness. Help users inspect files, run commands, and modify code.

<tools>
- read: Read file contents
- bash: Execute bash commands (ls, grep, find, etc.)
- edit: Make precise file edits with exact text replacement, including multiple disjoint edits in one call
- write: Create or overwrite files

Additional tools may be available.
</tools>

<rules>
- Use bash for file operations like ls, rg, find
- Use read to examine files instead of cat or sed.
- You can inspect PI_* environment variables for current model and session details.
- Use edit for targeted changes. Batch changes to one file in a single call.
- Each edits[].oldText must exactly and uniquely match the original file. Keep matches short and non-overlapping; merge nearby changes without including large unchanged regions.
- Use write only for new files or complete rewrites.
- Be concise in your responses
- Show file paths clearly when working with files
</rules>

<docs>
Pi documentation (read only when the user asks about the harness itself, ...):
- Main documentation: /usr/local/lib/node_modules/@earendil-works/pi-coding-agent/README.md
- Additional docs: .../docs
- Examples: .../examples (extensions, custom tools, SDK)
- When reading pi docs or examples, resolve docs/... under Additional docs ...
- When asked about: extensions (docs/extensions.md, ...), ... codemode scripts ... (docs/codemode.md)
- Before implementing harness changes, read the relevant docs and examples in full, following related cross-references.
</docs>

<cwd>
/home/user/spine-next/.spine/worktrees/t-123
</cwd>
```

### Measured sizes (reconstructed)

**System prompt:**

| Item | Size |
|---|---|
| Whole prompt | **2,284 chars, 289 words, ≈570 tokens** (chars/4) |
| `<docs>` section alone | ≈1,150 chars, about half the prompt |

**Tool definitions** as sent in `tools[]`:
- Built by `AI/api/openai-completions.ts:1471-1505` from plain TypeBox JSON Schema.
- `strict` is omitted because `supportsStrictMode` is false.
- `outputSchema` is not sent (`agent/src/types.ts:430`).

| Tool | Compact JSON | ≈ tokens |
|---|---|---|
| read | 684 chars | 170 |
| bash | 543 chars | 135 |
| edit | 1,179 chars | 295 |
| write | 430 chars | 107 |
| **Total** | **2,836 chars** (3,824 pretty-printed) | **≈710** |

Tool descriptions say output is truncated to 2000 lines or 50KB (`truncate.ts:11-12`).

**Total before the first user message:** about 1,280 tokens, plus the chat-template wrapper. Qwen- and Hermes-style templates add ~100–300 tokens of tool-use instructions when they render `tools` into the system turn. **Estimate 1.3–1.6K tokens.** The Spine task prompt itself comes on top.

**What AGENTS.md and context files add** (`CA/docs/configuration.md:41-47`; `resource-loader.ts:213`):
- Pi loads `AGENTS.override.md`, `AGENTS.md`, `AGENTS.MD`, `CLAUDE.md` or `CLAUDE.MD` from:
  - the agent dir;
  - the cwd;
  - **every parent directory**.
- Discovery does **not** require project trust.
- Each file is added verbatim inside `<project_instructions path="...">…</project_instructions>`, under the header "Project-specific instructions and guidelines:" (`system-prompt.ts:79-86`). That costs about chars/4 tokens per file plus ~20 tokens of wrapper.

In Spine's container, Pi sees only the worktree (plus its mounted parents, if any), so the target repo's AGENTS.md/CLAUDE.md will be loaded. Spine's private agent dir has no AGENTS.md (only models.json and settings.json are copied). To keep the prompt minimal on a 32K model, add `--no-context-files` (`-nc`, `CA/src/cli/args.ts:216, 336`) through adapter `args`. Other relevant flags: `--system-prompt <text>`, which replaces the preamble, tools, rules and docs, and `--append-system-prompt` (`args.ts:121-124, 304-305`).

---

## 6. Edit tool mechanics (1.5.0)

**Schema** (`CA/src/core/tools/edit.ts:20-40`): `{path: string, edits: [{oldText, newText}]}`. This is the multi-edit form; the old single `oldText`/`newText` form was replaced.

**Lenient input** (`prepareEditArguments`, `edit.ts:100-131`). The tool accepts:
- `edits` as a JSON **string**, which it parses (noted for "Opus 4.6, GLM-5.1");
- a single edit object instead of an array;
- legacy top-level `oldText`/`newText`, which is appended to `edits`.

An empty `edits` array fails with "Edit tool input is invalid. edits must contain at least one replacement." (`:133-138`).

**Matching** (`CA/src/core/tools/edit-diff.ts:300-362`):
1. Strip the BOM and normalize CRLF to LF. The file's original line endings are restored on write (`edit.ts:186-195`).
2. Match every edit against the **original** content, not incrementally.
3. Try exact `indexOf` first. If that fails, use the **fuzzy fallback** `normalizeForFuzzyMatch` (`:34-55`), which applies NFKC, trims trailing whitespace per line, turns smart quotes into ASCII, turns Unicode dashes into `-`, and turns special spaces into a regular space.
4. If any edit needed fuzzy matching, the whole operation runs in normalized space. Changed lines are then overlaid onto the original, so untouched lines keep their original bytes (`:132-140, 352-355`).
5. There is no indentation-insensitive or whitespace-collapsing match. Leading whitespace must match exactly.

**Errors returned to the model** (`edit-diff.ts:253-289, 346-348`; `edit.ts:178`):

| Case | Message |
|---|---|
| Not found | "Could not find the exact text in {path}. The old text must match exactly including all whitespace and newlines." (multi-edit form: "Could not find edits[i] in {path}…") |
| Duplicate | "Found N occurrences of the text in {path}. The text must be unique. Please provide more context to make it unique." Uniqueness is counted in fuzzy space. |
| Empty | "oldText must not be empty in {path}." |
| Overlap | "edits[i] and edits[j] overlap in {path}. Merge them into one edit or target disjoint regions." |
| No change | "No changes made to {path}. The replacement produced identical content. …" |
| Missing file | "Could not edit file: {path}. Error code: ENOENT." |

On success the tool returns "Successfully replaced N block(s) in {path}." Edits are all-or-nothing, and the file is written only if every edit matched.

**The 1.5.0 tool-schema change** (`CA/CHANGELOG.md:14`; `AI/CHANGELOG.md` 1.5.0 lines 5-22). This is a breaking change:
- Tool argument validation now runs on `@earendil-works/pi-schema` (JSON-Schema-only) instead of TypeBox's validator.
- TypeBox-only features (`Type.Refine`, `BigInt`, `Undefined`, codecs, `Format.Set` formats) no longer apply.
- Plain JSON Schema and MCP tools now get the same **coercion**: numeric strings become numbers, and a single value is wrapped into an array. This helps weaker local models.
- Validation errors the model sees now read like `- path: missing` and `- limit: expected number, got string`, instead of `must have required properties path`.
- The built-in tools' schemas (read/bash/edit/write) are unchanged in shape.

---

## 7. Compaction defaults and tuning for 32K–64K windows

**Defaults:** `enabled:true`, `reserveTokens:16384`, `keepRecentTokens:20000` (`CA/src/core/settings-defaults.ts:9-13`; `compaction/compaction.ts:121-130`).

**When it triggers:** when `contextTokens > contextWindow − reserveTokens` (`compaction.ts:267-270`; `CA/docs/compaction.md:28-37`). It is checked:
- between turns, after tool results;
- before a prompt;
- for overflow recovery: one compact-and-retry on a provider context-overflow error or an early `stopReason:"length"` (`compaction.md:37-39`).

vLLM's "maximum context length is N tokens" error is recognized as overflow (`AI/utils/overflow.ts:50`), as are the llama.cpp, LM Studio and Ollama messages (`:37, 54-55`).

**Per-model overrides** go in `settings.json`, keyed by the exact `provider/modelId` (`settings-schema.ts:13-27`; `compaction.md:441-461`):
```json
{
  "defaultThinkingLevel": "off",
  "modelThinkingLevels": { "vllm/qwen3-32b": "medium" },
  "compaction": {
    "modelOverrides": {
      "vllm/qwen3-32b":        { "reserveTokens": 12288, "keepRecentTokens": 8000 },
      "vllm/qwen3-coder-30b":  { "reserveTokens": 14336, "keepRecentTokens": 16000 }
    }
  }
}
```

### Tuning rule

This rule is derived from the code paths above; it is not stated in Pi's docs.

1. **`reserveTokens` ≥ the output you want near the threshold + 4096.** The output cap is clamped to `contextWindow − context − 4096` (`simple-options.ts:15-22`). With a thinking model, add the thinking budget (8192 at medium) or set `thinkingBudgets` lower.
2. **`keepRecentTokens` must be well below `contextWindow − reserveTokens − (system prompt + summary)`.** Otherwise compaction re-fires right away. The fixed prompt is about 1.5K tokens and summaries a few K.

**Worked numbers:**
- **32K (32768), maxTokens 8192:**
  - Use reserve 12288, so it triggers at about 20.5K.
  - Use keepRecent 6–8K, so after compaction the context is about 1.5K + 2–3K summary + 8K ≈ 12K.
  - Lower the thinking budget (e.g. `thinkingBudgets.medium: 4096`) or use thinking off.
  - **The defaults (16384/20000) are unusable here**: compaction triggers at 16K but tries to keep 20K.
- **64K (65536), maxTokens 8–16K:**
  - Use reserve 16384–20480, so it triggers at about 45–49K.
  - Use keepRecent 16–20K.
  - The defaults are borderline OK.
- **Summary requests** use the model's output limit, reduced to fit (`compaction.md:35`). A 32K model with maxTokens 8192 is fine.
- **`enabled`** is global only. It cannot be set per model.

**Interaction with Spine:** Spine maps a final `stopReason:"length"` to BudgetExceeded (`internal/agent/pirpc.go:378-383`). Pi first tries one compact-and-retry for an early length stop, so the final mapping only bites after that.

---

## 8. CHANGELOG 1.0–1.5: local-model relevant items

These come from `CA/CHANGELOG.md` and `AI/CHANGELOG.md`. In 1.0–1.5 there were **no** fixes specific to vLLM, SGLang, Ollama, Qwen-local or Devstral. The relevant items are:

**1.5.0**
- pi-schema validation with argument coercion (`CA:14`).
- Tightened system prompt (`CA:32`).
- `--model <provider>/<id>` now works for provider IDs with slashes, such as URL-derived llama.cpp IDs (`CA:84`).
- `/tree` branch summaries now honor the session thinking level. Before, reasoning models could reject an unsupported `none` effort (`CA:79`).
- LiteLLM `ContextWindowExceededError` is recognized as overflow (`CA:94`).
- Large streamed tool calls are parsed incrementally, saving CPU and memory with big `write` calls (`CA:57`).
- Gemini `const` to `enum` (not local).

**1.2.0**
- Mistral streams are no longer aborted mid-generation (e.g. during long thinking) by the request timeout. The timeout now applies only to waiting for headers (`CA:256`, `AI:145`).

**1.1.0**
- Native llama.cpp decision models (Julia-1, Laya, Kev, lev, OpenJev) on llama.cpp ≥0.6.0 run as classifiers through `/v1/systemone` (`CA:292, 304`).
- Mistral `finish_reason:"error"` is retried (`CA:329`, `AI:178`).
- Bedrock gpt-oss gets `reasoning_effort` clamped to low/medium/high (`AI:179`; Bedrock only).

**Earlier features still in effect** (from the 0.x history, for context):
- `qwen-chat-template` (0.58.1, `AI:1614`) and `preserve_thinking` (0.67.67, `AI:1426`).
- `chat-template` with `chatTemplateKwargs` (0.79.9).
- vLLM `thinking_token_budget` (0.84.0).
- Configurable budget fields for vLLM, Qwen/SGLang and llama.cpp (0.84.3, `AI:446`).
- `vllmPriority` (0.85.0, `AI:401`).
- Built-in llama.cpp router and `/llama` (0.81.0).
- llama.cpp streamed usage fix (0.83.0).
- Ollama "prompt too long" recognized as overflow (0.63.1).
- Ollama and LM Studio `finish_reason:"end"` (0.58.1).
- LM Studio reasoning_text streaming (0.73.1).

---

## 9. The spine-next Pi adapter (read-only review)

**Flags** (`internal/agent/pirpc.go:41-42, 240-249`): `--mode rpc --no-session --no-approve --no-extensions --no-skills --no-prompt-templates --no-themes --no-mcp`, then `--provider`, `--model` and the adapter `args`. Spine passes **no `--thinking`** and **no `--no-context-files`**, and sets nothing per model. Thinking therefore resolves to Pi's default `medium`, unless the copied `settings.json` or `args` say otherwise.

**Default limits:**

| Setting | Default | Source |
|---|---|---|
| `stall_timeout` | 15m | `pirpc.go:260-262`; config `internal/config/config.go:62, 159-169` |
| `tool_timeout` | 10m. After 2 recoveries (`MaxToolTimeouts = 2`, `:471`), the 3rd timeout ends the run (`:599-608`). | `pirpc.go:263-265` |
| `max_turns` | 80 assistant turns | `:266-268, 522-523` |
| Token budget | Only when `budget.max_tokens` > 0: remaining = budget − tokens used. It is checked against Pi's per-message usage (`pirpc.go:544-545`). Default MaxTokens is 0, meaning no token cap (`internal/api/task.go:37-53` sets no default). | `internal/reconciler/reconcile.go:252-258, 266` |
| Run timeout | `budget.per_run_timeout`, default **45m** (`internal/api/task.go:49-51`). A wrap-up steer is sent at 80% (`pirpc.go:611-614`) and the run is aborted at 100% (`:593-595`). The process is killed if the abort is not honored within 20s (`:580-581`). | |
| Wall clock | `max_wall_clock` default 4h. Attempts default 2, patch rounds 3. | `task.go:38-48` |

**Container** (`internal/agent/container.go:28-31, 93-105`):
- `--network` defaults to **`host`**, so `127.0.0.1:8000` reaches a model server on the host.
- `--user` is the controller's uid:gid, `HOME=/tmp/spine-home`, `--rm -i --init`.
- The shared git dir is mounted `:ro`.
- A private agent dir is mounted at `/spine/pi-agent` with `PI_CODING_AGENT_DIR` set (`pirpc.go:281-293`), containing copies of `models.json` and `settings.json` only (`:417-438`). So per-model `compaction.modelOverrides`, `modelThinkingLevels`, `defaultThinkingLevel` and `thinkingBudgets` can all live in that `settings.json`, and they do reach the container.
- The image `contrib/pi/Dockerfile` is `node:22-bookworm-slim` with `npm install -g @earendil-works/pi-coding-agent@1.5.0`, so the docs path in the prompt is `/usr/local/lib/node_modules/...`.

**README example** (`README.md:80-83`): the models.json example has no `compat` and no `reasoning`. That is fine for Qwen3-Coder (`reasoning:false`). It would misbehave for a reasoning model, which would get role `developer` and `reasoning_effort` (see TL;DR).

**Suggested adapter `args` for small-context local models:** `[--thinking, off, --no-context-files]`. Alternatively, put `modelThinkingLevels` in `settings.json`.
