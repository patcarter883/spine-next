# Expanded survey: open-weight models from non-Chinese labs (≤~110B, fits 16–64 GB at 4-bit)

Compiled 2026-11-04.

**Method.**
- I listed the HF author orgs, newest first: mistralai, openai, meta-llama, meta-models, facebook, microsoft, google, nvidia, ibm-granite, allenai, LiquidAI, arcee-ai, ServiceNow-AI, CohereLabs, tiiuae, LGAI-EXAONE, upstage, swiss-ai, RekaAI, JetBrains, poolside, PrimeIntellect, NousResearch, cognition-ai, cursor, windsurf, ai21labs, deepcogito, HuggingFaceTB, Motif-Technologies, kakaocorp, naver-hyperclovax, sarvamai, trillionlabs, SakanaAI.
- Then for each candidate I pulled `config.json`, `README.md` and `api/models/<repo>?blobs=true`.

**Conventions.**
- Sizes are HF API bytes, with GB = 1e9 and shards summed.
- "KV" values are **my arithmetic** from config, in BF16 bytes per token. They count only layers that grow with context; sliding-window and SSM state is a fixed cost listed separately. Halve them for an FP8 KV cache.
- UNVERIFIED means I found no primary source.
- Scope: this file skips everything already in `other_models.md` and `qwen_models.md`.

**Big picture since mid-2026.** Three new non-Chinese labs released strong local coding-agent models:
- **Poolside:** Laguna XS.2 / XS 2.1.
- **Meta Superintelligence Lab:** Muse Glimmer 30B, under the new `meta-models` org.
- **Cohere:** North Mini Code.

In addition, JetBrains (Mellum2.1) and NVIDIA (Nemotron 3.5 Bolt 74B-A9B) released models in the 12B and 74B classes. The older "names to check" are mostly stale or closed:
- No open Devstral Medium, no new Codestral, no Phi-5.
- No open SWE-1.5, Cursor Composer or Cognition model on HF. The `cognition-ai`, `cursor` and `windsurf` orgs return nothing for 2026, so treat these as closed.
- gpt-oss-120b does not fit in 64 GB with useful context.

---

## A. Main candidates (detailed)

### A1. Poolside Laguna XS 2.1: `poolside/Laguna-XS-2.1` (sibling `poolside/Laguna-XS.2`)

- Card: https://huggingface.co/poolside/Laguna-XS-2.1
- Blog: https://poolside.ai/blog/introducing-laguna-xs-2-1 (published **July 2, 2026**; the HF repo was created 2026-06-20).
- XS.2 (the predecessor) was released 2026-04-23.
- **License:**
  - XS 2.1: **OpenMDW-1.1**. The card says it is "fully permissive… commercial products… No permission required".
  - XS.2: **Apache-2.0**.
- **Size:** MoE, 33B total / 3B active. HF count is 33,442,617,088.
  - 256 experts plus 1 shared, top-8. `moe_intermediate_size` 512. Layer 0 is a dense MLP.
- **Attention:** sigmoid-gated attention with SWA and global layers mixed 3:1. Global layers sit at indices 0, 4, 8, …, 36, which is 10 of 40.
  - The sliding window is **512**.
  - Q heads differ by layer type: 48 on global layers, 64 on sliding layers (`num_attention_heads_per_layer`).
  - The card's highlights say "KV cache in FP8".
- **Context:** 262,144 native (`max_position_embeddings 262144`). Global layers use YaRN factor 32 from 8192, `partial_rotary_factor 0.5`.
- **Thinking:** "interleaved thinking with preserved thinking". It is **off unless enabled**: the vLLM recipe sets `--default-chat-template-kwargs '{"enable_thinking": true}'`.
  - To disable it, send `enable_thinking: false` per request or omit the server default.
  - The card says thinking works best when you return `reasoning_content` in history.
- **config.json (verbatim):**
  ```
  num_hidden_layers 40 | num_attention_heads 48 | num_key_value_heads 8 | head_dim 128 | hidden_size 2048
  intermediate_size 8192 | moe_intermediate_size 512 | num_experts 256 | num_experts_per_tok 8 | mlp_only_layers [0]
  vocab_size 100352 | max_position_embeddings 262144 | sliding_window 512
  layer_types: full_attention at [0,4,8,12,16,20,24,28,32,36], sliding_attention elsewhere (30)
  num_attention_heads_per_layer: 48 on full layers, 64 on sliding layers
  rope_parameters: full_attention {rope_theta 500000, yarn factor 32, original_max_position_embeddings 8192, beta_fast 64, beta_slow 1, attention_factor 1.3466, partial_rotary_factor 0.5}; sliding_attention {default, theta 10000, partial_rotary_factor 1.0}
  torch_dtype bfloat16 | tie_word_embeddings false
  ```
