# Expanded survey: open-weight models from Chinese labs (≤ ~110B total, for 16–64 GB)

Compiled 2026-11-04. This is a sibling of `other_models.md` and `qwen_models.md`. The models those notes cover are skipped here.

**Method**
- HF data was pulled live: `huggingface.co/api/models?author=<org>` listings, `config.json` and `README.md` through `resolve/main`, and sizes from `api/models/<repo>?blobs=true`.
- GB means 1e9 bytes, summed over the safetensors or GGUF shards.
- Parser and model support was checked against sparse clones of current main:
  - vLLM `cfc9f68` (2026-11-03): `vllm/tool_parsers/__init__.py`, `vllm/reasoning/__init__.py`, `model_executor/models/registry.py`.
  - SGLang `4699fd4` (2026-11-03): `srt/function_call/parser_names.py`, `srt/parser/reasoning_parser_names.py`, `srt/models/`.
  - llama.cpp `149f564` (2026-11-03): `conversion/*.py` (the HF→GGUF converter is now a package) and `common/chat.cpp`.
- "KV/token" values are my arithmetic from config values at BF16 (2 bytes).
- UNVERIFIED means I found no primary source.
- Benchmarks are vendor-reported unless marked otherwise.

---

## 0. Lab-by-lab sweep (what exists, what fits)

| Lab | Newest open models (HF createdAt) | Fits ≤110B? |
|---|---|---|
| DeepSeek | V4-Flash(-0731) 291–304B, V4-Pro 1.6T, V3.2 685B. Small repos are only `dflash_*`/`dspark_*` drafters (1–3B, 2026-06-28), which are spec-decode heads for Qwen3/Gemma 4, not chat models | **None.** No Lite/distill since 2025 |
| Moonshot | Kimi-K3 2.78T, K2.7-Code 1.03T, K2.6/K2.5 1.03T, **Kimi-Linear-48B-A3B** (2025-10-30), **Kimi-Dev-72B** (2025-06-16) | Kimi-Linear, Kimi-Dev |
| MiniMax | M2/M2.1/M2.5/M2.7 all 228.7B. `other_models.md` lists M3.1-Flash at 220B | **None** |
| Zhipu | GLM-5.x ≥321B (5.3-Flash 321B). **GLM-4.5-Air** 110.5B (2025-07). GLM-4.6V 107.7B (VLM, 2025-12). GLM-4.6V-Flash 10.3B (VLM). There is **no "GLM-4.6-Air" or "4.7-Air"** under zai-org | 4.5-Air/4.6V only at 64 GB with no room for KV (see §6) |
| Baidu | **ERNIE-4.5-21B-A3B-Thinking** (2025-09-08), ERNIE-4.5-VL-28B-A3B-Thinking. No open ERNIE 5 text model on HF | ERNIE-4.5-21B-A3B-Thinking |
| Tencent | Hy3 299B, Hy4-preview 780B. **Hunyuan-A13B-Instruct** 80.4B (2025-06). Hy-*-30B-A3B are MT/memory specialists | Hunyuan-A13B |
| ByteDance Seed | **Seed-OSS-36B-Instruct** (2025-08-20), Stable-DiffCoder-8B (diffusion, 2026-01). No Seed-Coder update | Seed-OSS-36B |
| inclusionAI | Ling-3.1-flash 572B, Ling-3.0-flash 127.5B (too big), **Ling-3.0-tiny** 7.9B (2026-08-10), Ling/Ring-flash-2.0 102.9B, Ring-flash-linear-2.0 104B, Ling/Ring-mini-2.0 16.3B (2025-09) | tiny, mini, flash-2.0 (tight) |
| Meituan | LongCat-2.0 1.78T, Flash 562B. **LongCat-Flash-Lite** 69.1B (2026-01-27). **LongCat-Flash-Lite-Sparse** 69.1B (2026-07-31) | Lite, Lite-Sparse |
| Xiaomi | MiMo-V2-Flash 310B, V2.5/V2.6-Flash 311B, Pro 1T. There is no "MiMo Code" repo | **None** |
| StepFun | Step-3.5-Flash 199B, Step-5-Preview 604B | **None** |
| Kwaipilot | **KAT-Coder-V2.5-Dev** 34.7B (2026-07-23), KAT-Dev-72B-Exp (2025-10-10), KAT-Dev 32B (2025-09-15) | all |
| Kuaishou Klear | Klear-46B-A2.5B-Instruct (2025-09) | yes, but no vLLM/SGLang/llama.cpp support |
| Skywork | Skywork-SWE-32B (2025-06-11). Nothing newer for code | yes (old) |
| InternLM | Intern-S2-Preview 36.1B (Qwen3.5-35B-A3B arch, science, 2026-05), Intern-S2-Mobius 36B, JanusCoder-14B (2025-10), Intern-S2-397B | yes, but science-focused with no agentic coding evals |
| Huawei openPangu | **openPangu-2.0-Flash** ~92B/A6B (100.1B safetensors count, 2026-06-30), openPangu-R-72B-2512, 2.0-Pro 541B | Flash, R-72B |
| iFlytek / 01.AI | iFlytek: nothing relevant on HF. 01.AI: last release Yi-Coder (2024-08) | — |

