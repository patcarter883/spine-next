# Local models for spine/Pi: serving + agentic-coding benchmarks

Researched 2026-11-03/04. Source checks: vLLM main @56e993a (2026-11-03; latest tag v0.33.0), SGLang main @a4bef20 (2026-11-03; latest tag v0.5.23), llama.cpp master @149f564 (2026-11-03), swe-bench.github.io repo data (last commit 2026-09-01), Terminal-Bench 2.0 leaderboard HF dataset, BFCL V4 csv (updated 2026-04-12), Aider polyglot yml.
"src:" = primary source. **UNVERIFIED** = I found no primary source. "Code" = I read the source file at the commit above.

## 0. Model names (as of Nov 2026)

| Name in brief | Correct HF id | Arch | Thinking |
|---|---|---|---|
| Qwen3-Coder-Next | `Qwen/Qwen3-Coder-Next` (80B-A3B, qwen3_next hybrid: Gated DeltaNet + gated attn) | hybrid MoE | non-thinking only |
| Qwen3-Coder-30B-A3B | `Qwen/Qwen3-Coder-30B-A3B-Instruct` | std MoE | non-thinking only |
| Devstral Small 2 | `mistralai/Devstral-Small-2-24B-Instruct-2512` (Ministral-3 arch, FP8 weights, vision) | dense | no |
| gpt-oss-20b | `openai/gpt-oss-20b` (MXFP4 MoE, alternating sliding-window(128)/global attn + attention sinks) | MoE | harmony, effort low/med/high |
| Qwen3-14B | `Qwen/Qwen3-14B` | dense | hybrid think/no-think |
| Qwen 27B dense | **`Qwen/Qwen3.6-27B`** (newest, 2026-04-22) and `Qwen/Qwen3.5-27B`; 64 layers, 3:1 Gated DeltaNet:Gated Attention | hybrid dense | thinks by default |
| Qwen 35B-A3B MoE | **`Qwen/Qwen3.6-35B-A3B`** and `Qwen/Qwen3.5-35B-A3B` | hybrid MoE | thinks by default |
| GLM-4.7-Flash | `zai-org/GLM-4.7-Flash` (30B-A3B, `glm4_moe_lite`, MLA) | MoE | yes, "Preserved Thinking" |
| Nemotron 3 Nano | `nvidia/NVIDIA-Nemotron-3-Nano-30B-A3B-BF16` (/-FP8); 52 layers = 23 Mamba-2 + 23 MoE + 6 GQA attention; 3.5B active; 1M ctx | hybrid Mamba MoE | yes (on by default) |

