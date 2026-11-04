# Other local models (non-Qwen-family focus): verified facts + config values

Compiled 2026-11-03. Sources: HF config.json / README / file listing pulled live via `huggingface.co/<repo>/resolve/main/...` and `huggingface.co/api/models/<repo>?blobs=true`. Sizes are bytes from the HF API (GB = 1e9). "Computed" = my arithmetic from config values, not a published number. UNVERIFIED = no primary source found.

---

## 1. Devstral Small 2: `mistralai/Devstral-Small-2-24B-Instruct-2512`

- Repo: https://huggingface.co/mistralai/Devstral-Small-2-24B-Instruct-2512 (this is the exact name. No newer Devstral Small exists on HF as of 2026-11-03. Mistral's later releases are Mistral-Small-4-119B-2603, Leanstral-1.5-119B-A6B, and Mistral-Large-4. None is a ≤80B coding model.)
- Release: December 9, 2025 (Mistral blog "Introducing: Devstral 2 and Mistral Vibe CLI", https://mistral.ai/news/devstral-2-vibe-cli). The HF repo was created 2025-11-28.
- Dense, 24B. The HF safetensors count is 24,011,361,280 params (F8_E4M3 22.23B + BF16 1.78B). Base model is Mistral-Small-3.1-24B-Base-2503 (card front-matter). It includes a Pixtral vision encoder (24 layers, hidden 1024), so it accepts image input.
- Attention: full GQA on all 40 layers. `sliding_window: null`. Card: "same architecture as Ministral 3 using rope-scaling as introduced by Llama 4 and Scalable-Softmax" (attention-softmax temperature).
- Context: 256k (card "A 256k context window"). Config `max_position_embeddings: 393216`, YaRN factor 48 from 8192.
- License: Apache-2.0 (card + blog. Devstral 2 123B uses a modified MIT license instead).
- Reasoning: none. It is an instruct/agentic model with no thinking mode (the card describes no thinking toggle). The card recommends temperature 0.15. Tool calling uses vLLM `--tool-call-parser mistral --enable-auto-tool-choice`.
- Benchmarks (card): SWE-bench Verified 68.0%, SWE-bench Multilingual 55.7%, Terminal Bench 2 22.5%.
- Official weights are **FP8** (`quant_method: fp8`, `activation_scheme: static`. The vision tower, projector and lm_head stay BF16). There is no official BF16 repo.

config.json `text_config` (verbatim):
```
num_hidden_layers 40 | num_attention_heads 32 | num_key_value_heads 8 | head_dim 128
hidden_size 5120 | intermediate_size 32768 | vocab_size 131072
max_position_embeddings 393216 | sliding_window null | dtype bfloat16 (top-level "dtype")
rope_parameters: {rope_type yarn, factor 48.0, original_max_position_embeddings 8192,
  rope_theta 100000000.0, beta_fast 32.0, beta_slow 1.0, llama_4_scaling_beta 0.1, mscale 1.0, mscale_all_dim 1.0}
tie_word_embeddings false; architectures ["Mistral3ForConditionalGeneration"], text model_type "ministral3"
```
params.json (verbatim keys): `dim 5120, n_layers 40, head_dim 128, hidden_dim 32768, n_heads 32, n_kv_heads 8, rope_theta 1e8, vocab_size 131072, max_position_embeddings 393216, tied_embeddings false, llama_4_scaling {original_max_position_embeddings 8192, beta 0.1}, yarn {original_max_position_embeddings 8192, factor 48, apply_scale false, beta 32, alpha 1}, q_lora_rank/kv_lora_rank/qk_rope_head_dim null (no MLA), quantization {qformat_weight fp8_e4m3, qscheme_act TENSOR}`.

KV cache (computed): 40 × 2 × 8 × 128 × 2 B = **163,840 B/token (160 KiB) at BF16**. That is ≈20.0 GiB at 128K and ≈40 GiB at 256K.

Files:
- Official FP8: `consolidated-0000{1,2}-of-00002.safetensors` = 19,839,869,982 + 5,953,165,906 = **25.79 GB**. The HF-format `model-0000x-of-00006` is a duplicate copy of ≈25.79 GB. The repo total is 51.6 GB because it ships both formats.
- unsloth GGUF (https://huggingface.co/unsloth/Devstral-Small-2-24B-Instruct-2512-GGUF): Q6_K 19,346,476,192 (19.35 GB). Q5_K_M 16,764,521,632 (16.76 GB). UD-Q4_K_XL 14,506,151,072 (14.51 GB). Q4_K_M 14.33 GB. Q8_0 25.06 GB. UD-Q6_K_XL 20.79 GB. BF16 47.15 GB. mmproj-F16 0.88 GB (vision, optional).
- AWQ: `cyankiwi/Devstral-Small-2-24B-Instruct-2512-AWQ-4bit`, model.safetensors 16,067,524,312 (16.07 GB; ships both consolidated and HF copies). Others: `androiddrew/...-AWQ-4bit`, `Firworks/...-nvfp4`, `btbtyler09/...-INT4-INT8-Mixed-GPTQ`, `levara/Devstral-Small-2-24B-TextOnly-FP8`.

---

## 2. `openai/gpt-oss-20b`

Primary sources:
- HF card/config: https://huggingface.co/openai/gpt-oss-20b
- Model card PDF (arXiv 2508.10925, dated August 5, 2025): https://cdn.openai.com/pdf/419b6906-9da6-406c-a19d-1bb078ac7637/oai_gpt-oss_model_card.pdf
- Harmony guide: https://github.com/openai/openai-cookbook/blob/main/articles/openai-harmony.md

Facts:
- **Params:** MoE, **20.91B total / 3.61B active** (model card Table 1. The HF card rounds to "21B / 3.6B"). Breakdown: MLP 19.12B, attention 0.64B, embed+unembed 1.16B. Unembedding counts toward active params and embedding does not.
- **Experts:** 32 experts, top-4, per layer. All 24 layers are MoE.
- **Quantization:** MXFP4 is native. MoE weights were post-trained in MXFP4 at 4.25 bits/param. Attention, router, embeddings and lm_head stay BF16 (`modules_to_not_convert`). The card says it "run[s] within 16GB of memory". **Checkpoint size 12.8 GiB** (model card Table 1). HF `model-0000{0,1,2}-of-00002.safetensors` = 4,792,272,488 + 4,798,702,184 + 4,170,342,232 = **13,761,316,904 B (13.76 GB = 12.82 GiB)**. The repo also has `original/model.safetensors` (13.76 GB) and `metal/model.bin` (13.75 GB), which is why the repo totals 41.3 GB.
- **Attention:** "attention blocks alternate between banded window and fully dense patterns, where the bandwidth is 128 tokens". 64 query heads of dim 64, GQA with 8 KV heads. Each head has "a learned bias in the denominator of the softmax, similar to off-by-one attention and attention sinks", which lets a head attend to no token. The checkpoint has a `model.layers.N.self_attn.sinks` tensor. Layer pattern (config `layer_types`): even indices (0,2,…,22) are `sliding_attention` and odd indices (1,3,…,23) are `full_attention`, so 12 + 12. Attention has biases (`attention_bias: true`).
- **Context:** 131,072 (config `max_position_embeddings 131072`). YaRN factor 32 from `initial_context_length 4096`. The card says YaRN extends the dense layers to 131,072.
- **Format:** harmony. The card says it "should only be used with the harmony format as it will not work correctly otherwise". The tokenizer is o200k_harmony.
- **Reasoning:** three levels, low/medium/high. You set the level in the system prompt with "Reasoning: low" etc., and the chat template defaults to `reasoning_effort = "medium"`.
  - **It cannot be fully disabled:** OpenAI documents only these three levels. The model card says it was trained to support "three reasoning levels: low, medium, and high", and there is no off/none mode in the card, the HF card or the harmony guide. Output always has an `analysis` channel (CoT) before `final`.
  - Prefilling an empty analysis message or skipping it is a community hack. It is UNVERIFIED and not officially supported.
- **License:** Apache-2.0 (plus a gpt-oss usage policy). Release date: August 5, 2025.

config.json (verbatim):
```
num_hidden_layers 24 | num_attention_heads 64 | num_key_value_heads 8 | head_dim 64
hidden_size 2880 | intermediate_size 2880 | vocab_size 201088
max_position_embeddings 131072 | initial_context_length 4096 | sliding_window 128
num_local_experts 32 | num_experts_per_tok 4 (also "experts_per_token": 4)
layer_types: ["sliding_attention","full_attention"] × 12 (alternating, starts sliding)
rope_theta 150000 | rope_scaling {rope_type yarn, factor 32.0, original_max_position_embeddings 4096, beta_fast 32.0, beta_slow 1.0, truncate false}
swiglu_limit 7.0 | attention_bias true | tie_word_embeddings false
quantization_config {quant_method mxfp4, modules_to_not_convert [model.layers.*.self_attn, model.layers.*.mlp.router, model.embed_tokens, lm_head]}
(no torch_dtype key; non-expert weights BF16 per HF param count: BF16 1,804,459,584 + U8 19,110,297,600 packed)
```

KV cache (computed, BF16):
- Full layers: 12 × 2 × 8 × 64 × 2 B = **24,576 B/token**, which is ≈3.0 GiB at 131,072.
- Sliding layers: capped at 128 tokens, so 12 × 128 × 2 × 8 × 64 × 2 B ≈ 3 MiB per sequence. This only holds if the runtime implements SWA cache trimming. llama.cpp may keep a larger SWA buffer.

GGUF:
- unsloth (https://huggingface.co/unsloth/gpt-oss-20b-GGUF): Q6_K 12,041,000,128 (12.04 GB). Q5_K_M 11,717,357,248 (11.72 GB). UD-Q4_K_XL 11,872,347,328 (11.87 GB). Q4_K_M 11.62 GB. Q8_0 12.11 GB. Q2_K 11.47 GB. F16 13.79 GB.
- All quants fall between 11.5 and 12.1 GB because the MXFP4 expert tensors are kept and only the small BF16 parts get requantized.
- ggml-org (official llama.cpp): `gpt-oss-20b-MXFP4.gguf` 12,109,566,624 (12.11 GB).
- INT4 alternatives: `OpenVINO/gpt-oss-20b-int4-ov`. No widely used AWQ repo, since MXFP4 is already 4-bit.

---

## 3. Newer models (May–Nov 2026), ≤~80B, relevant to agentic coding

I surveyed HF author listings: mistralai, zai-org, nvidia, ibm-granite, google, LiquidAI, inclusionAI, MiniMaxAI, moonshotai, ByteDance-Seed, Qwen.

Excluded because they are too big. Totals are from HF safetensors param counts:
- `zai-org/GLM-5.3-Flash` (2026-08-25): **321B** params. "Flash" is not small any more.
- `MiniMaxAI/MiniMax-M3.1-Flash` (2026-10-17): 220B.
- `inclusionAI/Ling-3.1-flash(-1019)`: 572B.
- `Qwen/Qwen3.8-Flash-Next`: 180B.
- `nvidia/NVIDIA-Nemotron-3-Super-120B-A12B` and `Nemotron-3.5-Super-VL-120B-A12B`: 120B.
- `mistralai/Leanstral-1.5-119B-A6B`: 119B, and Lean-specific.
- `moonshotai/Kimi-K2.7-Code`: 1.03T.

Excluded for other reasons:
- `Qwen/Qwen-AgentWorld-35B-A3B`: a world model that simulates environments, not an agent policy.
- Seed-OSS-36B: Aug 2025, no 2026 update.
- LFM3: ≤2B.
- Nemotron-3.5-Bolt-E4B: 8B edge model.

Picks:

### 3a. NVIDIA Nemotron 3.5 Lightning 30B-A3B: `nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-BF16`

- Card: https://huggingface.co/nvidia/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-BF16
- Release: **August 11, 2026** (card).
- License: **OpenMDW-1.1** (https://openmdw.ai/license/1-1/).
- Lineage: successor to Nemotron 3 Nano 30B-A3B (Dec 2025, same total param count 31,577,937,344).
- Hybrid **Mamba-2 + MoE + a few GQA attention layers**, with an MTP head (`num_nextn_predict_layers 1`). Params: "30B (3B active)". The HF count is 31.58B.
- Context: the card says "Up to 1M tokens (for single H100 deployment, we use 256K)", but config `max_position_embeddings` is 262144. Treat 256K as config-native and 1M as vendor-validated with `--max-model-len 1048576`.
- Reasoning: on by default. Turn it off with `enable_thinking=False` via chat-template kwargs (card). vLLM parsers: `--reasoning-parser nemotron_v3 --tool-call-parser qwen3_coder`.
- Benchmarks (card, vs. gpt-oss-20b): SWE-bench Verified 51.56 vs 52.44. Terminal-Bench 2.1 24.58 vs 15.17. The same table gives Gemma 4 26B-A4B 57.40 / 37.22.

config.json (verbatim):
```
num_hidden_layers 52 | num_attention_heads 32 | num_key_value_heads 2 | head_dim 128
hidden_size 2688 | intermediate_size 1856 | moe_intermediate_size 1856 | moe_shared_expert_intermediate_size 3712
vocab_size 131072 | max_position_embeddings 262144 | sliding_window null | dtype bfloat16
n_routed_experts 128 | num_experts_per_tok 6 | n_shared_experts 1 | routed_scaling_factor 2.5
mamba_num_heads 64 | mamba_head_dim 64 | ssm_state_size 128 | n_groups 8 | conv_kernel 4 | expand 2 | chunk_size 128
mamba_ssm_cache_dtype "float32" | rope_theta 10000 | mlp_hidden_act relu2
layers_block_type (52): ["mamba","moe","mamba","moe","mamba","attention","moe","mamba","moe","mamba","moe","mamba","attention","moe","mamba","moe","mamba","moe","mamba","attention","moe","mamba","moe","mamba","moe","mamba","attention","moe","mamba","moe","mamba","moe","mamba","attention","moe","mamba","moe","mamba","moe","mamba","moe","mamba","attention","moe","mamba","moe","mamba","moe","mamba","moe","mamba","moe"]
  → 23 mamba, 23 moe, 6 attention at indices [5,12,19,26,33,42]
mtp_layers_block_type ["attention","moe"]
```

Memory (computed):
- KV cache: 6 × 2 × 2 × 128 × 2 B = **6,144 B/token**. That is ≈0.75 GiB at 128K and ≈6 GiB at 1M.
- Mamba state: 23 × 64 × 64 × 128 × 4 B (fp32) ≈ 46 MiB per sequence plus a small conv state, constant regardless of context.

Files:
- NVFP4 (official): `nvidia/...-NVFP4` model.safetensors 20,427,842,628 (20.43 GB).
- FP8: `RedHatAI/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-FP8` 32,293,788,600 (32.29 GB).
- W4A16: `useful-quants/...-W4A16`. NVIDIA also says the NVFP4 card covers W4A16.
- unsloth GGUF (https://huggingface.co/unsloth/NVIDIA-Nemotron-3.5-Lightning-30B-A3B-GGUF): Q6_K 34,305,161,184 (34.31 GB). Q5_K_M 26,964,179,424 (26.96 GB). UD-Q4_K_XL 25,505,724,480 (25.51 GB). Q4_K_M 25.48 GB. MXFP4_MOE 23.21 GB. Q8_0 35.00 GB.
  - Low quants bottom out around 19.4 GB, since some tensors are kept at high precision.
- ggml-org GGUF: Q8_0 33.59 GB, plus an MTP draft GGUF of 2.18 GB.

### 3b. Gemma 4 (Google DeepMind): `google/gemma-4-26B-A4B-it`, `google/gemma-4-31B-it`, `google/gemma-4-12B-it`

- Releases page: https://ai.google.dev/gemma/docs/releases
  - **March 31, 2026**: E2B, E4B, 31B and 26B A4B.
  - April 16, 2026: MTP drafters.
  - **June 3, 2026**: Gemma 4 12B "Unified". The HF repo was created 2026-05-23.
  - Secondary press dates the launch blog to Apr 2, 2026.
  - So the 26B/31B models are just before the May–Nov window and the 12B is inside it.
- License: **Apache-2.0** (card front-matter).
- Context: 256K for 12B/26B/31B (card).
- Thinking: put `<|think|>` at the start of the system prompt, or `enable_thinking=True`. To turn it off, remove the token or set `enable_thinking=False`. With thinking off, 12B/26B/31B still emit an empty `<|channel>thought\n<channel|>` block (card).
- Attention: hybrid sliding-window (1024) + global. "the final layer is always global".
  - Global layers use "unified Keys and Values" (`attention_k_eq_v: true`), p-RoPE (partial_rotary_factor 0.25), `global_head_dim 512` and their own `num_global_key_value_heads`.
  - Pattern: 5 sliding then 1 full, repeating. Full layers sit at indices 5, 11, 17, …
- Card benchmarks (31B / 26B-A4B / 12B): LiveCodeBench v6 80.0 / 77.1 / 72.0. Tau2 76.9 / 68.2 / 69.0.
- Param counts:
  - 26B-A4B: card says 25.2B total / 3.8B active, with 128 experts, 8 active and 1 shared. The HF count is 25,805,936,206, which includes the vision tower.
  - 31B: dense, 30.7B per the card. The HF count is 31,273,088,876.
  - 12B: dense, 11.95B. The HF count is 11,959,730,224. It handles text, image and audio input.

config.json `text_config` (verbatim):

| key | 12B-it | 26B-A4B-it | 31B-it |
|---|---|---|---|
| num_hidden_layers | 48 | 30 | 60 |
| num_attention_heads | 16 | 16 | 32 |
| num_key_value_heads (sliding) | 8 | 8 | 16 |
| num_global_key_value_heads | 1 | 2 | 4 |
| head_dim / global_head_dim | 256 / 512 | 256 / 512 | 256 / 512 |
| hidden_size | 3840 | 2816 | 5376 |
| intermediate_size | 15360 | 2112 | 21504 |
| moe_intermediate_size | null | 704 | — |
| num_experts / top_k_experts | null/null | 128 / 8 (`enable_moe_block true`) | null/null |
| vocab_size | 262144 | 262144 | 262144 |
| max_position_embeddings | 262144 | 262144 | 262144 |
| sliding_window | 1024 | 1024 | 1024 |
| layer_types | 40 sliding + 8 full, full idx [5,11,17,23,29,35,41,47] | 25 sliding + 5 full, full idx [5,11,17,23,29] | 50 sliding + 10 full, full idx [5,11,…,59] |
| attention_k_eq_v | true | true | true |
| final_logit_softcapping | 30.0 | 30.0 | 30.0 |
| dtype | (top-level) | bfloat16 | bfloat16 |
| rope_parameters | full: proportional, partial_rotary_factor 0.25, theta 1e6; sliding: default theta 1e4 | same | same |

KV cache (computed, BF16):
- Sliding layers are capped at 1024 tokens:
  - 31B: 50 × 2 × 16 × 256 × 2 = 819,200 B/token → ≈0.78 GiB max.
  - 26B: 204,800 B/token → ≈0.2 GiB.
  - 12B: 327,680 B/token → ≈0.31 GiB.
- Global layers, if K=V is stored once vs. if K and V are both stored. Which one applies depends on the runtime, so it is UNVERIFIED.
  - 31B: 40,960 or 81,920 B/token.
  - 26B: 10,240 or 20,480 B/token.
  - 12B: 8,192 or 16,384 B/token.
  - Example: the 26B at 256K needs ≈2.5–5 GiB for global KV.

Files:

| | 12B | 26B-A4B | 31B |
|---|---|---|---|
| unsloth Q6_K | 9,786,022,720 (9.79 GB) | UD-Q6_K 23,172,478,688 (23.17 GB) (no plain Q6_K) | 25,201,486,784 (25.20 GB) |
| unsloth Q5_K_M | 8,413,576,000 (8.41 GB) | UD-Q5_K_M 21,150,365,408 (21.15 GB) | 21,658,401,728 (21.66 GB) |
| unsloth UD-Q4_K_XL | 7,366,423,360 (7.37 GB) | 17,010,980,576 (17.01 GB) | 18,822,970,304 (18.82 GB) |
| unsloth Q8_0 | 12.67 GB | 26.86 GB | 32.64 GB |
| other unsloth | | MXFP4_MOE 16.55 GB | |
| MTP drafter GGUF | 0.47 GB | 0.46 GB | 0.51 GB |
| official INT4 (QAT) | `google/gemma-4-12B-it-qat-w4a16-ct` 10.26 GB | — | `google/gemma-4-31B-it-qat-w4a16-ct` 23.27 GB |
| FP8 | — | `RedHatAI/gemma-4-26B-A4B-it-FP8-dynamic` 28.64 GB | — |
| NVFP4 | — | `nvidia/Gemma-4-26B-A4B-NVFP4` | — |

Google also publishes `google/gemma-4-*-qat-q4_0-gguf` repos.

### 3c. IBM Granite 4.2 30B: `ibm-granite/granite-4.2-30b`

- Card: https://huggingface.co/ibm-granite/granite-4.2-30b
- Release: **August 25, 2026** (card. The HF repo was created 2026-08-07).
- License: Apache-2.0.
- **Dense** transformer ("Decoder-only Dense Transformer (Reasoning)"), 30B. HF count 29,276,770,304. Unlike Granite 4.0 there is no Mamba: `GraniteForCausalLM`, full GQA on all 64 layers.
- Lineage: post-trained from Granite-4.1-30B-Base, with agentic RL on SWE, terminal and web-search tasks.
- Context: "Natively Supports 128K (Long-context extension to 512K)". Config 131072.
- Thinking modes:
  - Default: `enable_thinking=True`, emits `<think>…</think>`.
  - Off: `enable_thinking=False`.
  - Low effort: `enable_thinking=True, low_effort=True`.
- Card (30B column): SWE-bench Verified 57. SWE-bench Pro 33.29. Terminal-Bench 2.1 29.24. BFCL v4 61.39.
- Newer sibling: `ibm-granite/granite-switch-4.1-30b` (2026-10-14, 29.99B, Apache-2.0). I did not investigate it.

config.json (verbatim):
```
num_hidden_layers 64 | num_attention_heads 32 | num_key_value_heads 8 | head_dim (absent → 4096/32 = 128)
hidden_size 4096 | intermediate_size 32768 | vocab_size 100352 | max_position_embeddings 131072
rope_theta 50000000 (rope_type default) | no sliding_window/layer_types | torch_dtype bfloat16
attention_multiplier 0.0078125 | tie_word_embeddings false
```

KV cache (computed): 64 × 2 × 8 × 128 × 2 = **262,144 B/token (256 KiB)**, which is 32 GiB at 128K. That is heavy: it has the largest KV per token of all the models here.

Files:
- Official FP8: `ibm-granite/granite-4.2-30b-fp8` 30,109,724,784 (30.11 GB). IBM also has `-nvfp4` and `-mxfp4` repos (the 8B and 3B are confirmed; the 30B nvfp4 appears in search).
- GGUF (official ibm-granite; **no unsloth repo**, 401 Unauthorized): Q6_K 24,021,365,472 (24.02 GB). Q5_K_M 20,775,957,216 (20.78 GB). Q4_K_M 17,721,455,328 (17.72 GB). Q8_0 31.11 GB.

### 3d. GLM-4.7-Flash: `zai-org/GLM-4.7-Flash` (Jan 2026, before the window, but still the reference ≤80B Z.ai model)

- Card: https://huggingface.co/zai-org/GLM-4.7-Flash
- Release: January 19, 2026 (HF initial commit and upload).
- License: MIT.
- MoE "30B-A3B". HF count 31,221,488,576. Card: SWE-bench Verified 59.2 vs gpt-oss-20b 34.0.
- Attention: **MLA** (DeepSeek-style, `Glm4MoeLiteForCausalLM`) on all 47 layers. The first layer is dense (`first_k_dense_replace 1`), and there is an MTP layer.
- Context: config 202752. A "200K" figure appears only in secondary sources; the card states no context number, so the official figure is UNVERIFIED.
- Thinking: on by default. The chat template emits `</think>` instead of `<think>` when `enable_thinking` is false, so `chat_template_kwargs {"enable_thinking": false}` turns it off. `clear_thinking=false` gives "Preserved Thinking", which the card recommends for multi-turn agentic use. vLLM uses `--reasoning-parser glm45`.
- Newer replacement GLM-5.3-Flash is 321B, so it is out of range.

config.json (verbatim):
```
num_hidden_layers 47 | num_attention_heads 20 | num_key_value_heads 20 | hidden_size 2048 | intermediate_size 10240
q_lora_rank 768 | kv_lora_rank 512 | qk_nope_head_dim 192 | qk_rope_head_dim 64 | v_head_dim 256
n_routed_experts 64 | num_experts_per_tok 4 | n_shared_experts 1 | moe_intermediate_size 1536 | first_k_dense_replace 1
vocab_size 154880 | max_position_embeddings 202752 | rope_theta 1000000 | dtype bfloat16 | num_nextn_predict_layers 1
```

KV cache (computed, BF16):
- With MLA latent cache: 47 × (512 + 64) × 2 = **54,144 B/token**, which is ≈6.6 GiB at 128K.
- If the runtime expands K/V (no MLA kernel): 47 × 20 × (256 + 256) × 2 = 962,560 B/token. That is 18× larger, so check runtime MLA support.

Files:
- unsloth GGUF (https://huggingface.co/unsloth/GLM-4.7-Flash-GGUF): Q6_K 24,693,098,848 (24.69 GB). Q5_K_M 21,408,850,272 (21.41 GB). UD-Q4_K_XL 17,520,169,312 (17.52 GB). Q4_K_M 18.31 GB. MXFP4_MOE 16.97 GB. Q8_0 31.84 GB.
- FP8: no official `zai-org/GLM-4.7-Flash-FP8` (401). Use `unsloth/GLM-4.7-Flash-FP8-Dynamic`.
- AWQ: `QuantTrio/GLM-4.7-Flash-AWQ`, `cyankiwi/GLM-4.7-Flash-AWQ-4bit`.
- NVFP4: `GadflyII/GLM-4.7-Flash-NVFP4`.

### 3e. (Pointer) Qwen3.8-27B: `Qwen/Qwen3.8-27B`

This is the most-downloaded new ≤80B model. It is listed here in case Qwen is not covered elsewhere.

- Card: https://huggingface.co/Qwen/Qwen3.8-27B
- License: Apache-2.0.
- Release date: the HF repo was created 2026-08-05 and the README was updated 2026-08-12. Third-party sources say Aug 14, 2026. The official date is UNVERIFIED; the card cites https://qwen.ai/blog?id=qwen3.8.
- Dense 27B, a VLM (HF count 27,781,427,952). Hybrid layout: "16 × (3 × (Gated DeltaNet → FFN) → 1 × (Gated Attention → FFN))", i.e. linear attention with full attention every 4th layer.
- Context: 262,144 native, extensible to 1M.
- Thinking: on by default. Turn it off with `chat_template_kwargs {"enable_thinking": false}`. `reasoning_effort` takes xhigh (default), medium or low.

config (verbatim):
```
num_hidden_layers 64; num_attention_heads 24; num_key_value_heads 4; head_dim 256; hidden_size 5120
intermediate_size 17408; vocab_size 248320; max_position_embeddings 262144; full_attention_interval 4
layer_types 48 linear_attention + 16 full_attention (full idx 3,7,…,63)
linear_num_key_heads 16; linear_num_value_heads 48; linear_key_head_dim 128; linear_value_head_dim 128; linear_conv_kernel_dim 4
mamba_ssm_dtype float32; partial_rotary_factor 0.25; dtype bfloat16; mtp_num_hidden_layers 1
```

Memory (computed):
- KV cache: 16 × 2 × 4 × 256 × 2 = **65,536 B/token**, which is 8 GiB at 128K.
- DeltaNet state: 48 × 48 × 128 × 128 × 4 B ≈ 144 MiB per sequence (fp32).

Files:
- BF16: 55.56 GB.
- Official FP8: `Qwen/Qwen3.8-27B-FP8`, 30.87 GB total safetensors.
- unsloth GGUF has no plain Q6_K or Q5_K_M: UD-Q6_K 21,983,677,344 (21.98 GB). UD-Q5_K_M 19,771,509,664 (19.77 GB). UD-Q4_K_XL 17,559,178,144 (17.56 GB). Q8_0 29.05 GB. mmproj-F16 0.93 GB.
- INT4: `RedHatAI/Qwen3.8-27B-INT4`, `cyankiwi/Qwen3.8-27B-AWQ-INT4`.

---

## Quick comparison (computed from the above)

| model | total / active | KV B/token (BF16) | Q4-class GGUF | notes |
|---|---|---|---|---|
| Devstral Small 2 24B | 24B dense | 163,840 | UD-Q4_K_XL 14.51 GB | no thinking, 256K, Apache |
| gpt-oss-20b | 20.9B / 3.6B | 24,576 (+SWA 128 cap) | ~11.6–12.1 GB (any) | reasoning always on (low/med/high), 131K |
| Nemotron 3.5 Lightning | 31.6B / 3B | 6,144 (+46 MiB SSM) | UD-Q4_K_XL 25.51 GB | thinking toggle, 256K–1M, OpenMDW |
| Gemma 4 26B-A4B | 25.2B / 3.8B | global 10–20K (+SWA ≤0.2 GiB) | UD-Q4_K_XL 17.01 GB | thinking toggle, 256K, Apache |
| Gemma 4 31B | 30.7B dense | global 41–82K (+SWA ≤0.78 GiB) | UD-Q4_K_XL 18.82 GB | as above |
| Gemma 4 12B | 11.95B dense | global 8–16K (+SWA ≤0.31 GiB) | UD-Q4_K_XL 7.37 GB | as above |
| Granite 4.2 30B | 29.3B dense | 262,144 | Q4_K_M 17.72 GB | thinking toggle, 128K, Apache |
| GLM-4.7-Flash | 31.2B / ~3B | 54,144 (MLA) | UD-Q4_K_XL 17.52 GB | thinking toggle, 202,752, MIT |
| Qwen3.8-27B | 27.8B dense | 65,536 (+144 MiB state) | UD-Q4_K_XL 17.56 GB | thinking toggle, 262K, Apache |