Sources: HF author listings (`/api/models?author=…&sort=createdAt`), run 2026-11-04.

---

## 1. KAT-Coder-V2.5-Dev — `Kwaipilot/KAT-Coder-V2.5-Dev` ★ top pick

- Card: https://huggingface.co/Kwaipilot/KAT-Coder-V2.5-Dev. The repo was created 2026-07-23. The card says "Following the release of KAT-Coder-V2.5 in July".
- It is a **post-train of Qwen3.6-35B-A3B** (card: "we adopt … Qwen3.6-35B-A3B as the base model", SFT on 127K examples plus 10 epochs of RL). Architecture is identical to the Qwen3.6-35B-A3B already covered: MoE, 35B total / 3B active, HF count 34,660,610,688.
- The release is **text only**. Vision weights are absent, so vLLM needs `--language-model-only`.
- License: **Apache-2.0** (card front-matter).
- Attention: hybrid **Gated DeltaNet + gated full GQA**, `full_attention_interval 4`, `layer_types` = 3×linear then 1×full repeated. Full-attention layers are 3,7,11,…,39 (10 of 40).
- Context: `max_position_embeddings 262144` (256K). The card evals use 256K.
- Thinking is **on by default**. Turn it off with `chat_template_kwargs: {"enable_thinking": false}`; the card recommends temp 0.7, top_p 0.8, top_k 20, presence 1.5 for that mode. There is also an optional `preserve_thinking: true`, which keeps historical reasoning.

config.json `text_config` (verbatim):
```
num_hidden_layers 40 | num_attention_heads 16 | num_key_value_heads 2 | head_dim 256 | hidden_size 2048
full_attention_interval 4 | attn_output_gate true | partial_rotary_factor 0.25
linear_num_key_heads 16 | linear_num_value_heads 32 | linear_key_head_dim 128 | linear_value_head_dim 128 | linear_conv_kernel_dim 4 | mamba_ssm_dtype float32
num_experts 256 | num_experts_per_tok 8 | moe_intermediate_size 512 | shared_expert_intermediate_size 512
max_position_embeddings 262144 | rope_theta 10000000 | mtp_num_hidden_layers 0 | vocab_size 248320
```
- KV/token: 10 × 2 × 2 × 256 × 2 B = **20,480 B (20 KiB)**, so ≈5.0 GiB at 256K. GDN recurrent state is a fixed size per sequence and does not grow with length.
- `mtp_num_hidden_layers 0`: **no MTP head**, unlike stock Qwen3.6, so MTP speculative decoding is not available.