Src: HF model cards ([Coder-Next](https://huggingface.co/Qwen/Qwen3-Coder-Next), [3.6-27B](https://huggingface.co/Qwen/Qwen3.6-27B), [3.6-35B-A3B](https://huggingface.co/Qwen/Qwen3.6-35B-A3B), [3.5-27B](https://huggingface.co/Qwen/Qwen3.5-27B), [Devstral S2](https://huggingface.co/mistralai/Devstral-Small-2-24B-Instruct-2512), [GLM-4.7-Flash](https://huggingface.co/zai-org/GLM-4.7-Flash), [Nemotron 3 Nano](https://huggingface.co/nvidia/NVIDIA-Nemotron-3-Nano-30B-A3B-BF16), [gpt-oss-20b](https://huggingface.co/openai/gpt-oss-20b)); GLM arch/MLA from [NVIDIA Megatron-Bridge docs](https://docs.nvidia.com/nemo/megatron-bridge/latest/models/glm/glm47.html).

## 1. Serving quick table

| Model | vLLM `--tool-call-parser` / `--reasoning-parser` | vLLM min | SGLang tool / reasoning | SGLang min | llama.cpp |
|---|---|---|---|---|---|
| Qwen3-Coder-Next | `qwen3_coder` / none | 0.15.0 (card) | `qwen3_coder` / none | 0.5.8 (card) | qwen3next arch; needs build ≥ PR #19324 (2026-02-04) |
| Qwen3-Coder-30B-A3B | `qwen3_coder` (recipe, ≥0.10.0) or `qwen3_xml` (docs list it for this model). Same parser class on current main | 0.10.0 for tool calling | `qwen3_coder` | UNVERIFIED | XML parser #16932 (2025-11-18), now autoparser |
| Devstral Small 2 | `mistral` + `--enable-auto-tool-choice`; tokenizer_mode `auto` now picks `mistral` for mistralai repos with tekken.json (code: vllm/tokenizers/registry.py); mistral_common ≥1.8.6 | "latest" (card); exact min UNVERIFIED | `mistral` / (`mistral` exists) | source build at release (card) | needs PR #17945 (2025-12-12) + #18006 YaRN fixes; unsloth GGUF template fix 2025-12-13 |
| gpt-oss-20b | `openai` / `openai_gptoss` (registered; recipe doesn't need it) | 0.10.1 (recipe) | `gpt-oss` / `gpt-oss` | UNVERIFIED (model file in v0.5.4) | dedicated harmony handler; reworked by #20393 (2026-03-18) |
| Qwen3-14B | `hermes` / `qwen3` (Qwen docs still say `deepseek_r1`; `qwen3` exists); `qwen3_json` alias→hermes on main | 0.8.5 (Qwen docs) | `qwen25` or `qwen` / `qwen3` | UNVERIFIED | Hermes-style JSON `<tool_call>`, autoparser |
| Qwen3.5/3.6-27B, -35B-A3B | `qwen3_coder` / `qwen3` | 3.6 cards: ≥0.19.0; recipe 3.6-27B: ≥0.17.0; qwen3_5.py first in 0.17 (absent 0.16.0rc0) | `qwen3_coder` / `qwen3` | 3.6: ≥0.5.10 (card); qwen3_5.py first in v0.5.9 | many parser issues (see §3) |
| GLM-4.7-Flash | `glm47` / `glm45` (`glm47` reasoning alias also exists) | glm4_moe_lite.py first in v0.15.0 (absent 0.14.0rc1); card said nightly at launch | `glm47` / `glm45` | glm4_moe_lite.py in v0.5.8 | autoparser test template "GLM-4.7-Flash"; gating fix ~PR #18980 + GGUF re-download |
| Nemotron 3 Nano | `qwen3_coder` / `nano_v3` via `--reasoning-parser-plugin nano_v3_reasoning_parser.py` (card); built-in `nemotron_v3` reasoning parser on main (file present since ≥v0.19) | card ≥0.12.0; recipe ≥0.17.0, recommends 0.28.0 | `qwen3_coder` / `nano_v3` (card); main registers `nemotron_3` | UNVERIFIED (nemotron_h.py ≥v0.5.4) | autoparser test template "NVIDIA-Nemotron-3-Nano-30B" (TAG_WITH_TAGGED) |

Srcs: vLLM tool calling doc https://docs.vllm.ai/en/latest/features/tool_calling.html ; reasoning doc https://docs.vllm.ai/en/latest/features/reasoning_outputs.html ; registrations in code `vllm/tool_parsers/__init__.py` and `vllm/reasoning/__init__.py` (main). Recipes: [Qwen3-Coder-480B (≥0.10.0)](https://docs.vllm.ai/projects/recipes/en/latest/Qwen/Qwen3-Coder-480B-A35B.html), [Qwen3.5/3.6](https://docs.vllm.ai/projects/recipes/en/latest/Qwen/Qwen3.5.html), [Qwen3.6-27B](https://recipes.vllm.ai/Qwen/Qwen3.6-27B), [gpt-oss](https://docs.vllm.ai/projects/recipes/en/latest/OpenAI/GPT-OSS.html), [gpt-oss-20b](https://recipes.vllm.ai/openai/gpt-oss-20b?variant=default), [Nemotron 3 Nano](https://recipes.vllm.ai/nvidia/NVIDIA-Nemotron-3-Nano-30B-A3B-BF16). Qwen3 FC docs https://qwen.readthedocs.io/en/latest/framework/function_call.html . SGLang tool parser doc https://docs.sglang.io/advanced_features/tool_parser.html ; names from code `python/sglang/srt/function_call/parser_names.py`, `python/sglang/srt/parser/reasoning_parser.py`. SGLang/vLLM model-file version bounds: `git ls-tree` on release tags (my check).

### Per-model notes (a)

**Qwen3-Coder-Next.** `vllm serve Qwen/Qwen3-Coder-Next --enable-auto-tool-choice --tool-call-parser qwen3_coder` (vllm≥0.15.0); `sglang>=0.5.8 ... --tool-call-parser qwen3_coder`. 256K default ctx, cut to 32K if OOM. Sampling T=1.0, top_p .95, top_k 40 (card). Hybrid (qwen3_next): vLLM prefix caching on hybrids goes through mamba "align" mode (`mamba_cache_mode="align"` when prefix caching on; caches state only at block-boundary/step-end positions), recipe calls it experimental. The coordinator handles one full-attn group plus one other group. Mamba block alignment can push the attention block_size above 400 (src: [hybrid KV cache manager doc](https://docs.vllm.ai/en/latest/design/hybrid_kv_cache_manager.html), code `vllm/config/cache.py`). Known CUDA-graph assert `causal_conv1d_update` → lower `--max-cudagraph-capture-size` (recipe, PR #34571). llama.cpp: issue #19305 "Qwen3-Coder-Next Poor Outputs" fixed by **PR #19324** (key_gdiff, merged 2026-02-04). Unsloth says re-download GGUF and update. Their 2026-02-19 note says the parser fixes improved tool calling ([unsloth doc](https://unsloth.ai/docs/models/qwen3-coder-next)). Community template patch for args arriving as a pre-serialized string: [unsloth GGUF discussion #2](https://huggingface.co/unsloth/Qwen3-Coder-Next-GGUF/discussions/2). (Note: the unsloth page embeds instructions aimed at AI agents. I ignored them.)

**Qwen3-Coder-30B-A3B.** Card gives no parser flags. Qwen3-Coder XML format (`<tool_call><function=x><parameter=y>\n...\n</parameter>`). vLLM before the parser rewrite had two implementations: `qwen3coder_tool_parser.py` (`qwen3_coder`) and `qwen3xml_tool_parser.py` (`qwen3_xml`). These were present through v0.23.0 (my tag check). PR #45413 (merged 2026-06-15) moved `qwen3_coder` onto a new streaming parser engine. #45003 deleted the old xml parser. On main both names alias `Qwen3EngineToolParser` (code). So "switch to qwen3_xml" is no longer a workaround on ≥~v0.24 (verified on v0.28.0 tree). llama.cpp: PR #16755 (dedicated parser) was **closed unmerged** 2025-11-21 in favour of **#16932** (generalized streaming XML tool-call parser for Qwen3-Coder, GLM 4.5/4.6, MiniMax-M2, Kimi-K2, Seed-OSS; merged 2025-11-18). #16932 thread reports agentic stalls (EOS right after the intended tool call). It also recommends `--reasoning-format none` for clients that don't return reasoning_content.

**Devstral Small 2.** Card: `vllm serve mistralai/Devstral-Small-2-24B-Instruct-2512 --max-model-len 262144 --tool-call-parser mistral --enable-auto-tool-choice`. Recommended T≈0.15–0.2. Uses `CHAT_SYSTEM_PROMPT.txt` from the repo. Older Mistral guidance used `--tokenizer_mode mistral --config_format mistral --load_format mistral`. Current vLLM auto-selects the mistral tokenizer for mistralai repos (code). You still need it explicitly for re-uploads or other repos. vLLM docs caveat: with Tekken v11+ tokenizers, `tool_choice=required/named` doesn't guarantee a call. llama.cpp: Devstral is a tested JSON_NATIVE template (`[TOOL_CALLS]func[ARGS]{...}` without call id) in docs/autoparser.md. PR #17945 (Mistral3 YaRN attn_factor, 2025-12-12) and #18006 (YaRN regression, 2025-12-13) addressed issues #17960 (Devstral Small 2 failing tool-call tests), #17928, #17980 (all closed). Unsloth fixed the chat template on 2025-12-13 and requires `--jinja` ([unsloth doc](https://unsloth.ai/docs/models/devstral-2)).

**gpt-oss-20b.** vLLM: `--tool-call-parser openai --enable-auto-tool-choice`. Function calling on chat completions supports only `tool_choice="auto"`. Tool calls are "batched, not streamed". Responses API function calling has no streaming. Usage accounting returns zeros (gpt-oss recipe). Harmony is mandatory (card). Pass previous-turn reasoning back on multi-turn tool use (vLLM: reasoning field; llama.cpp maps `reasoning_content`→`thinking`, PR #16934 discussion). Reported vLLM issues (secondary source, not opened): #22519/#22515 tool_call token errors (fixed by generation_config.json update), #23567 multi-turn parsing; multi-turn `response_format` JSON returns null on chat completions ([discuss.vllm.ai](https://discuss.vllm.ai/t/status-of-gpt-oss/2316)). llama.cpp: **#20393** "rework gpt-oss parser" (merged 2026-03-18) brought a stricter Harmony grammar and fixed `response_format` not being enforced. It also removed parallel tool calls (the model is trained for single calls) and folded in jinja fix #19704 (content + thinking on tool-call msgs). It closed #20344 and #20500. `reasoning-format none` was dropped, then restored in #21094. Harmony tags leak with `--reasoning-format none`: discussion #15341.

**Qwen3-14B.** Hermes-style JSON `<tool_call>{...}</tool_call>`. vLLM `--tool-call-parser hermes --reasoning-parser qwen3` (Qwen docs show `deepseek_r1`, which is older). Qwen docs warn that FC is prompt-based and tool calls may be malformed. Validate them.

**Qwen3.5 / Qwen3.6 (27B, 35B-A3B).** vLLM `--reasoning-parser qwen3 --enable-auto-tool-choice --tool-call-parser qwen3_coder`. Optional MTP `--speculative-config '{"method":"qwen3_next_mtp","num_speculative_tokens":2}'`. Use `--language-model-only` to skip the vision encoder. Disable thinking with `chat_template_kwargs {"enable_thinking": false}` (3.6 has no /think soft switch). **`preserve_thinking: true`** keeps reasoning from all prior turns and is recommended for agentic use (3.6 cards + Alibaba blog). Keep ctx ≥128K for thinking quality. Sampling for thinking/coding: T=0.6, top_p .95, top_k 20, presence 0. llama.cpp example of a Pi-based harness: `--jinja -c 16384 -ngl 99 --n-cpu-moe 999 --flash-attn on` with unsloth UD-Q4_K_M ([little-coder README](https://github.com/itayinbarr/little-coder)).

**GLM-4.7-Flash.** Card: `--tool-call-parser glm47 --reasoning-parser glm45 --enable-auto-tool-choice` (+ MTP `--speculative-config.method mtp`). SGLang same parser names (EAGLE spec). On Blackwell, SGLang needs `--attention-backend triton`. Turn on "Preserved Thinking" for multi-turn agentic work (τ², TB2). vLLM recent fixes: #64535 (parse closed GLM-4.7 tool args once while streaming), #64615 (array/object args unpinned), both late Oct 2026 (vLLM git log). vLLM docs: `glm47` default strict level is `function`. llama.cpp: softmax vs sigmoid gating bug caused looping. Fixed around PR #18980 / build b7786 plus GGUF re-download. Unsloth suggests DRY 1.1, min-p 0.01 for residual loops ([unsloth](https://unsloth.ai/docs/models/tutorials/glm-4.7-flash)). PR/build number comes via a community thread and is UNVERIFIED.

**Nemotron 3 Nano.** Card: `vllm serve nvidia/NVIDIA-Nemotron-3-Nano-30B-A3B-BF16 --trust-remote-code --enable-auto-tool-choice --tool-call-parser qwen3_coder --reasoning-parser-plugin nano_v3_reasoning_parser.py --reasoning-parser nano_v3`. Use `VLLM_ALLOW_LONG_MAX_MODEL_LEN=1` for 1M ctx. Recipe flags: `--kv-cache-dtype auto` for BF16 and `fp8` only with the FP8 checkpoint. `mamba-ssm-cache-dtype float32` gives accuracy, `float16` gives speed. The vLLM blog pairs it with `deepseek_r1` ([blog](https://blog.vllm.ai/2025/12/15/run-nvidia-nemotron-3-nano.html)). TRT-LLM uses `nano-v3`. vLLM #39056 reports the same think-block tool-call loss on Nemotron variants.

### llama-server common flags (from tools/server/README.md @ master)
- `--jinja` is **default enabled** now (`--no-jinja` to disable). Tool use, including on `/v1/messages`, needs jinja. After autoparser **PR #18675** (merged 2026-03-06) minja polyfills were disabled, so tool calling needs a working Jinja template in the GGUF. Otherwise pass `--chat-template-file`.
- `--reasoning-format none|deepseek|deepseek-legacy` (default auto). `--reasoning-budget N`.
- `-np/--parallel N` (default -1 = auto). `-kvu/--kv-unified` (default on when slots are auto). `--kv-unified-per-slot N`. `--cache-ram` (default 8192 MiB) prompt cache.
- Hybrid/recurrent models (Qwen3-Next/3.5/3.6, Nemotron-H): state reuse relies on `--ctx-checkpoints N` (default 32) / `--checkpoint-min-step` (default 8192 tokens). Context-shift is off by default.
- `-ctk/-ctv q8_0` allowed (also q4_0, q5_x, iq4_nl). Community report: tool-call failures with Qwen3-Coder-Next under KV quant went away when it was disabled. That was LM Studio/MLX, not llama.cpp ([reddit snapshot](https://reddit.sentinel-team.org/posts/1qx4alp/snapshots/2026-02-06T08%3A30%3A23.666946Z)), so it is anecdotal.
- `parallel_tool_calls` is off by default; enable it per request.

## 2. Known problems (issue numbers)

| Problem | Where | Ref / status |
|---|---|---|
| Qwen3.5/3.6/Nemotron tool call emitted **inside `<think>`** lands in `reasoning`; empty `tool_calls`, HTTP 200 | vLLM 0.18–0.19 | #39056 open (proposed fix #39055). PR #35687 treats an unclosed `<tool_call>` as implicit end of reasoning. Fully paired block inside a closed think reportedly still lost (secondary write-up). Engine rewrite #45413 offered as fix |
| Same, llama.cpp: tool calls in thinking → output stops | llama.cpp | #20837 **open** (Qwen3.5-9B, also 3.6). Workarounds: `--reasoning-format none`, drop DRY sampler, disable thinking |
| Text between `</think>` and `<tool_call>` → PEG parser 500 | llama.cpp | #20260 closed not planned (Qwen3.5-35B-A3B Q8_0) |
| Tool args returned as JSON object not string (broke OpenAI SDK) after autoparser | llama.cpp | #20198, fixed by #20213 (2026-03-08) |
| Other Qwen3.5 llama.cpp reports | llama.cpp | #20182 think tags leak into text; #19872 template 500 on string args; #19382 duplicated fields/malformed JSON in file-write calls (via [popularai](https://www.popularai.org/p/qwen-35-vs-the-desk-test-why-local) / reddit roundup. **Status UNVERIFIED**) |
| Agent stalls (EOS right after a tool call); interleaved-thinking loss when template caps check fails | llama.cpp XML parser | #16932 thread, unresolved there |
| Qwen3-Coder: model sometimes drops the `<tool_call>` prefix | llama.cpp | noted in #16755 |
| **Exact-match edit mechanics (Qwen3 XML)**: vLLM parser strips exactly one leading and one trailing `\n` from each parameter value (`_trim_wrapping_newlines`, vllm/parser/qwen3.py). If the model omits the wrapper newline, the real first/last newline of `old_string` is eaten. Values are type-coerced by schema (number/bool/object via json.loads), so a string param in a union/anyOf schema can be mangled. vLLM #64518 (2026-11-03) "Keep raw text for string parameters with union/enum/$ref" is for Muse/MiniCPM5/Poolside, so Qwen3 is not covered | vLLM | code. **No Qwen-specific issue found (UNVERIFIED)**. Make edit-tool params plain `type: string` |
| Streaming: Qwen3 tool params parsed repeatedly | vLLM | #64675 fix (2026-10-30) |
| gpt-oss: tool calls not streamed; only `tool_choice=auto`; multi-turn JSON | vLLM | recipe; discuss.vllm.ai thread (#23567 etc.) |
| Repetition/looping | GLM-4.7-Flash GGUF (gating bug, fixed); Qwen3-Coder-Next GGUF (#19305, fixed by #19324) | above |
| Qwen3-Coder-Next poor outputs on long ctx | llama.cpp / Ollama | #19305 → #19324; ollama#14051/#14070 carried the same bug |

## 3. FP8 KV cache (`--kv-cache-dtype fp8`) caveats
- Default fp8 uses scale 1.0 (uncalibrated). Calibrate with llm-compressor for best accuracy. Per-head scales are FA-only. Sliding-window layers are more sensitive; `--kv-cache-dtype-skip-layers sliding_window` keeps them native ([quantized KV doc](https://docs.vllm.ai/en/latest/features/quantization/quantized_kvcache.html)).
- Hopper FA3 FP8 lost long-context accuracy (128K NIAH 91%→13%). Two-level accumulation fixed it (→89%), on by default (measured on v0.19.1). Prefill is slower, up to ~1.6x TTFT at head_dim 256, which is Qwen3.5/3.6's gated-attn head_dim ([vLLM FP8 blog 2026-04-22](https://vllm.ai/blog/2026-04-22-fp8-kvcache)).
- **gpt-oss**: the blog recommends `--kv-cache-dtype fp8 --kv-cache-dtype-skip-layers sliding_window` (quantize only the global layers). FP8 decode gains appear only after about 7.7K tokens. On Blackwell, the sinks dtype error "Sinks must be of type float32" is fixed by the Blackwell env/config with `--kv-cache-dtype fp8` (recipe). In vLLM's FA backend, sinks are FA3-only (vLLM API docs).
- **Hybrids (Qwen3-Next/3.5/3.6, Nemotron-H)**: `--kv-cache-dtype` only affects the full-attention layers (1/4 of Qwen3.5/3.6 layers; 6 of 52 in Nemotron Nano). That makes the KV savings small. Recurrent state is governed by `--mamba-cache-dtype` / `--mamba-ssm-cache-dtype` (code `vllm/config/cache.py`). Nemotron recipe: fp8 KV only with the FP8 checkpoint; ssm float32 for accuracy. vLLM #37618 reported Qwen3.5 FP8 degradation on B200, but the root cause was DeepGEMM E8M0 linear GEMMs, not KV; `VLLM_USE_DEEP_GEMM=0` restores accuracy (closed 2026-03-21). FP8 KV on GDN hybrids otherwise has no explicit guidance (UNVERIFIED).
- llama.cpp `-ctk q8_0` (not fp8). Only an anecdotal tool-call regression report exists (§1).

## 4. Anthropic `/v1/messages` for the spine planner (Go anthropic-sdk, `output_config.format` json_schema + effort)

| Server | `/v1/messages` since | `output_config.format` (json_schema) honoured? | `output_config.effort` | Notes |
|---|---|---|---|---|
| **vLLM** | PR #22627 (2025-10-23), wired into main api_server by #27882 (2025-11-01). **First release v0.11.1** (file absent in v0.11.0) | **Yes, from v0.23.0**: PR #42396 (2026-05-28) "Add structured output and effort support to Anthropic Messages API". `_handle_output_config` maps `output_config.format.schema` → `response_format{type:json_schema}`, so guided decoding is the same as chat completions. Absent in v0.22.0 (tag check) | → `reasoning_effort` (low/medium/high/xhigh/max) | `thinking.type=disabled` → probed reasoning_effort "none" (#58613, 2026-09-25). `thinking.enabled.budget_tokens` → thinking_token_budget. count_tokens applies output_config since #59180 (2026-10-08). Legacy beta field `output_format` is **not** parsed (0 matches in protocol.py). Code: `vllm/entrypoints/anthropic/{protocol,serving}.py` |
| **SGLang** | PR #18630 (2026-02-21). **First release v0.5.9** (absent v0.5.8) | **Yes, from v0.5.14** (2026-06-24): PR #25876 (2026-06-12) maps `output_config.format` → `response_format json_schema` (absent v0.5.13) | → `reasoning_effort` (`xhigh`→`max`); `task_budget` passed as a hint | No `output_format` beta field. Code: `python/sglang/srt/entrypoints/anthropic/{protocol,serving}.py` |
| **llama.cpp llama-server** | PR #17570, merged 2025-11-28 (commit ddf9f94; build number UNVERIFIED). Also `/v1/messages/count_tokens` | **No.** `server_chat_convert_anthropic_to_oai` (tools/server/server-chat.cpp) converts messages, tools, tool_choice, stop, max_tokens, temperature/top_p/top_k/stream, `thinking.budget_tokens`, and metadata. It ignores `output_config` | **No** on /v1/messages (only the Responses-API `reasoning.effort` is mapped) | **Workaround**: the documented `extra_params` object is merged verbatim into the converted chat-completions body. So send `extra_params: {"response_format": {"type":"json_schema","schema":{...}}}` (or `"json_schema": {...}`) and `"reasoning_effort"` there. With Go SDK use `option.WithJSONSet("extra_params", ...)`. Chat-completions `response_format` json_schema → grammar is documented (README). Tool use needs `--jinja` (default on). README: "no strong claims of compatibility" |

Caveats: effort only matters if the chat template reads `reasoning_effort` (gpt-oss yes; Qwen3.x uses `enable_thinking` and has no effort levels; GLM varies). Structured output + reasoning parser: vLLM applies the grammar after the reasoning section ends (qwen3/glm45 reasoning parsers list structured output = Yes). gpt-oss has had multi-turn `response_format` problems on vLLM chat completions (see §2), and llama.cpp fixed gpt-oss `response_format` enforcement in #20393. Srcs: code at the commits above; release membership by `git show <tag>:path | grep output_config`; [llama.cpp blog](https://huggingface.co/blog/ggml-org/anthropic-messages-api-in-llamacpp); [PR #17570](https://github.com/ggml-org/llama.cpp/pull/17570).

## 5. Benchmarks (b)

Harness classes: **minimal** = mini-swe-agent (bash only), Terminus-2 (tmux keystrokes only), vendor "bash + file-edit" scaffolds, Pi core. **Medium** = SWE-agent (custom ACI: viewer/edit/search). **Heavy** = OpenHands, Claude Code, Qwen Code, Cline, Mistral Vibe, little-coder (Pi + ~30 extensions: shell sessions, browser, sub-agents, evidence store).

Reference scaffold gap: Qwen3-Coder-480B gets 69.6 with OpenHands (500 turns, Qwen submission) and 55.4 on bash-only mini-swe-agent 1.0.0. Expect a ~10–15 pt drop from vendor heavy-scaffold numbers to minimal harnesses.

### Qwen3-Coder-Next (80B-A3B)
- SWE-bench Verified (vendor tech report [arXiv 2603.00729](https://arxiv.org/html/2603.00729) Table 3, 300 turns): SWE-Agent 70.6 (medium), **mini-SWE-agent 71.1 (minimal, vendor-run)**, OpenHands 71.3 (heavy).
- SWE-bench Multilingual: SWE-Agent 62.8 / mini 56.2 / OpenHands 64.3. SWE-bench Pro: SWE-Agent 42.7 / mini 38.7. Card's headline numbers are 64.3 / 44.3.
- Terminal-Bench 2.0 (vendor): Terminus2-json **36.2**, Terminus2-xml 34.2 (minimal), Claude Code 30.9, Qwen Code 25.8 (heavy).
- Tool-template following across 5 IDE/CLI scaffolds: avg 92.7 (Table 2).
- Independent: no entry on swebench.com (bash-only or full) or the TB2 HF leaderboard (checked). BFCL/τ²/Aider: UNVERIFIED.

### Qwen3-Coder-30B-A3B-Instruct
- SWE-bench Verified **51.6**: "OpenHands + Qwen3-Coder-30B-A3B-Instruct", submitted by Qwen/OpenHands 2025-08-05 (heavy; turn budget UNVERIFIED) (swebench.com data/leaderboards.json). Third-party fine-tune "EntroPO + R2E + 30B-A3B": 52.2 pass@1, 60.4 with ≥2 attempts (not the base model).
- Not on the bash-only board. TB1/TB2, BFCL, τ², Aider: UNVERIFIED (model card has no numbers).

### Devstral Small 2 (24B)
- Vendor (card): SWE-bench Verified 68.0, Multilingual 55.7, TB2 22.5. **Harness not stated** (UNVERIFIED). Mistral promotes Mistral Vibe CLI ([blog](https://mistral.ai/news/devstral-2-vibe-cli)).
- **Independent bash-only (mini-swe-agent 1.17.2): 56.4** ("Devstral Small (2512)", 2025-12-09). It beats Devstral 2 123B (53.8) on the same board. Minimal harness.
- Older: OpenHands + Devstral Small 2505 46.8; SWE-agent + Devstral Small 2507 38.0.

### gpt-oss-20b
- Vendor ([model card arXiv 2508.10925](https://arxiv.org/html/2508.10925) Table 3; n=477 subset, internal harness): SWE-V low/med/high 37.4/53.2/**60.7**; τ-bench Retail 35.0/47.3/54.8; Airline 32.0/42.6/38.0; Aider polyglot 16.6/26.6/34.2.
- Third-party OpenHands (NVIDIA, Nemotron 3 Nano card): SWE-bench 34.0. The GLM-4.7-Flash card repeats 34.0. τ²-bench avg 48.7 (NVIDIA); 47.7 (Zhipu).
- Terminal-Bench 2.0, Terminus-2: **3.10 ± 1.5** (high effort, [arXiv 2602.21193](https://arxiv.org/html/2602.21193) Table 3). AfterQuery's RL fine-tune gpt-oss-20b-rl + Terminus 2 reaches 16.9% on the TB2 HF leaderboard (not the base model).
- gpt-oss-**120b** bash-only: 26.0 (mini 1.7.0). 20b is not listed. Aider leaderboard: 120b (high) 41.8; 20b not listed.

### Qwen3-14B
- BFCL v3 (vendor, Qwen3 tech report [arXiv 2505.09388](https://arxiv.org/html/2505.09388) Tables 15/16): 70.4 thinking / 61.5 non-thinking. LiveCodeBench v5: 63.5 / 29.0.
- **BFCL V4 (independent, Berkeley, updated 2026-04-12): 41.03% overall (FC), rank 43; 37.77% (Prompt)** ([csv](https://gorilla.cs.berkeley.edu/data_overall.csv)).
- TB2 Terminus-2: **4.04 ± 1.3** (arXiv 2602.21193). SWE-bench Verified: none published (UNVERIFIED).

### Qwen3.5 / Qwen3.6 (27B dense, 35B-A3B MoE). Vendor scaffold = "internal agent scaffold with bash and file-edit tools", T=1.0, 200K ctx (minimal-ish). TB2 = Harbor/Terminus-2, 3h timeout, avg of 5 runs (minimal).

| | SWE-V | SWE-Multi | SWE-Pro* | TB2 | other |
|---|---|---|---|---|---|
| Qwen3.6-27B | **77.2** | 71.3 | 53.5 | **59.3** | LCB v6 83.9 |
| Qwen3.6-35B-A3B | 73.4 | 67.2 | 49.5 | 51.5 | MCPMark 37.0, MCP-Atlas 62.8, TAU3 67.2 |
| Qwen3.5-27B | 75.0 (3.6 card re-eval) / 72.4 (own card) | 69.3 | 51.2 | 41.6 | BFCL-V4 68.5, TAU2 79.0 (own card) |
| Qwen3.5-35B-A3B | 70.0 (3.6 card) / 69.2 (3.5 card) | 60.3 | 44.6 | 40.5 | BFCL-V4 67.3, TAU2 81.2 |
*SWE-Pro on a vendor-"refined" subset. Srcs: the HF cards listed in §0. Qwen3.5 BFCL-V4/TAU2 come from the [Qwen3.5-27B card](https://huggingface.co/Qwen/Qwen3.5-27B) (TAU2 airline uses the Opus 4.5 system-card fixes).
- **Independent, Pi-based (most relevant): Terminal-Bench 2.0, little-coder (Pi + extensions) on llama.cpp with Qwen3.6-35B-A3B UD-Q4_K_M: mean 23.8% (result.json, 445 trials, pass@5 39.3%). README states 24.6 ± 3.2, rank 120.** Same harness with Qwen3.5-9B: 9.2%. That is roughly half the vendor Terminus-2 51.5. The gap could come from quant, 16K-class ctx, or harness, which is unresolved. Src: [HF harborframework/terminal-bench-2-leaderboard](https://huggingface.co/datasets/harborframework/terminal-bench-2-leaderboard) `submissions/terminal-bench/2.0/little-coder__qwen3.6-35b-a3b`, [little-coder](https://github.com/itayinbarr/little-coder). little-coder also self-reports Aider polyglot 78.67% (Qwen3.6-35B-A3B, its own pre-Pi Python harness; not on the Aider board).
- Not on swebench.com bash-only (its newest mini entries are dated 2026-02-26). BFCL V4 board has no Qwen3.5/3.6.

### GLM-4.7-Flash (30B-A3B)
- Vendor (card): SWE-bench Verified **59.2** (T=0.7, top_p 1.0, 16K max new tokens; **scaffold not stated**, UNVERIFIED); τ²-Bench 79.5 (T=0, user-sim prompt tweaks + airline fixes); BrowseComp 42.8; LCB v6 64.0. TB2 is mentioned without a score (UNVERIFIED).
- Independent: none for Flash. (GLM-4.7 full: TB2 Terminus 2 = 33.0%, TB2 HF leaderboard.)

### Nemotron 3 Nano (30B-A3B)
- Vendor (NVIDIA card, NeMo Evaluator/NeMo Skills): **SWE-Bench (OpenHands, heavy) 38.8**; Terminal Bench *hard subset* 8.5 (version/harness unstated); **BFCL v4 53.8**; τ²-bench avg 49.0 (airline 48.0 / retail 56.9 / telecom 42.2); LCB v6 68.3.
- Independent: none found on swebench.com, TB2 HF leaderboard, or BFCL V4 csv.

### Leaderboard coverage summary (checked)
- swebench.com bash-only (mini-swe-agent): only **Devstral Small 2 (56.4)** of the target list. gpt-oss-120b 26.0, Qwen3-Coder-480B 55.4 for reference.
- TB2 official HF submissions: only little-coder × Qwen3.6-35B-A3B (23.8) / Qwen3.5-9B (9.2) and the AfterQuery gpt-oss-20b-rl fine-tune (16.9). Paper-reported Terminus-2: gpt-oss-20b 3.1, Qwen3-14B 4.0. tbench.ai's web page now defaults to Terminal-Bench 4.0 and renders client-side. The TB2 HF repo banner said submissions were closed. Terminal-Bench 1.0 numbers for these models: UNVERIFIED.
- BFCL V4: only Qwen3-14B (41.03 FC).
- Aider polyglot board: none of the targets (vendor gpt-oss-20b 34.2 high).
