# Qwen open-weight models: verified facts + config.json values (as of 2026-11-03)

How this was collected: config.json fetched from `https://huggingface.co/<repo>/resolve/main/config.json`, model cards from `.../resolve/main/README.md`, param counts/license/creation date from `https://huggingface.co/api/models/<repo>`, file sizes from `https://huggingface.co/api/models/<repo>/tree/main?recursive=true` (summed LFS sizes; GB = 1e9 bytes, GiB = 2^30). Release dates come from the QwenLM GitHub News section (https://github.com/QwenLM/Qwen3.6, same text in QwenLM/Qwen3.5 and QwenLM/Qwen3.8 READMEs). Where News has no entry, the HF repo `createdAt` is given and labelled as such. The repo can be created a day or so before the public release.

## Name resolution (items 4 and 5)
- **"Qwen3.8-27B" exists.** HF repo `Qwen/Qwen3.8-27B` (created 2026-08-05). GitHub News: "2026-08-14: Qwen3.8-27B is now available". It is not a misnaming. The older dense 27Bs are `Qwen/Qwen3.6-27B` (2026-04-22) and `Qwen/Qwen3.5-27B` (2026-02-24). All three share the same architecture and config shape.
- **"Qwen3.6-35B-A3B" exists.** HF repo `Qwen/Qwen3.6-35B-A3B` (News: 2026-04-16). The predecessor is `Qwen/Qwen3.5-35B-A3B` (2026-02-24), with the same architecture. There is **no Qwen3.8-35B-A3B** in the Qwen HF org (the full list of Qwen repos created between Feb and Nov 2026 was checked).

## Summary table

| Model (HF repo) | Dense/MoE | Total / active | Attention | Native ctx | Extended ctx | License | Release | Thinking |
|---|---|---|---|---|---|---|---|---|
| `Qwen/Qwen3-Coder-Next` | MoE (512 experts, 10 routed + 1 shared) | 80B / 3B (HF safetensors: 79,674,391,296) | Hybrid: Gated DeltaNet + Gated Attention, 12×(3 DeltaNet → 1 full) | 262,144 | Not stated on card: **UNVERIFIED** | apache-2.0 | HF created 2026-01-30 (FP8/Base 2026-02-01). Blog: https://qwen.ai/blog?id=qwen3-coder-next | **Non-thinking only.** No `<think>` blocks; `enable_thinking=False` "no longer required" |
| `Qwen/Qwen3-Coder-30B-A3B-Instruct` | MoE (128 experts, 8 active) | 30.5B / 3.3B (HF: 30,532,122,624) | Full attention GQA (32Q/4KV), all 48 layers | 262,144 | "up to 1M tokens using Yarn" (card) | apache-2.0 | HF created 2025-07-31 | **Non-thinking only** (same note as above) |
| `Qwen/Qwen3-14B` | Dense | 14.8B (13.2B non-emb) (HF: 14,768,307,200) | Full attention GQA (40Q/8KV), all 40 layers | 32,768 (config max_position_embeddings=40,960) | 131,072 with YaRN factor 4.0 | apache-2.0 | HF created 2025-04-27 | **Hybrid.** `enable_thinking=False` in the chat template, or `/no_think` / `/think` soft switch per turn (only works when enable_thinking=True) |
| `Qwen/Qwen3.8-27B` | Dense | 27B (HF: 27,781,427,952 incl. vision) | Hybrid: 16×(3 Gated DeltaNet → 1 Gated Attention) | 262,144 | 1,000,000 (YaRN factor 4.0) | apache-2.0 | 2026-08-14 (News); HF created 2026-08-05 | Thinking **on by default**. Disable with `chat_template_kwargs: {"enable_thinking": False}`. Also `reasoning_effort` (xhigh default / medium / low) and `preserve_thinking` (default **True**). No /no_think mentioned |
| `Qwen/Qwen3.6-27B` | Dense | 27B (HF: 27,781,427,952) | same as 3.8-27B | 262,144 | 1,010,000 (YaRN 4.0) | apache-2.0 | 2026-04-22 | Thinking on by default. Disable with `enable_thinking: False`. `/think`, `/nothink` officially **not** supported. `preserve_thinking` opt-in (default off) |
| `Qwen/Qwen3.5-27B` | Dense | 27B (HF: 27,781,427,952) | same | 262,144 | 1,010,000 | apache-2.0 | 2026-02-24 | Same as 3.6 but no preserve_thinking |
| `Qwen/Qwen3.6-35B-A3B` | MoE (256 experts, 8 routed + 1 shared) | 35B / 3B (HF: 35,951,822,704) | Hybrid: 10×(3 Gated DeltaNet → 1 Gated Attention) | 262,144 | 1,010,000 | apache-2.0 | 2026-04-16 | As Qwen3.6-27B |
| `Qwen/Qwen3.5-35B-A3B` | MoE | 35B / 3B (HF: 35,951,822,704) | same | 262,144 | 1,010,000 | apache-2.0 | 2026-02-24 | As Qwen3.5-27B |

Thinking sources: Qwen3-Coder-Next card line "This model supports only non-thinking mode and does not generate `<think></think>` blocks..."; the same note is on the Qwen3-Coder-30B-A3B-Instruct card. Qwen3-14B card: "enable_thinking=True ... Default is True" and the soft-switch section. Qwen3.5 and 3.6 cards: "operate in thinking mode by default" and "does not officially support the soft switch of Qwen3, i.e., /think and /nothink". Qwen3.8-27B card: "Thinking mode is on by default and can be disabled per request" and `"enable_thinking": True, # on by default`, `"preserve_thinking": True, # on by default`, `reasoning_effort="xhigh", # xhigh by default; supported levels are xhigh, medium, and low`. On Qwen Cloud/Model Studio APIs, pass `enable_thinking` as a top-level field instead of inside `chat_template_kwargs`.

Recommended sampling (from cards): Qwen3.8 thinking temp 1.0 / top_p 0.95 / top_k 20 / presence 0.0; non-thinking temp 0.7 / top_p 0.8 / top_k 20 / presence 1.5. Qwen3-14B thinking temp 0.6 / top_p 0.95 / top_k 20; non-thinking temp 0.7 / top_p 0.8. Qwen3.5/3.6: advise keeping context ≥128K "to preserve thinking capabilities".

YaRN recipe (3.5 / 3.6 / 3.8): override `text_config.rope_parameters` to `{"mrope_interleaved": true, "mrope_section": [11,11,10], "rope_type": "yarn", "rope_theta": 10000000, "partial_rotary_factor": 0.25, "factor": 4.0, "original_max_position_embeddings": 262144}`, with `--max-model-len 1010000` (3.5/3.6) or `1000000` (3.8). Static YaRN can hurt short-context quality. Qwen3-14B: `{"rope_type":"yarn","factor":4.0,"original_max_position_embeddings":32768}`, or in llama.cpp `--rope-scaling yarn --rope-scale 4 --yarn-orig-ctx 32768`.

## config.json values (verbatim)
Sources: https://huggingface.co/Qwen/<name>/resolve/main/config.json. In the Qwen3.5/3.6/3.8 repos the text-model fields live under `text_config` (VLM wrapper `Qwen3_5ForConditionalGeneration` / `Qwen3_5MoeForConditionalGeneration`). In those configs the dtype field is `dtype` ("bfloat16"), not `torch_dtype`.

| field | Qwen3-Coder-Next | Qwen3-Coder-30B-A3B-Instruct | Qwen3-14B | Qwen3.8-27B (= 3.6-27B = 3.5-27B) | Qwen3.6-35B-A3B (= 3.5-35B-A3B) |
|---|---|---|---|---|---|
| architectures / model_type | Qwen3NextForCausalLM / qwen3_next | Qwen3MoeForCausalLM / qwen3_moe | Qwen3ForCausalLM / qwen3 | Qwen3_5ForConditionalGeneration / qwen3_5 (text: qwen3_5_text) | Qwen3_5MoeForConditionalGeneration / qwen3_5_moe (text: qwen3_5_moe_text) |
| num_hidden_layers | 48 | 48 | 40 | 64 | 40 |
| num_attention_heads | 16 | 32 | 40 | 24 | 16 |
| num_key_value_heads | 2 | 4 | 8 | 4 | 2 |
| head_dim | 256 | 128 | 128 | 256 | 256 |
| hidden_size | 2048 | 2048 | 5120 | 5120 | 2048 |
| intermediate_size | 5120 | 6144 | 17408 | 17408 | (absent) |
| vocab_size | 151936 | 151936 | 151936 | 248320 | 248320 |
| max_position_embeddings | 262144 | 262144 | 40960 | 262144 | 262144 |
| full_attention_interval | 4 | n/a | n/a | 4 | 4 |
| layer_types | (absent; implied by interval 4) | n/a | n/a | 64 entries: `linear_attention`×3 then `full_attention`, repeated 16× | 40 entries: same pattern repeated 10× |
| linear_num_key_heads | 16 | n/a | n/a | 16 | 16 |
| linear_num_value_heads | 32 | n/a | n/a | 48 | 32 |
| linear_key_head_dim | 128 | n/a | n/a | 128 | 128 |
| linear_value_head_dim | 128 | n/a | n/a | 128 | 128 |
| linear_conv_kernel_dim | 4 | n/a | n/a | 4 | 4 |
| num_experts | 512 | 128 | n/a | n/a | 256 |
| num_experts_per_tok | 10 | 8 | n/a | n/a | 8 |
| moe_intermediate_size | 512 | 768 | n/a | n/a | 512 |
| shared_expert_intermediate_size | 512 | n/a | n/a | n/a | 512 |
| partial_rotary_factor | 0.25 | n/a | n/a | 0.25 (in rope_parameters) | 0.25 |
| rope_theta | 5000000 | 10000000 | 1000000 | 10000000 | 10000000 |
| attn_output_gate | (absent) | n/a | n/a | true (output_gate_type "swish" in 3.6/3.8) | true |
| mtp_num_hidden_layers | (absent) | n/a | n/a | 1 | 1 |
| mamba_ssm_dtype | (absent) | n/a | n/a | "float32" | "float32" |
| tie_word_embeddings | false | false | false | false | false |
| torch_dtype / dtype | bfloat16 | bfloat16 | bfloat16 | bfloat16 | bfloat16 |
| vision tower | none | none | none | depth 27, hidden 1152, out_hidden 5120 | depth 27, hidden 1152, out_hidden 2048 |

Full-attention layers (0-indexed, from layer_types): 3, 7, 11, ... i.e. every layer where (i+1) % 4 == 0. The 27B models have 16 full-attention layers and 48 linear. The 35B-A3B models have 10 full and 30 linear. Qwen3-Coder-Next has no layer_types list, but `full_attention_interval: 4` and the card's "12 * (3 * (Gated DeltaNet -> MoE) -> 1 * (Gated Attention -> MoE))" give 12 full and 36 linear.

Differences between 3.5, 3.6 and 3.8 configs (otherwise identical shapes): 3.6/3.8 add `bos_token_id: 248044`, `output_gate_type: "swish"`, and top-level `partial_rotary_factor`. transformers_version is 4.57.0.dev0 (3.5), 4.57.1 (3.6), 5.8.0.dev0 (3.8). The HF param count is identical (27,781,427,952 / 35,951,822,704), so the weights differ but the shapes match.

### Derived per-token / per-sequence cache sizes (my arithmetic from the configs above, not from a source)
- Full-attention KV per token, BF16 = full_layers × kv_heads × head_dim × 2 (K+V) × 2 bytes:
  - Qwen3-14B: 40×8×128×2×2 = 163,840 B (160 KiB/token)
  - Qwen3-Coder-30B-A3B: 48×4×128×2×2 = 98,304 B (96 KiB/token)
  - Qwen3-Coder-Next: 12×2×256×2×2 = 24,576 B (24 KiB/token)
  - Qwen3.5/3.6/3.8-27B: 16×4×256×2×2 = 65,536 B (64 KiB/token)
  - Qwen3.5/3.6-35B-A3B: 10×2×256×2×2 = 20,480 B (20 KiB/token)
- Gated DeltaNet recurrent state per sequence (fixed, does not grow with context). Each linear layer holds linear_num_value_heads × key_dim × value_dim elements, plus a conv state of (2×k_heads×k_dim + v_heads×v_dim) × (kernel−1):
  - Coder-Next: 36 layers × (32×128×128 = 524,288 + 8,192×3 = 24,576) elements
  - 27B: 48 layers × (48×128×128 = 786,432 + 10,240×3 = 30,720)
  - 35B-A3B: 30 layers × (524,288 + 24,576)
  - The configs set mamba_ssm_dtype float32 for the 3.5/3.6/3.8 SSM state, so about 4 B/element for the recurrent part. For example, 27B is about 48 × 786,432 × 4 B ≈ 151 MB per sequence. In the conv-state size formula, 2×k_heads×k_dim is the q and k channels. This is the vLLM/HF layout; the exact cache layout is engine-dependent and **UNVERIFIED**.

## Published file sizes

### Official BF16 / FP8 / INT4 (Qwen org)
| Repo | Size |
|---|---|
| https://huggingface.co/Qwen/Qwen3-Coder-Next (BF16) | 159.36 GB (148.41 GiB) |
| https://huggingface.co/Qwen/Qwen3-Coder-Next-FP8 | 80.38 GB (74.86 GiB) |
| https://huggingface.co/Qwen/Qwen3-Coder-Next-GGUF (official) | Q4_K_M 48.41 GB, Q5_K_M 56.71 GB, Q6_K 65.53 GB, Q8_0 84.81 GB |
| https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct (BF16) | 61.07 GB |
| https://huggingface.co/Qwen/Qwen3-Coder-30B-A3B-Instruct-FP8 | 31.18 GB (29.03 GiB) |
| https://huggingface.co/Qwen/Qwen3-14B (BF16) | 29.54 GB |
| https://huggingface.co/Qwen/Qwen3-14B-FP8 | 16.33 GB (15.21 GiB) |
| https://huggingface.co/Qwen/Qwen3-14B-AWQ (official AWQ) | 9.98 GB (9.29 GiB) |
| https://huggingface.co/Qwen/Qwen3.8-27B (BF16) | 55.56 GB (51.75 GiB) |
| https://huggingface.co/Qwen/Qwen3.8-27B-FP8 | 30.87 GB (28.75 GiB) |
| https://huggingface.co/Qwen/Qwen3.6-27B-FP8 | 30.87 GB |
| https://huggingface.co/Qwen/Qwen3.5-27B-FP8 | 30.87 GB |
| https://huggingface.co/Qwen/Qwen3.5-27B-GPTQ-Int4 | 30.24 GB (as listed; oddly close to FP8, presumably many tensors left unquantized; not investigated) |
| https://huggingface.co/Qwen/Qwen3.6-35B-A3B (BF16) | 71.90 GB |
| https://huggingface.co/Qwen/Qwen3.6-35B-A3B-FP8 | 37.46 GB (34.89 GiB) |
| https://huggingface.co/Qwen/Qwen3.5-35B-A3B-FP8 | 37.46 GB |
| https://huggingface.co/Qwen/Qwen3.5-35B-A3B-GPTQ-Int4 | 24.42 GB (22.74 GiB) |
| No official Qwen AWQ/GPTQ exists for Qwen3.6-* or Qwen3.8-27B | (checked the full Qwen org listing) |

### unsloth GGUF (single-file totals; split shards summed)
| Repo | Q6_K | Q5_K_M | UD-Q4_K_XL | others |
|---|---|---|---|---|
| https://huggingface.co/unsloth/Qwen3-Coder-Next-GGUF | 65.61 GB | 56.85 GB | 49.61 GB | Q4_K_M 48.53, UD-Q6_K_XL 73.12, Q8_0 84.81 |
| https://huggingface.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF | 25.09 GB | 21.73 GB | 17.67 GB | Q4_K_M 18.56, UD-Q6_K_XL 26.34, Q8_0 32.48 |
| https://huggingface.co/unsloth/Qwen3-14B-GGUF | 12.12 GB | 10.51 GB | 9.16 GB | Q4_K_M 9.00, Q8_0 15.70 |
| https://huggingface.co/unsloth/Qwen3.8-27B-GGUF | no plain Q6_K; UD-Q6_K 21.98 GB | no plain Q5_K_M; UD-Q5_K_M 19.77 GB | 17.56 GB | UD-Q4_K_M 16.46, UD-Q6_K_XL 25.30, Q8_0 29.05, mmproj-BF16 0.93 |
| https://huggingface.co/unsloth/Qwen3.6-27B-GGUF | 22.52 GB | 19.51 GB | 17.61 GB | Q4_K_M 16.82, Q8_0 28.60, mmproj-BF16 0.93 |
| https://huggingface.co/unsloth/Qwen3.5-27B-GGUF | 22.45 GB | 19.61 GB | 17.62 GB | Q4_K_M 16.74, Q8_0 28.60 |
| https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF | no plain Q6_K; UD-Q6_K 29.31 GB | no plain Q5_K_M; UD-Q5_K_M 26.46 GB | 22.36 GB | UD-Q4_K_M 22.13, Q8_0 36.90, mmproj-BF16 0.90 |
| https://huggingface.co/unsloth/Qwen3.5-35B-A3B-GGUF | 28.85 GB | 26.25 GB | 22.24 GB | Q4_K_M 22.02, Q8_0 36.90 |

### Community AWQ / INT4 (safetensors totals)
- Qwen3-Coder-Next: https://huggingface.co/cyankiwi/Qwen3-Coder-Next-AWQ-4bit is 48.25 GB. Also qtum/Qwen3-Coder-Next-AWQ (most downloads; size not fetched).
- Qwen3-Coder-30B-A3B: https://huggingface.co/cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit is 18.09 GB. https://huggingface.co/QuantTrio/Qwen3-Coder-30B-A3B-Instruct-AWQ is 16.81 GB.
- Qwen3.8-27B: https://huggingface.co/cyankiwi/Qwen3.8-27B-AWQ-INT4 is 21.02 GB. https://huggingface.co/RedHatAI/Qwen3.8-27B-INT4 is 19.45 GB.
- Qwen3.6-27B: https://huggingface.co/cyankiwi/Qwen3.6-27B-AWQ-INT4 is 20.44 GB. https://huggingface.co/QuantTrio/Qwen3.6-27B-AWQ is 21.85 GB.
- Qwen3.6-35B-A3B: https://huggingface.co/QuantTrio/Qwen3.6-35B-A3B-AWQ is 25.46 GB. https://huggingface.co/cyankiwi/Qwen3.6-35B-A3B-AWQ-4bit is 24.97 GB.
- Sizes include the vision tower and MTP weights where the repo keeps them. Quality of community quants is not verified.

## Other notable Qwen releases, May to Nov 2026 (from the HF Qwen org listing)
- **`Qwen/Qwen3.8-Flash-Next`** (+ `-FP8`), HF created 2026-08-24. License **other: `qwen-community-1.0`** (not Apache). This is an experimental preview of the "Qwen4" architecture (`Qwen4ExpForConditionalGeneration`, model_type `qwen4_exp`).
  - Size: 125B total with 6B active, plus 51B n-gram embedding and 4B MTP (HF count 179,999,981,459).
  - Layers: 48, as 12×(3 Gated DeltaNet → 1 Qwen Sparse Attention (QSA)).
  - Config: hidden 2560, 24Q/2KV heads, head_dim 256, linear 16 key heads / 48 value heads with head dims 128/128, conv 4, 512 experts with 10 routed + 1 shared, moe_intermediate 640, vocab 248320, max_pos 262144. Indexer: 4 heads, 1 KV head, dim 128, budget 2048, compress 4. ngram_vocab_size_base 20,000,000; hc_count 4 (gated residual branches).
  - Context: 262,144 native, up to 1,000,000 with YaRN.
  - Thinking: on by default, with enable_thinking / preserve_thinking / reasoning_effort like 3.8-27B.
  - Sizes: BF16 360.00 GB, FP8 185.52 GB, unsloth UD-Q4_K_XL 111.33 GB (https://huggingface.co/unsloth/Qwen3.8-Flash-Next-GGUF).
  - Card: https://huggingface.co/Qwen/Qwen3.8-Flash-Next. Blog: https://qwen.ai/blog?id=qwen3.8-flash-next.
  - Not Pi-scale.
- **`Qwen/Qwen3.8-2.4T-A95B`** (+FP8), released 2026-08-12 (News). License other: `qwen3.8-max`. Size 2.4T total with 95B active, 23×(3 DeltaNet → 1 Gated Attention), 262K native and 1,010,000 extended. Not local-scale.
- **`Qwen/Qwen-AgentWorld-35B-A3B`**, HF created 2026-06-22, apache-2.0. This is a "language world model" that simulates agent environments (MCP, Search, Terminal, SWE, Android, Web, OS); it is not a coding assistant.
  - Base: Qwen3.5-35B-A3B-Base. The config is identical in shape to Qwen3.5-35B-A3B (40 layers, 16Q/2KV, head_dim 256, hidden 2048, 256 experts with top-8, moe_int 512, linear 16/32/128/128/4, vocab 248320, max_pos 262144), with `language_model_only: true`.
  - Size: HF count 34,660,610,688 (no vision weights). Context 262,144.
  - Thinking by default (card: "The model uses thinking mode by default").
  - Card: https://huggingface.co/Qwen/Qwen-AgentWorld-35B-A3B
- No new Qwen3-Coder checkpoints after Qwen3-Coder-Next (Jan/Feb 2026). The Qwen org search for "Qwen3-Coder" returns only 480B-A35B, 30B-A3B, and Next variants. The Qwen3.8-27B card positions that model as the coding/agent upgrade (it reports SWE-bench Pro 61.7 vs 53.5 for Qwen3.6-27B, Terminal Bench 2.1 73.0 vs 63.4; vendor numbers).
- Others found but not relevant: Qwen-Drive-1.0-4B (2026-08-27), Qwen-Image-2.1 family, Qwen3-ASR/ForcedAligner -hf ports.

## UNVERIFIED / open items
- Qwen3-Coder-Next extended (YaRN) context: the card states only "262,144 natively"; no extension figure is given.
- Exact public release date of Qwen3-Coder-Next. The HF repo was created 2026-01-30 and the blog is linked, but the date was not read from the blog (qwen.ai is JS-rendered). Same for Qwen3.8-Flash-Next (HF created 2026-08-24) and Qwen-AgentWorld (HF created 2026-06-22).
- Whether llama.cpp / vLLM store DeltaNet state in fp32 (engine-specific).