Files:
- BF16 69.33 GB.
- bartowski GGUF (https://huggingface.co/bartowski/Kwaipilot_KAT-Coder-V2.5-Dev-GGUF): Q6_K 30.05 GB, Q5_K_M 25.02 GB, Q4_K_M 21.39 GB, IQ4_XS 18.81 GB, Q8_0 36.91 GB.
- mradermacher GGUF is similar (Q4_K_M 21.17 GB).
- **No unsloth GGUF** (an unsloth search returned none), so there is no UD-Q4_K_XL.
- AWQ: `cyankiwi/KAT-Coder-V2.5-Dev-AWQ-INT4` 24.43 GB.
- No official FP8.

Serving:
- vLLM ≥0.19: `--reasoning-parser qwen3 --enable-auto-tool-choice --tool-call-parser qwen3_coder --language-model-only` (card).
- SGLang ≥0.5.10: `--reasoning-parser qwen3 --tool-call-parser qwen3_coder` (card).
- llama.cpp: `Qwen3_5MoeForConditionalGeneration` is in `conversion/qwen.py`, so it is supported. Use `--jinja`.
  - In discussion #12, a community user runs it with a "Froggeric chat template", and another suggests Unsloth's Qwen template. That suggests the shipped template has the same rough edges as Qwen3.6. UNVERIFIED as a llama.cpp bug.

Benchmarks (vendor, re-run in-house for every model in the table):

| Benchmark | KAT-Coder-V2.5-Dev | Qwen3.6-35B-A3B (in-house) |
|---|---|---|
| SWE-bench Verified | **69.40** | 64.40 |
| SWE-bench Pro | 45.96 | |
| SWE-bench Multilingual | 63.00 | |
| Terminal-Bench **2.1** | **41.02** | |

- SWE-bench harness: **heavy**, `agent=claude_code@2.1.195`, temp 1.0, 256K context.
- Terminal-Bench 2.1 is the average of Terminus-2 32.60 and Claude Code 49.44. No TB 2.0 number is published.
- **Independent check:** HF discussion #12 (https://huggingface.co/Kwaipilot/KAT-Coder-V2.5-Dev/discussions/12) points out that provider-reported numbers for Qwen3.6-35B-A3B (73.4 SWE-V) exceed KAT's 69.4. Anecdotal reports there are mixed but positive, and say it "uses far fewer thinking tokens". No independent SWE-bench or Terminal-Bench run was found.
- The card also notes RL penalties targeting excessive parallel tool calls (>70 in one turn), failed or empty tool calls, and repetition. It reports abnormal tool labels dropping from 9.34% to 0.28%. This is directly relevant to Pi's four-tool loop.

**Fit:** 24 GB GPU with Q4_K_M and about 100K context. 32 GB runs Q5/Q6 at 256K. It is a drop-in replacement for an existing Qwen3.6-35B-A3B deployment.

---

## 2. LongCat-Flash-Lite / LongCat-Flash-Lite-Sparse — `meituan-longcat/LongCat-Flash-Lite`, `meituan-longcat/LongCat-Flash-Lite-Sparse`

- Cards:
  - Lite: https://huggingface.co/meituan-longcat/LongCat-Flash-Lite (tech report arXiv 2601.21204)
  - Sparse: https://huggingface.co/meituan-longcat/LongCat-Flash-Lite-Sparse (arXiv 2608.01662)
- Released 2026-01-27 (Lite) and 2026-07-31 (Sparse). **MIT** license.
- MoE, "68.5B total, ~2.9–4.5B active". The HF count is 69.07B (Lite) and 69.13B (Sparse).
  - **Over 30B of that is N-gram embedding tables.** The MoE has 256 FFN experts plus 128 "zero/identity" experts, top-12.
- Both are **non-thinking** models (card: "a non-thinking … model").
- Attention: **MLA on all layers.** The config says `num_layers 14`, but each LongCat decoder layer holds **two** MLA blocks.
  - transformers `modeling_longcat_flash.py:461`: `self.self_attn = nn.ModuleList([LongcatFlashMLA(...) for i in [0, 1]])`. The Lite ngram model subclasses `LongcatFlashDecoderLayer`.
  - So there are **28 MLA layers**.
- Sparse swaps dense MLA for "LongCat Sparse Attention (LSA)". This is DSA-style top-k with sink and local window, cross-layer index reuse, and hierarchical indexing.
- Context: Lite is 256K via YaRN (`max_position_embeddings 327680`). Sparse is native 1M (`983040`).

config.json Lite (verbatim):
```
num_layers 14 | hidden_size 3072 | num_attention_heads 32 | kv_lora_rank 512 | q_lora_rank 1536
qk_rope_head_dim 64 | qk_nope_head_dim 128 | v_head_dim 128 | n_routed_experts 256 | zero_expert_num 128 | moe_topk 12
expert_ffn_hidden_size 1024 | ffn_hidden_size 6144 | ngram_vocab_size_ratio 78 | emb_neighbor_num 4 | emb_split_num 4
max_position_embeddings 327680 | rope_theta 5000000.0 | rope_scaling {yarn, factor 10, original_max_position_embeddings 32768}
```
Sparse adds:
```
attention_method "LSA" | index_n_heads 16 | index_head_dim 128 | index_topk 2048 | index_local_tokens 1024 | index_init_tokens 16
cli_factor 2 | mtp_num_layers 3 | max_position_embeddings 983040 | rope_scaling {deepseek_yarn, factor 120, original 8192}
```

KV/token (MLA latent): 28 × (512 + 64) × 2 B = **32,256 B (31.5 KiB)**, so ≈7.9 GiB at 256K. Sparse adds indexer keys. Its 1M context needs ≈31 GiB of MLA cache.

Benchmarks (vendor):

| | Lite | Sparse (w/o HI) |
|---|---|---|
| SWE-bench Verified | 54.4 | **68.2** |
| Terminal-Bench 2.0 | 33.75 | **33.7** |
| τ²-Telecom | 72.8 | 95.18 |
| SWE-bench Pro | — | 40.63 |
| SWE-bench Multilingual | — | 59.33 |

- Lite also reports τ²-Retail 73.1 and τ²-Airline 58.0.
- The Sparse table labels Terminal-Bench as "TerminalBench 2.0(acc)". Harness not stated. UNVERIFIED.
- The Lite card's comparison table also gives Meituan-measured numbers for **Kimi-Linear-48B-A3B: SWE-bench 32.8, TerminalBench 20.0**. This is a third-party measurement of Moonshot's model.

Files:
- Lite: BF16 138.18 GB. Official FP8 (`meituan-longcat/LongCat-Flash-Lite-FP8`) 101.76 GB.
- `Intel/LongCat-Flash-Lite-int4-AutoRound` is **84.16 GB**, because the embedding tables stay high-precision. **No vLLM-servable 4-bit build fits 64 GB.**
- GGUF `InquiringMinds-AI/LongCat-Flash-Lite-GGUF`: Q4_K_M 40.23 GB, Q5_K_M 48.01 GB, Q6_K 56.28 GB, Q8_0 72.87 GB. These **need a custom llama.cpp fork** (branch `longcat-flash-ngram`).
  - The card says upstream PRs #19167/#19182 stalled ("identity experts too complex").
  - The fork is AI-generated and not upstreamable.
- Sparse: BF16 138.29 GB, `Intel/…-Sparse-MXFP4-CT-AutoRound` 88.61 GB, MLX 4/6/8-bit, and community GGUFs (abliterated only).

Serving:
- vLLM main registers `LongcatFlashNgramForCausalLM` (Lite).
- vLLM has **no** support for Sparse's `LongcatCausalLM`. SGLang main has neither the Ngram class nor `LongcatCausalLM`.
  - The cards point to SGLang PR #17838 (Lite) and PR #32918 (Sparse), with custom sgl-kernel builds. **Not merged** as of `4699fd4`.
- llama.cpp mainline: not supported.
- **Tool-call caveat:**
  - The Lite chat template tells the model to emit `<longcat_tool_call>{name}\n<longcat_arg_key>k</longcat_arg_key>\n<longcat_arg_value>v</longcat_arg_value>…`, which is XML key/value.
  - vLLM's `longcat` tool parser (`vllm/tool_parsers/longcat_tool_parser.py`, 24 lines) is a `Hermes2ProToolParser` subclass that expects **JSON** inside `<longcat_tool_call>`.
  - The likely result is a format mismatch, so test it before relying on it. Meituan ships its own `parse_model_response.py`.
- **Verdict:** strongest vendor agentic-coding numbers among Chinese models at this size. It is **not practically deployable for Pi on ≤64 GB today**: the only path is a non-upstream llama.cpp fork, and tool parsing is uncertain. Revisit when SGLang/vLLM land Sparse and a compact 4-bit quant exists.

---

## 3. Seed-OSS-36B-Instruct — `ByteDance-Seed/Seed-OSS-36B-Instruct`

- Card: https://huggingface.co/ByteDance-Seed/Seed-OSS-36B-Instruct. Repo created 2025-08-20. **Apache-2.0**.
- **Dense** 36.15B. Full **GQA on all 64 layers**, no sliding window. Native context **512K** (`max_position_embeddings 524288`).
- Thinking is on by default, with a **thinking budget** passed as `chat_template_kwargs: {"thinking_budget": N}`.
  - The template defaults to `-1`, meaning unlimited.
  - `thinking_budget: 0` injects a "thinking budget is 0 … directly start answering" system line. That is the off switch (chat_template.jinja lines 112–116).
  - The card recommends multiples of 512.

config.json (verbatim):
```
num_hidden_layers 64 | num_attention_heads 80 | num_key_value_heads 8 | head_dim 128 | hidden_size 5120
intermediate_size 27648 | max_position_embeddings 524288 | rope_theta 10000000.0 | rope_scaling {rope_type default}
attention_bias true | vocab_size 155136
```
- KV/token: 64 × 2 × 8 × 128 × 2 B = **262,144 B (256 KiB)**. That is ≈32 GiB at 128K, so long context is expensive. Use q8 KV or cap around 64K on 48 GB.

Files:
- BF16 72.30 GB.
- unsloth GGUF: Q6_K 29.67 GB, Q5_K_M 25.59 GB, **UD-Q4_K_XL 22.03 GB**, Q4_K_M 21.76 GB, Q8_0 38.42 GB.
- AWQ: UNVERIFIED (not checked).

Serving:
- vLLM: `--tool-call-parser seed_oss` (card).
  - Main registers `seed_oss` as both tool and reasoning parser.
  - The arch is now served by the **Transformers backend** (`registry.py`: `"SeedOssForCausalLM": ("transformers", "TransformersForCausalLM")`).
- SGLang: no `SeedOss` model class found in `srt/models`. UNVERIFIED whether a generic path works.
- llama.cpp: `SeedOssForCausalLM` is registered (`conversion/olmo.py`). Tool format is `<seed:tool_call>`, handled by the generic autoparser. UNVERIFIED in practice.

Benchmarks (card, vendor):
- SWE-bench Verified **56.0 (OpenHands)** and 47 (Agentless 4×10).
- Multi-SWE-bench 17.
- TAU1-Retail 70.4, TAU1-Airline 46.
- No Terminal-Bench or BFCL numbers.
- It is now 14 months old, and the 2026 MoEs beat it on SWE.

---

## 4. Kimi-Linear-48B-A3B-Instruct — `moonshotai/Kimi-Linear-48B-A3B-Instruct`

- Card: https://huggingface.co/moonshotai/Kimi-Linear-48B-A3B-Instruct (arXiv 2510.26692). Created 2025-10-30. **MIT**.
- MoE: 48B total / 3B active (HF 49.12B). Context 1M (`model_max_length 1048576`). Instruct only, **no thinking mode**.
- Attention: hybrid **KDA (Kimi Delta Attention) + MLA, 3:1**.
  - `full_attn_layers [4,8,12,16,20,24,27]` (7 MLA layers, 1-indexed).
  - `kda_layers` has the other 20.

config.json (verbatim):
```
num_hidden_layers 27 | hidden_size 2304 | num_attention_heads 32 | num_key_value_heads 32 | head_dim 72
kv_lora_rank 512 | qk_rope_head_dim 64 | qk_nope_head_dim 128 | v_head_dim 128 | q_lora_rank null | mla_use_nope true
linear_attn_config {full_attn_layers [4,8,12,16,20,24,27], kda_layers [...20 layers], head_dim 128, num_heads 32, short_conv_kernel_size 4}
num_experts 256 | num_experts_per_token 8 | num_shared_experts 1 | moe_intermediate_size 1024 | first_k_dense_replace 1
```
- KV/token (MLA): 7 × 576 × 2 B = **8,064 B**. Even 1M context is only ≈7.5 GiB.

Files:
- BF16 98.25 GB.
- bartowski GGUF: Q6_K 40.46 GB, Q5_K_M 35.10 GB, Q4_K_M 30.06 GB, IQ4_XS 26.46 GB.
- AWQ `cyankiwi/…-AWQ-4bit` 30.53 GB.

Serving:
- vLLM: registered as `KimiLinearForCausalLM`. The card shows `vllm serve … --trust-remote-code`.
  - Tool format uses Kimi-K2 tokens (`<|tool_calls_section_begin|>` …) per the template, so `--tool-call-parser kimi_k2` should apply. UNVERIFIED; the card does not state it.
- SGLang: `kimi_linear.py` exists.
- llama.cpp: supported (`conversion/kimi_linear.py`).

Benchmarks:
- No coding or agent numbers on the card.
- Meituan's independent measurement (LongCat-Flash-Lite card): **SWE-bench 32.8, TerminalBench 20.0**, τ²-Airline 44.0, τ²-Retail 18.86, τ²-Telecom 15.68.
- **Weak for agentic coding.** It is interesting as an architecture, not a pick.

---

## 5. Ling-3.0-tiny (and Ling/Ring 2.0) — inclusionAI

### Ling-3.0-tiny — `inclusionAI/Ling-3.0-tiny`

- Card: https://huggingface.co/inclusionAI/Ling-3.0-tiny. Repo created 2026-08-10. **MIT**.
- MoE: **7.9B total / 1.3B active**. Hybrid **KDA + MLA 3:1**: `layer_group_size 4`, 24 layers, so 6 MLA layers.
- `max_position_embeddings 131072`. The SGLang recipe uses YaRN ×2 to reach 256K.
- Hybrid reasoning, thinking on by default. Turn it off with `chat_template_kwargs: {"enable_thinking": false}` (card).

config (verbatim subset):
```
num_hidden_layers 24 | hidden_size 1536 | num_attention_heads 16 | num_key_value_heads 16 | head_dim 128
kv_lora_rank 512 | q_lora_rank 256 | qk_rope_head_dim 64 | qk_nope_head_dim 128 | v_head_dim 128 | layer_group_size 4
num_experts 128 | num_experts_per_tok 8 | num_shared_experts 1 | moe_intermediate_size 512 | short_conv_kernel_size 4 | model_type bailing_hybrid
```
- KV/token ≈ 6 × 576 × 2 = **6,912 B**. This is computed under the assumption that 1 in 4 layers is MLA, as the card says.

Files:
- BF16 15.79 GB, official FP8 8.41 GB, official INT4 5.81 GB.
- Official GGUF: Q8_0 8.41, Q6_K 6.50, Q5_K_M 5.64, Q4_K_M 4.82 GB.

Serving:
- vLLM and SGLang: `--tool-call-parser ling3 --reasoning-parser ling3`. Both are registered on main. The vLLM card adds `--mamba-cache-mode align`.
- llama.cpp: `BailingMoeV3ForCausalLM` is in `conversion/bailingmoe3.py`, and `common/chat.cpp` has a dedicated "Ling 3.0 (Bailing V3)" handler.

Benchmarks:
- The card states Artificial Analysis Intelligence Index 25 and Agentic Index 16.
- Terminal-Bench 2.1 figures appear only in an image. UNVERIFIED.
- No SWE-bench in text.
- **Only interesting as a 16 GB-tier fast model**, not a primary coder.

### Ling-flash-2.0 / Ring-flash-2.0 / Ring-flash-linear-2.0 / Ling-mini-2.0 / Ring-mini-2.0

- Created 2025-09, **MIT**.
- `BailingMoeV2ForCausalLM`: full GQA on all layers, `partial_rotary_factor 0.5`, native 32K (128K with YaRN). Ling is instruct; Ring is reasoning.
- flash (102.9B total): `num_hidden_layers 32, num_attention_heads 32, num_key_value_heads 4, head_dim 128`. KV/token 65,536 B.
- mini (16.3B total): `num_hidden_layers 20, num_attention_heads 16, num_key_value_heads 4, head_dim 128`. KV/token 40,960 B.
- Ring-flash-linear-2.0: `layer_group_size 8` linear hybrid. Neither vLLM nor SGLang main has `BailingMoeLinearV2ForCausalLM` (SGLang has only `BailingMoeV2_5`). llama.cpp has no support.

Sizes:
- Ling-flash-2.0 bartowski GGUF: Q4_K_M 62.62, IQ4_XS 55.18, Q5_K_M 73.32 GB. Too tight for 64 GB.
- Ling-mini-2.0 bartowski GGUF: Q4_K_M 9.94, Q6_K 13.39 GB.

Other notes:
- Cards report only LiveCodeBench/CodeForces. **No SWE, TB or BFCL numbers.**
- Tool format is Hermes `<tool_call>`, so the `hermes` parser presumably applies. UNVERIFIED.
- The family has been superseded by Ling 3.x, whose 3.0-flash is 127.5B and too big.

---

## 6. GLM-4.5-Air — `zai-org/GLM-4.5-Air` (borderline: does not really fit 64 GB)

- Created 2025-07-20, **MIT**. MoE 106B / 12B active (HF 110.47B). Full **GQA** on all 46 layers, plus 1 MTP layer. 128K context.
- Hybrid thinking. Turn it off with `chat_template_kwargs: {"enable_thinking": false}` (zai-org/GLM-4.5 README).
- config: `num_hidden_layers 46 | num_attention_heads 96 | num_key_value_heads 8 | head_dim 128 | partial_rotary_factor 0.5 | n_routed_experts 128 | num_experts_per_tok 8 | num_nextn_predict_layers 1 | max_position_embeddings 131072`.
- KV/token 46 × 2 × 8 × 128 × 2 = **188,416 B (184 KiB)**.

Sizes:
- FP8 112.56 GB.
- unsloth GGUF: UD-Q4_K_XL **67.72 GB**, IQ4_XS 60.48, Q4_K_M 72.98, Q6_K 99.01 GB.
- AWQ (cyankiwi) 63.40 GB.
- **Nothing 4-bit leaves room for KV in 64 GB.** You would need about 80–96 GB, or a REAP-pruned 82B variant (`cerebras/GLM-4.5-Air-REAP-82B-A12B`, not evaluated here).

Serving:
- vLLM/SGLang `--tool-call-parser glm45 --reasoning-parser glm45`. The current README now says `glm47` for the tool parser; both are registered in vLLM.
- llama.cpp supports `Glm4MoeForCausalLM`.

Benchmarks (GLM-4.5 tech report, arXiv 2508.06471 Tables 3 and 5, vendor):
- SWE-bench Verified **57.6** (OpenHands v0.34.0, 100 iterations).
- Terminal-Bench (1.0) 30.0 (Terminus).
- BFCL v3 76.4.
- τ-Retail 77.9, τ-Airline 60.8.

GLM-4.6V (107.7B VLM, 2025-12) has the same text architecture and size problem (UD-Q4_K_XL 65.45 GB). There is no Air successor to 4.6 or 4.7; zai went from 4.7-Flash (30B, covered) to GLM-5.x (≥321B).

---

## 7. Other candidates (brief)

| Model | Repo | Arch / size | License | Key numbers | Fit / notes |
|---|---|---|---|---|---|
| KAT-Dev-72B-Exp | `Kwaipilot/KAT-Dev-72B-Exp` (2025-10-10) | Dense Qwen2.5-72B: 80 L, 64 heads, 8 KV, head 128 (KV 320 KiB/token), 128K | Apache-2.0 | SWE-bench Verified **74.6%** "strictly with the SWE-agent scaffold" (card, vendor; temp 0.6, max_turns 150) | GGUF Q4_K_M 47.42, IQ4_XS 39.71, Q6_K 64.35 GB. FP8 75.21 GB. Dense 72B decodes slowly on 48–64 GB. Parsers per KAT-Dev: `qwen3_coder` with a plugin file. Uses Qwen2.5 Hermes format. UNVERIFIED |
| KAT-Dev (32B) | `Kwaipilot/KAT-Dev` (2025-09-15) | Dense Qwen3-32B: 64 L, 8 KV, head 128 | Front-matter says apache-2.0 with `license_name: kwaipilot-license`, but the LICENSE file is missing (404) | SWE-bench Verified 62.4% (card, harness not stated) | Superseded by V2.5-Dev. vLLM: `--tool-parser-plugin qwen3coder_tool_parser.py --tool-call-parser qwen3_coder` |
| Kimi-Dev-72B | `moonshotai/Kimi-Dev-72B` (2025-06-16) | Dense Qwen2.5-72B, 128K | MIT | SWE-bench Verified 60.4 (Agentless-style, vendor) | Not trained for multi-turn tool loops. Unsloth Q6_K 64.35, IQ4_XS 39.75 GB |
| Skywork-SWE-32B | `Skywork/Skywork-SWE-32B` (2025-06-11) | Dense Qwen2.5-Coder-32B, **32K ctx** | Apache-2.0 | SWE-bench Verified 38.0 (OpenHands v0.32), 47.0 with Bo8 | Obsolete |
| ERNIE-4.5-21B-A3B-Thinking | `baidu/ERNIE-4.5-21B-A3B-Thinking` (2025-09-08) | MoE 21.8B / 3B: 28 L, 20 heads, 4 KV, head 128 (KV 56 KiB/token), 128K | Apache-2.0 | No SWE/TB. "Efficient tool usage" | Thinking-only, with long CoT per the card. Card says `--reasoning-parser ernie_x1 --tool-call-parser ernie_x1`, but vLLM main registers these as **`ernie45`**. Unsloth UD-Q4_K_XL 13.40, Q6_K 17.97 GB. llama.cpp supported |
| Hunyuan-A13B-Instruct | `tencent/Hunyuan-A13B-Instruct` (2025-06-25) | MoE 80.4B / 13B: 32 L, 32 heads, 8 KV, head 128, GQA (KV 128 KiB/token), 256K | **tencent-hunyuan-a13b. Excludes EU, UK and South Korea**, plus a >100M MAU clause | BFCL v3 78.3, τ-Bench 54.7 (card) | `/no_think` or `enable_thinking=False`. vLLM main: `hunyuan_a13b` tool and reasoning parsers, arch via Transformers backend. SGLang: `hunyuan`. Unsloth UD-Q4_K_XL 46.69, Q5_K_M 57.12 GB. FP8 80.93 GB |
| openPangu-2.0-Flash | `openpangu/openPangu-2.0-Flash` (2026-06-30) | MoE ~92B / ~6B (HF 100.1B). MLA with **DSA + SWA hybrid 1:2** (`dsa_layers` every 3rd; `swa_layers` window 512, last few 2048), 4-stream mHC, 3-head MTP, 512K | **OpenPangu Model License v2.0. Forbids any use "within the European Union"**, plus a "Powered by openPangu" attribution requirement | SWE-bench Verified 63.1 (thinking) / 57.6 (non-thinking), TAU2 88.0, PinchBench 85.6 (card, Avg@3, harness not stated) | Ascend/omni-infer only. No vLLM, SGLang or llama.cpp main support (`OpenPanguV2ForCausalLM` absent). Community GGUF needs forks (`mrexodia` Q4_K_M 55.28 GB). Not practical |
| Klear-46B-A2.5B-Instruct | `Kwai-Klear/Klear-46B-A2.5B-Instruct` (2025-09) | MoE 46.2B / 2.5B: 32 L, 4 KV, 64K | Apache-2.0 | No agentic evals | `KlearMoeForCausalLM` has no support in vLLM, SGLang or llama.cpp. Skip |
| Intern-S2-Preview | `internlm/Intern-S2-Preview` (2026-05-15) | Qwen3.5-35B-A3B architecture (same config shape as KAT-Coder), science-tuned | Apache-2.0 | No coding or agent evals | FP8 38.79 GB. Not a coder |
| GLM-4.6V-Flash | `zai-org/GLM-4.6V-Flash` (2025-12-07) | ~9B dense VLM | MIT | — | Below the useful tier for Pi coding |

Excluded as too large (≥127B even at 4-bit):
- DeepSeek V4-Flash 291B, MiniMax M2.x 229B, MiMo-V2(.5/.6)-Flash 310B, Step-3.5-Flash 199B, Hy3 299B, GLM-5.3-Flash 321B.
- Ling-3.0-flash 127.5B. inclusionAI also posted FP4 variants (Fin-fp4 65.6, VL-fp4 64.5 by HF count) that are domain or VL variants only.
- LongCat-Flash 562B, Kimi K2.x 1T.

---

## 8. Ranked shortlist for Pi (4 tools, OpenAI-compatible tool calls, 16–64 GB)

1. **KAT-Coder-V2.5-Dev** (35B-A3B, Apache-2.0).
   - The only 2026 Chinese coding-agent release in this size class that is drop-in servable everywhere: vLLM/SGLang `qwen3_coder` + `qwen3`, and llama.cpp.
   - Vendor SWE-V 69.4 / TB 2.1 41.0 with heavy harnesses.
   - Its RL explicitly penalizes bad tool calls, but independent evidence is thin and mixed.
   - A/B it directly against Qwen3.6-35B-A3B on the same stack. Q4_K_M 21.4 GB fits 24–32 GB with 128–256K context (20 KiB/token).
2. **Seed-OSS-36B-Instruct** (dense 36B, Apache-2.0).
   - Mature parser support (`seed_oss`) and a clean thinking off-switch (`thinking_budget: 0`).
   - SWE-V 56 (OpenHands).
   - Heavier KV (256 KiB/token), so it suits the 32–48 GB tier at ≤64K context. Older and weaker than item 1, but a different lineage (not Qwen-derived), which helps diversity.
3. **LongCat-Flash-Lite-Sparse** (69B-A3B, MIT). Watch-list item; best numbers, worst serving story.
   - Vendor SWE-V 68.2, TB 2.0 33.7, τ²-Telecom 95. Non-thinking, so latency is cheap.
   - Blocked today on: Sparse is not in vLLM or SGLang main; 4-bit vLLM quants are 84–89 GB because the embeddings are not quantized; llama.cpp needs a fork; the vLLM `longcat` parser expects JSON while the template emits XML arg tags.
4. **KAT-Dev-72B-Exp** (dense 72B, Apache-2.0). Only for 48–64 GB rigs that tolerate dense-72B decode speed.
   - Vendor SWE-V 74.6 with SWE-agent, the highest claimed number here. October 2025, Qwen2.5 base.
   - Q4_K_M 47.4 GB leaves about 12 GB of KV (≈38K tokens at 320 KiB/token). Tight.
5. **Ling-3.0-tiny** (7.9B-A1.3B, MIT). 16 GB-tier or fast-subagent option only.
   - First-class `ling3` tool and reasoning parsers in vLLM, SGLang and llama.cpp. 7 KiB/token KV.
   - No published SWE-bench; expect weak results on real repo tasks.

Not recommended:
- Kimi-Linear-48B: independently measured SWE 32.8 / TB 20.
- GLM-4.5-Air: does not fit 64 GB with KV.
- Hunyuan-A13B: territory-restricted license; 2025-era.
- openPangu-2.0-Flash: EU-prohibited license; Ascend-only serving.
- ERNIE-4.5-21B-A3B-Thinking: no agentic coding evals; thinking-only.
- Ling/Ring-2.0: no agentic evals; superseded.