- **KV (computed):** global layers 10 × 2 × 8 × 128 × 2 = **40,960 B/token BF16**, which is about 5 GiB at 128K or 2.5 GiB with FP8 KV. The SWA layers add a fixed ≈60 MiB (30 layers × 4 KiB × 512 tokens).
- **Files:**
  - BF16: 66.89 GB.
  - Official FP8 `poolside/Laguna-XS-2.1-FP8`: 35.32 GB.
  - Official INT4 `-INT4`: 23.90 GB.
  - Official NVFP4 `-NVFP4`: 21.58 GB.
  - AWQ `cyankiwi/Laguna-XS-2.1-AWQ-INT4`: 23.07 GB.
  - GGUF:
    - Official `poolside/Laguna-XS-2.1-GGUF`: Q4_K_M 20.27 GB.
    - bartowski `bartowski/Laguna-XS-2.1-GGUF`: Q6_K 29.04 GB, Q5_K_M 24.01 GB, Q4_K_M 20.55 GB.
    - ggml-org: Q4_K_M 19.56 GB.
    - `noctrex/...-MXFP4_MOE-GGUF`: 19.19 GB.
    - unsloth: no XS 2.1 GGUF repo was accessible (401).
- **Serving:**
  - **vLLM ≥ 0.21.0:** `--tool-call-parser poolside_v1 --reasoning-parser poolside_v1 --enable-auto-tool-choice`.
    - The card warns that you need vLLM PR #47311. Without it, `poolside_v1` silently drops tool calls that lack a newline after the function name. On older builds use `--tool-call-parser glm47`.
  - **SGLang:** `--reasoning-parser poolside_v1` (PR sgl-project/sglang#24204).
  - **TRT-LLM:** `--tool_parser poolside_v1 --reasoning_parser laguna`.
  - **llama.cpp:** PR #25165 was **merged 2026-07-22** (https://github.com/ggml-org/llama.cpp/pull/25165). It initially covered CPU only, with CUDA in follow-ups. No dedicated tool parser was added; the autoparser handles the GLM-4.5/4.7-style format. Use `--jinja`.
    - Open issues in the thread: reasoning can run to the token budget because of an EOG-token registration problem (workaround `--reasoning off`); the DFlash draft GGUF fails to load; partial expert offload crashed.
    - The card notes a Metal f16 overflow (llama.cpp PR #25389). It does not affect CUDA.
- **Benchmarks (vendor):**
  - Harness: Poolside's own `pool` agent on Harbor, at most 500 steps, thinking on, 256K context.
  - Laguna XS 2.1: SWE-bench Verified **70.9**, SWE-bench Multilingual 63.1, SWE-Bench Pro 47.6, **Terminal-Bench 2.0 37.5**.
  - XS.2: 69.9 / 57.7 / 46.3 / 35.7.
  - The same table quotes Qwen3.6-35B-A3B at 73.4 / 67.2 / 49.5 / 51.5.
  - Independent: none found. Laguna is not on the SWE-rebench page (latest window 15/05–01/07/2026, https://swe-rebench.com/).

### A2. Meta Muse Glimmer 30B: `meta-models/Muse-Glimmer-30B`

- Card: https://huggingface.co/meta-models/Muse-Glimmer-30B. Release "August 2026"; the HF repo was created 2026-08-09.
- **License: Apache-2.0** plus a Usage Policy file (`USAGE_POLICY.md`).
- Note that Llama 4 used a custom license. Meta's new org is `meta-models`, not `meta-llama`.
- **Size:** **dense**, 29.6B including a ~1.8B ViT-G/14 "perception encoder" (HF count 29,776,626,688). It is distilled from Muse Spark (816B-A42B), takes image input and gives text output.
- **Attention:** gated attention, pattern `[Local, Local, Local, Global]`. Local (sliding) layers use RoPE θ = 5e5. Global layers have **no RoPE**: config `layer_rope_theta` is 0 on global layers.
  - Global layers sit at indices 3, 7, …, 51 (13 of 52). The window is **2048**.
  - GQA is 32 query heads to **2 KV heads**.
- **Context:** card says "131,072+". Config 131072.
- **Thinking:** "Reasoning strength" low / medium / high / xhigh, set in the system prompt or via the `reasoning_strength` template kwarg. The default is high.
  - **It cannot be switched off.** The GGUF card says "The template opens the thinking channel unconditionally, so `--reasoning off`… and `"reasoning_effort": "none"` all have no effect". `low` is the minimum, and llama.cpp `--reasoning-budget N` caps it.
  - The format is a harmony-like channel format (`<|start|>`, `<|message|>`, `to=…`, `<|eom|>`/`<|eot|>`).
  - Do **not** stop on `<|eom|>`, or parallel tool calls collapse.
- **config.json `text_config` (verbatim):**
  ```
  num_hidden_layers 52 | num_attention_heads 32 | num_key_value_heads 2 | head_dim 128 | hidden_size 6656
  intermediate_size 19968 | vocab_size 202048 | max_position_embeddings 131072 | sliding_window 2048
  layer_types: full_attention at [3,7,11,15,19,23,27,31,35,39,43,47,51] (13), sliding_attention elsewhere (39)
  layer_rope_theta: 500000.0 on sliding layers, 0 on full layers | rope_parameters {rope_theta 500000, default}
  tie_word_embeddings false | dtype bfloat16 (top-level)
  ```
- **KV (computed):** global 13 × 2 × 2 × 128 × 2 = **13,312 B/token BF16**, which is 1.6 GiB at 128K. SWA adds a fixed ≈78 MiB. This is very light for a dense 30B.
- **Files:**
  - BF16: 59.55 GB.
  - FP8: `RedHatAI/Muse-Glimmer-30B-FP8-block` 34.39 GB.
  - INT4: `RedHatAI/Muse-Glimmer-30B-INT4` 22.20 GB.
  - NVFP4: `nvidia/Muse-Glimmer-30B-NVFP4` 24.67 GB.
  - Official GGUF (`meta-models/Muse-Glimmer-30B-GGUF`): KQuant-Dynamic Q4_K_XL 19.65 GB (the card cites 0.2% degradation), KQuant-17GB Q4_K_M 16.76 GB (1.0% degradation), DFlash drafter 1.63 GB, mmproj 1.40 GB.
  - unsloth (`unsloth/Muse-Glimmer-30B-GGUF`, the second group in the listing): UD-Q4_K_XL 15.88 GB, UD-Q5_K_M 19.19 GB, UD-Q6_K_XL 26.27 GB, Q8_0 29.61 GB.
  - Another listing with plain quants (probably bartowski): Q6_K 23.41 GB, Q5_K_M 20.11 GB, Q4_K_M 17.31 GB.
- **Serving:**
  - **vLLM** recipe (https://recipes.vllm.ai/meta-models/Muse-Glimmer-30B): `--enable-auto-tool-choice --tool-call-parser muse_glimmer --reasoning-parser muse_glimmer --generation-config auto`, image `vllm/vllm-openai:v0.28.0`.
  - **SGLang** cookbook: https://docs.sglang.io/cookbook/autoregressive/Meta/MuseGlimmer.
  - **llama.cpp:** supported from **b10353** (PR #26841, merged 2026-08-10). `--jinja` is mandatory, and earlier GGUFs shipped a broken template (re-download).
  - DFlash spec-decoding: a user reports that the official setup needed patches (HF discussion #40). This is community-only.
- **Benchmarks (vendor, "High reasoning"):**
  - SWE-bench Verified **76.0**, SWE-Bench Pro 51.2.
  - **Terminal-Bench 2.1 (Terminus-2) 51.7**.
  - τ3-Banking 23.5, MCP-Atlas 75.5.
  - Comparison in the card: Qwen3.6-27B 77.2 / 50.2 / 60.7 and Gemma4-31B 66.6 / 36.9 / 43.4.
  - The SWE harness is not stated on the card; it points to https://research.meta.ai/static/muse-glimmer-methodology (UNVERIFIED, not read).
  - Independent: none found.

### A3. Cohere North Mini Code 1.0: `CohereLabs/North-Mini-Code-1.0`

- Card: https://huggingface.co/CohereLabs/North-Mini-Code-1.0. Blog (**June 9, 2026**): https://huggingface.co/blog/CohereLabs/introducing-north-mini-code.
- **License: Apache-2.0.** This is unlike Command A/A+, which used CC-BY-NC. Note that `command-a-plus-05-2026` is now tagged apache-2.0, but it is 218.75B, so out of range.
- **Size:** MoE, 30B / 3B active. HF count 30,484,303,872. 128 experts, top-8, sigmoid router, 1 dense first layer.
- **Attention:** SWA with RoPE, mixed 3:1 with global layers that use **no positional embedding**.
  - Global layers sit at indices 0, 4, …, 48 (13 of 49). The window is **4096**, with 4 KV heads.
- **Context:** 256K with 64K max output (card). Config `max_position_embeddings 500000`; the card's vLLM example uses `--max-model-len 320000`.
- **Thinking:** interleaved thinking, "works best when turned on". Pass `reasoning` back in history.
  - The template turns it off when `reasoning_effort == "none"` (`chat_template.jinja` line 11), or via an explicit `reasoning=false` kwarg.
- **config.json (verbatim):**
  ```
  num_hidden_layers 49 | num_attention_heads 32 | num_key_value_heads 4 | head_dim 128 | hidden_size 2048
  intermediate_size 768 | num_experts 128 | num_experts_per_tok 8 | first_k_dense_replace 1
  vocab_size 262144 | max_position_embeddings 500000 | sliding_window 4096 | rope_theta 50000 | rope_scaling null
  layer_types: full_attention at [0,4,8,...,48] (13), sliding_attention (36) | prefix_dense_sliding_window_pattern 1 | dtype bfloat16
  ```
- **KV (computed):** global 13 × 2 × 4 × 128 × 2 = **26,624 B/token BF16**, which is 3.25 GiB at 128K. SWA adds a fixed ≈288 MiB.
- **Files:**
  - Official FP8 `-fp8`: 32.03 GB.
  - Official `-w4a16`: 19.35 GB.
  - AWQ `cyankiwi/...-AWQ-INT4`: 18.47 GB.
  - unsloth GGUF: UD-Q6_K 25.51 GB, UD-Q5_K_M 22.95 GB, **UD-Q4_K_XL 19.25 GB**, MXFP4_MOE 18.66 GB, Q8_0 32.44 GB.
  - bartowski: Q6_K 26.40 GB, Q5_K_M 21.85 GB, Q4_K_M 18.74 GB.
  - There is also an official EAGLE head, `-eagle`.
- **Serving:**
  - **vLLM** (main at release, plus `cohere_melody>=0.9.0`): `--tool-call-parser cohere_command4 --reasoning-parser cohere_command4 --enable-auto-tool-choice`.
  - **llama.cpp:** `cohere2moe` arch was added in PR #24260 (merged ~2026-06-13). A dedicated Cohere chat parser is in PR #24615 (merge status UNVERIFIED).
  - Known llama.cpp issue: two different templates ship (in `tokenizer_config.json` and in `chat_template.jinja`). Use the `.jinja` one, because the old one leaks reasoning into content (PR thread).
- **Benchmarks:**
  - Vendor (blog): SWE-bench Verified **61.0 pass@1 with mini-SWE-agent**. The SFT checkpoint scored 80.2 pass@10. RL added +3.0 on SWE-bench and +7.9 on TB2 pass@1.
  - The card's chart is an image. Poolside's table quotes North Mini Code at SWE-V **67.6**, SWE-Pro 40.2, **TB2 36.0** as "official scores".
  - Card harnesses: SWE-agent v1.1.0 for SWE-bench, a single-terminal-tool ReAct harness for TB2, and Terminus-2 for TB-Hard.

### A4. JetBrains Mellum2.1 12B-A2.5B Thinking: `JetBrains/Mellum2.1-12B-A2.5B-Thinking`

- Card: https://huggingface.co/JetBrains/Mellum2.1-12B-A2.5B-Thinking. The HF repo was created 2026-09-20; the GGUF repo on 2026-10-07; the MTP variant on 2026-10-30.
- **License: Apache-2.0.**
- **Size:** MoE, 12B / 2.5B active (HF count 12,149,923,072). 64 experts, top-8. A custom `mellum` arch.
- **Attention:** SWA window 1024 on 3 of every 4 layers. Full layers sit at 3, 7, …, 27 (7 of 28). GQA 32 query heads to 4 KV heads.
- **Context:** 131,072. YaRN factor 16 from 8192 on full layers.
- **Thinking:** on by default, `<think>` format. **To turn it off, set `enable_thinking=false`**: the template then emits an empty `<think>\n\n</think>` (`chat_template.jinja` line 100). The card itself calls it "a thinking model".
- **config.json (verbatim):**
  ```
  num_hidden_layers 28 | num_attention_heads 32 | num_key_value_heads 4 | head_dim 128 | hidden_size 2304
  intermediate_size 7168 | moe_intermediate_size 896 | num_experts 64 | num_experts_per_tok 8
  vocab_size 98304 | max_position_embeddings 131072 | sliding_window 1024 | use_sliding_window true
  layer_types: full_attention at [3,7,11,15,19,23,27] (7), sliding (21)
  rope_parameters: full {yarn, theta 500000, factor 16, orig 8192, attention_factor 1.2773}; sliding {default, theta 500000}
  ```
- **KV (computed):** 7 × 2 × 4 × 128 × 2 = **14,336 B/token**, which is 1.75 GiB at 128K. SWA adds a fixed ≈42 MiB.
- **Files:**
  - Official GGUF: Q8_0 12.93 GB, Q6_K 10.88 GB, Q4_K_M 8.07 GB, MXFP4_MOE+MTP 7.3 GB, and a 1.8 GB MTP draft head.
  - bartowski: Q6_K 11.45 GB, Q5_K_M 9.97 GB, Q4_K_M 8.82 GB.
  - FP8 `liodon-ai/...-FP8`: 12.62 GB.
  - AWQ `cyankiwi/...-AWQ-INT4`: 7.68 GB.
- **Serving:**
  - **vLLM:** `--reasoning-parser qwen3 --enable-auto-tool-choice --tool-call-parser hermes`.
  - **llama.cpp:** official GGUF, `llama-server -hf JetBrains/Mellum2.1-12B-A2.5B-Thinking-GGUF:Q4_K_M`, plus MTP spec-decoding (`--spec-type draft-mtp`). The minimum build is UNVERIFIED.
- **Benchmarks (vendor; important because the harness is Pi):**
  - "Agentic benchmarks use the same open-source agent harness (**Pi v0.73.1**, shell and file tools) for every model… 114K-token context."
  - SWE-bench Verified **47.0**, Terminal-Bench 2.1 **17.4**, SWE-Pro 28.0, BFCL v4 62.3.
  - Same harness for comparison: Qwen3.5-9B 50.0 / 21.7 / 38.0 / 58.5 and Gemma 4 E4B 23.0 / 3.4.
  - This is a good 16 GB-tier option, but weaker than the 30B class.

### A5. NVIDIA Nemotron 3.5 Bolt 74B-A9B: `nvidia/NVIDIA-Nemotron-3.5-Bolt-74B-A9B-BF16` (plus `-24GB`)

- Card: https://huggingface.co/nvidia/NVIDIA-Nemotron-3.5-Bolt-74B-A9B-BF16. Release: "Hugging Face 10/15/2026" (card); the repos were created 2026-10-07/09.
- **License: OpenMDW-1.1.**
- **Size and lineage:** VLM, Mamba-2 + Transformer hybrid **LatentMoE**, 74B / 9B active. Compressed with Star Elastic from Nemotron-3.5-Super-VL-120B-A12B, with top-K 9 (down from 22). It has a C-RADIO v4-H vision encoder.
- **Context:** up to 256K.
- **Thinking:** `enable_thinking` True/False through chat-template kwargs. Medium effort is `{"enable_thinking": true, "medium_effort": true}`.
- **config `llm_config` (verbatim):**
  ```
  layers_block_type (88): 40 mamba, 40 moe, 8 attention at [7,16,25,36,47,58,69,78]
  num_attention_heads 32 | num_key_value_heads 2 | head_dim 128 | hidden_size 4096 | sliding_window null
  mamba_num_heads 128 | mamba_head_dim 64 | ssm_state_size 128 | n_groups 8 | conv_kernel 4 | expand 2 | chunk_size 128 | mamba_ssm_cache_dtype float32
  num_experts 512 (per-layer heterogeneous via block_configs, e.g. 240 routed, top-9) | n_shared_experts 1 | moe_latent_size 1024
  vocab_size 131072 | max_position_embeddings 262144 | num_nextn_predict_layers 0
  ```
- **Memory (computed):** KV 8 × 2 × 2 × 128 × 2 = **8,192 B/token**, which is 1 GiB at 128K. The Mamba state is about 40 × 128 × 64 × 128 × 4 B ≈ **160 MiB per sequence** (fp32).
- **Files:**
  - Official `-24GB`: native 2-bit QAD, experts in IQ2_XXS. Safetensors 24.08 GB; GGUF 22.02 GB plus mmproj 1.73 GB.
  - unsloth `unsloth/NVIDIA-Nemotron-3.5-Bolt-74B-A9B-GGUF`: UD-Q4_K_XL **50.22 GB**, UD-Q5_K_XL 58.91 GB, UD-Q6_K_XL 72.39 GB, Q8_0 78.85 GB.
  - So at 64 GB a 4-bit build fits with modest context. The 24GB IQ2 build is NVIDIA's own recommendation.
- **Serving:**
  - **vLLM:** `--tool-call-parser qwen3_coder --reasoning-parser-plugin ultra_v3_reasoning_parser.py --reasoning-parser ultra_v3 --mamba-backend flashinfer --mamba-cache-mode align`. The reasoning parser is a custom plugin file downloaded from the repo.
  - **llama.cpp:** official GGUF, `--jinja`. The card gives recipes: a 28 GB recipe for 64K context with q8_0 KV, and a 32 GB recipe for 262K.
- **Benchmarks (vendor; no SWE-bench reported):**
  - Terminal-Bench 2.1: **50.98 (24GB IQ2)** / 53.93 (BF16).
  - τ3 Banking 17.01, MCP-Atlas 52.2 / 55.6, PinchBench 87.8 / 90.35.
  - Same table: Qwen3.6-35B-A3B TB2.1 44.38, Gemma4 26B-A4B 37.22, Nemotron 3.5 Lightning 23.46.

---

## B. Checked, fits, but weaker or with caveats (summary)

| Model (repo) | Arch / params | Context | License | Thinking off | KV B/tok BF16 (computed) | 4-bit-class size | Parsers | Coding-agent evidence |
|---|---|---|---|---|---|---|---|---|
| `openai/gpt-oss-120b` (2025-08-05) | MoE 116.8B / 5.1B; 36 layers, alternating SWA-128 and full (18 + 18); 8 KV, head 64 | 131,072 | Apache-2.0 | no (low/med/high only) | 36,864 | **native MXFP4 = 65.25 GB**; unsloth "Q4_K_M" 62.77 / MXFP4 63.39 GB | vLLM `openai` tool + `openai_gptoss` reasoning (see the gpt-oss-20b notes) | SWE-V 62.4 (high) / 52.6 (med) / 47.9 (low), τ-Retail 67.8 (model card arXiv 2508.10925). TB2 18.7, SWE-Pro 16.2 (leaderboard numbers quoted by poolside). **Does not fit 64 GB with useful KV** (60.8 GiB of weights); it needs CPU offload of experts. |
| `nvidia/gpt-oss-puzzle-88B` (2026-03-26) | gpt-oss-120b pruned with Puzzle NAS to 88B (HF 90.8B incl. scales); 36 layers: 10 full + 8 window-8192 + 18 window-128; experts 128 to 64 per layer | 128K (config 229,376) | **NVIDIA Open Model License** | no (low/med/high) | full 20,480 + 8 × 2 KiB × 8192 (≈128 MiB fixed) | MXFP4 49.99 GB; GGUF MXFP4_MOE 48.11 GB (SamPurkis) | custom `GptOssPuzzleForCausalLM`; vLLM recipe in card | No agentic-coding benchmarks on the card; RL was on math/code/MCQ only. Fits 64 GB. |
| `nvidia/NVIDIA-Nemotron-Labs-3-Puzzle-75B-A9B-BF16` (2026-06-24) | Nemotron-3-Super 120B-A12B compressed to 75.3B / 9.3B; same 88-block layout as Bolt (8 attn, 2 KV), `ssm_state_size 96`, MTP | 256K (1M supported) | OpenMDW-1.1 | `enable_thinking=False`; `low_effort` | 8,192 (+≈120 MiB SSM) | official NVFP4 53.50 GB; W4A16 (danielrmay) 44.54 GB; GGUF Q4_K_M 51.60 GB | `qwen3_coder` + `nemotron_v3` | SWE-bench (OpenHands) 56.9 vs Super 59.5; TB-hard 24.0 (vendor). Text-only. Bolt is the newer, better-targeted choice. |
| `nvidia/Nemotron-Cascade-2-30B-A3B` (2026-03-18) | Nemotron-3-Nano arch (6 attn layers, 2 KV) | 1M | **NVIDIA Open Model License** | `enable_thinking=False` | 6,144 | AWQ 20.75 GB; bartowski Q4_K_M 24.73 GB / Q5_K_M 26.21 / Q6_K 33.55 GB | `qwen3_coder` + `nano_v3` | SWE-V (OpenHands) 50.2, TB2 21.1, BFCL v4 52.9 (vendor). "does not currently support OpenCode; primarily supports OpenHands". Superseded by 3.5 Lightning (already covered). |
| `mistralai/Mistral-Small-4-119B-2603` (2026-01-23) | MoE 119B, 128 experts, top-4 + 1 shared; **MLA** (kv_lora_rank 256, rope 64), 36 layers | 256K | Apache-2.0 | `reasoning_effort="none"` | 23,040 (MLA latent) | unsloth UD-Q4_K_XL **74.16 GB** (does not fit); UD-Q3_K_XL 54.82 GB; UD-IQ3_XXS 42.80 GB | `mistral` / `mistral` | Only 3-bit fits 64 GB, so quality risk. No SWE number on the card. |
| `mistralai/Magistral-Small-2509` (2025-09) | dense 24B; 40 layers, 8 KV, head 128 | 128K (config 131072) | Apache-2.0 | reasoning model; `--reasoning-parser mistral` | 163,840 | (as Devstral Small 2 class, ~14–15 GB Q4) | `mistral` / `mistral` | Older; Devstral Small 2 (covered) is the better Mistral coding pick. |
| `mistralai/Ministral-3-14B-Instruct-2512` / `-Reasoning-2512` (2025-12) | dense 14B (FP8 official); 40 layers, 8 KV, head 128, no SWA | 256K | Apache-2.0 | Instruct has none; Reasoning variant separate | 163,840 | FP8 official ~14 GB (UNVERIFIED exact) | `mistral` | No agentic SWE numbers found. |
| `meta-llama/Llama-4-Scout-17B-16E-Instruct` (2025-04) | MoE 108.6B / 17B, 16 experts top-1; 48 layers, 8 KV, head 128; iRoPE: 12 NoPE global layers at [3, 7, …, 47], 36 chunked-attention layers (`attention_chunk_size 8192`) | 10M (config) | **Llama 4 Community License (gated)** | n/a | 49,152 global (+≤1.1 GiB chunked) | ~58–62 GB at Q4 (UNVERIFIED exact) | `llama4_pythonic` (UNVERIFIED) | Weak at agentic coding; older than 12 months. Not recommended. |
| `ibm-granite/granite-4.0-h-small` (2025-10-02) | hybrid Mamba-2 MoE 32B / 9B; 40 layers: 36 mamba + 4 attn at [5, 15, 25, 35]; 8 KV | 128K | Apache-2.0 | no thinking | 16,384 (+Mamba 36 × 128 × 64 × 128 state) | ~19 GB Q4 (UNVERIFIED) | `hermes` (UNVERIFIED) | General instruct; Granite 4.2 30B (covered) supersedes it for SWE. |
| `ibm-granite/granite-switch-4.1-30b` (2026-10-14/27) | Granite 4.1 30B plus switchable adapters (guardian, speech, decisions) | 131,072 | Apache-2.0 | – | – | GGUF official | – | A modular/guardian product, not a coding agent. Skip. |
| `allenai/SERA-32B` (2026-01-27) | dense, Qwen3-32B base (**Chinese base**) | **32K** | Apache-2.0 | – | Qwen3-32B: 64 × 8 × 128 × 4 = 262,144 | community GGUFs (mradermacher) | `hermes` | SWE-V 49.5 at 32K (vendor). Research artifact; "may attempt to call a nonexistent `submit` tool". Skip. |
| `allenai/Olmo-3.1-32B-Think` / `-Instruct` (2025-12-10) | dense 32B fully open; 64 layers, 48 SWA-4096 + 16 full, 8 KV | 65,536 | Apache-2.0 | separate Instruct model | 65,536 (+768 MiB SWA) | unsloth/bartowski GGUF exist | – | No SWE/TB numbers; not agent-tuned. |
| `allenai/tmax-27b` (2026-06-21) | Qwen3.5-27B fine-tune (Chinese base) | – | Apache-2.0 | – | – | – | – | Research; not investigated further. |
| `arcee-ai/Trinity-Mini` (2025-12-01) | MoE 26.1B / ~3B (`afmoe`), 128 experts top-8; 32 layers, 8 full + 24 SWA-2048, 4 KV | 128K | **OpenMDW-1.1** | `deepseek_r1` reasoning parser | 16,384 (+96 MiB SWA) | W4A16 / NVFP4 / GGUF exist | `hermes` + `deepseek_r1`; llama.cpp ≥ b7061 | No SWE numbers. Trinity Large is 400B, out of range. |
| `ai21labs/AI21-Jamba2-Mini` (2026-01-06) | Jamba (Mamba-1 + attn, MoE) 51.6B / ~12B; 32 layers, attn every 8 (offset 4), so 4 attn layers, 8 KV | 256K | Apache-2.0 | no thinking | 16,384 | AWQ-4bit, MXFP4_MOE GGUF exist | `hermes` | Enterprise RAG focus; no SWE numbers. |
| `LGAI-EXAONE/EXAONE-4.5-33B` (2026-04-04) | dense 34.4B VLM; 64 layers, 48 SWA-4096 + 16 full (LLLG), 8 KV, MTP | 256K | **EXAONE license (non-commercial-style "other", flag)** | `enable_thinking=False` | 65,536 (+768 MiB SWA) | official AWQ, FP8, GGUF | `hermes` + `qwen3` | Card benchmarks not reviewed for SWE. The license blocks commercial use (UNVERIFIED exact terms). |
| `swiss-ai/Apertus-v1.5-70B` (2026-07-24) | dense 72B multimodal; 80 layers, 8 KV, head 128 | 262,144 | Apache-2.0 + AUP (gated) | "can be switched to thinking mode" | 327,680 | W4A16 (SinclairSchneider), NVFP4 | `--tool-call-parser apertus`, only in swiss-ai vLLM fork | Needs a fork; heavy KV; not code-tuned. |
| `PrimeIntellect/INTELLECT-3.1` (2026-01-20) | GLM-4.5-Air base (**Chinese base**), MoE 106.9B / 12B; 46 layers, 8 KV | 131K | MIT | `deepseek_r1` parser | 188,416 | Q4 ~60+ GB (UNVERIFIED) | `qwen3_coder` + `deepseek_r1` | Q4 does not leave KV room in 64 GB. |
| `NousResearch/Hermes-4.3-36B` (2025-11) | Seed-OSS-36B base (**Chinese base**), dense; 64 layers, 8 KV | 512K | Apache-2.0 | `<think>` toggle | 262,144 | GGUF official | `hermes` | Generalist; no SWE numbers. NousCoder-14B (Qwen3-14B base) is competitive coding only. |
| `upstage/Solar-Open-100B` (2025-12-10) | MoE 102.6B, 128 experts top-8; 48 layers, 8 KV | 128K | **Upstage Solar License (flag)** | – | 196,608 | Q4 ~60 GB (UNVERIFIED) | `solar_open` / `solar_open` | τ² Airline 52.4 / Retail 59.3 (vendor). Too big plus restrictive license. |
| `ServiceNow-AI/Apriel-1.6-15b-Thinker` (2025-11-28) | dense 15B VLM; 48 layers, 8 KV | 262K | MIT | always-reasoning thinker | 196,608 | GGUF official | `apriel` / `apriel` (custom image; upstream PR pending at release) | τ²-Telecom 69 (vendor). Not SWE-focused. |
| `tiiuae/Falcon-H1R-7B` (2025-10-29) | hybrid parallel attn + Mamba-2, 7.6B; 44 layers, 2 KV | 256K | **Falcon-LLM License (flag)** | reasoning model | 45,056 | GGUF official | `hermes` + `deepseek_r1` | Too small for the agent role. |
| `microsoft/Phi-4-reasoning-plus` (2025-04) / `Phi-4-reasoning-vision-15B` (2026-01-23) | dense 14–15B; 40 layers, 10 KV | **32K** | MIT | always reasoning | 204,800 | – | – | 32K context; no agentic tuning. No Phi-5 on HF. |
| `microsoft/AesCode-32B` (2026-09-29) | Qwen3-VL-32B base, HTML/CSS visual-artifact generator | 24,576 used | Apache-2.0 | – | – | GGUF (bartowski) | – | Not a coding agent. Skip. |
| `google/diffusiongemma-26B-A4B-it` (2026-06-09) | block-diffusion Gemma 4 26B-A4B | – | Apache-2.0 | – | – | – | – | Diffusion decoding; no vLLM tool-calling path. Skip. No Gemma 4 coding variant exists. |
| Korean/Indian labs: `kakaocorp/kanana-2-30b-a3b-instruct-2601` (DeepSeek-V3 arch, **kanana license**), `trillionlabs/Gravity-30B-A5B-Preview` (Apache), `sarvamai/sarvam-30b` (Apache), `naver-hyperclovax/HyperCLOVAX-SEED-Think-32B` (**custom license**), `Motif-Technologies/Motif-3` (315B, too big) | – | – | – | – | – | – | – | None publish SWE/TB numbers competitive with A1–A3. Not investigated further. |

**Out of range (too big at 4-bit for 64 GB):**
- `poolside/Laguna-S-2.1` (117.6B, OpenMDW). unsloth UD-Q4_K_XL 73.40 GB, bartowski Q4_K_M 71.76 GB.
- `poolside/Laguna-M.1` (225.8B).
- `mistralai/Mistral-Medium-3.5-128B`.
- `mistralai/Devstral-2-123B` (modified MIT).
- `meta-models/Muse-Spark-Open-816B-A42B`.
- `CohereLabs/command-a-plus-05-2026` (218.75B).
- Nemotron-3/3.5-Super 120B.
- `upstage/Solar-Open2-250B`.
- Arcee Trinity Large (~400B).
- `LGAI-EXAONE/K-EXAONE-*` (236B / 750B).
- MAI-Code-1-Flash (137B; it appears in poolside's comparison table, but I did not check whether open weights exist, UNVERIFIED).

**Small/edge only:** LiquidAI LFM3 (1.7B; no larger LFM in 2026), Nemotron-3.5-Bolt-E4B, Falcon-H1-Tiny, Apertus 8B, Reka Edge.

---

## C. Ranked shortlist for Pi (4 tools, OpenAI-compatible tool calling, 16–64 GB)

1. **Laguna XS 2.1** (`poolside/Laguna-XS-2.1`): best-documented local agentic-coding model from a non-Chinese lab.
   - SWE-V 70.9 / TB2 37.5 (vendor), 3B active, so it is fast.
   - Small KV (40 KiB/token BF16) and 256K native context.
   - Official FP8 (35 GB) and INT4/NVFP4 (~22–24 GB); Q4 GGUF ~20 GB.
   - Thinking can be toggled per request.
   - Risks: you need a vLLM with the #47311 parser fix (else use `glm47`), and llama.cpp has open EOG/reasoning issues.
   - XS.2 (Apache-2.0) is an equivalent fallback if you need Apache.
2. **Muse Glimmer 30B** (`meta-models/Muse-Glimmer-30B`): highest vendor numbers in the class (SWE-V 76.0, TB2.1 51.7), Apache-2.0.
   - Very small KV for a dense model (13 KiB/token). The official Q4 GGUF is 16.8–19.7 GB, so it fits 24–32 GB.
   - Caveats:
     - It is dense 30B, so decode is slower than 3B-active MoEs.
     - **Reasoning cannot be disabled** (only low…xhigh).
     - It uses a harmony-like format that needs the `muse_glimmer` parsers (vLLM ≥ 0.28) or llama.cpp ≥ b10353.
3. **Nemotron 3.5 Bolt 74B-A9B**, 24GB IQ2 or UD-Q4_K_XL 50 GB: best for the 32–64 GB tier.
   - TB2.1 51–54 (vendor), tiny KV (8 KiB/token) plus ~160 MiB SSM state, thinking toggle, OpenMDW.
   - Caveats: the vLLM reasoning parser is a plugin file, there is no published SWE-bench number, and it is hybrid Mamba (prefix-caching and spec-decode support vary by runtime).
4. **North Mini Code 1.0** (`CohereLabs/North-Mini-Code-1.0`): Apache-2.0, 30B-A3B, 256K context, SWE-V ~61–67.6 / TB2 ~36.
   - Caveats: its vLLM parsers (`cohere_command4`) need the `cohere_melody` package, and llama.cpp has template quirks (use `chat_template.jinja`).
   - It is a solid alternative if the Laguna parsers give trouble.
5. **Mellum2.1 12B-A2.5B Thinking**: the 16 GB pick (Q4 ~8 GB, KV 14 KiB/token).
   - It is the only model here benchmarked **in Pi itself** (SWE-V 47.0, TB2.1 17.4 under Pi v0.73.1). Apache-2.0, `hermes` + `qwen3` parsers, `enable_thinking=false` works.

gpt-oss-120b is **not** shortlisted: 65.25 GB of MXFP4 weights do not fit 64 GB of VRAM with a usable KV cache. gpt-oss-puzzle-88B (50 GB) fits but has no agentic-coding evidence and uses NVIDIA's own license.

## D. UNVERIFIED / open items
- Muse Glimmer's SWE-bench harness (methodology report not read).
- Whether llama.cpp PR #24615 (Cohere parser) has merged.
- The minimum llama.cpp build for Mellum.
- An unsloth GGUF for Laguna XS 2.1 (401; may exist under auth).
- Independent (non-vendor) SWE-bench/TB results for Laguna XS 2.1, Muse Glimmer, North Mini Code, Bolt and Mellum2.1: none found. SWE-rebench's latest window (to 01/07/2026) does not list them.
- Exact Q4 sizes for Llama 4 Scout, Granite 4.0-H-Small, INTELLECT-3.1, Solar Open (not pulled).
- EXAONE license terms.
- Whether Llama 4 Scout's chunked attention reduces KV in vLLM.
