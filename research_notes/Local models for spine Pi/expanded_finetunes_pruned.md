# Expanded scope: agentic-coding fine-tunes and pruned/compressed MoEs (as of 2026-11-04)

Scope: (A) SWE/agentic fine-tunes and RL'd coders of open bases, up to about 80B params. (B) Pruned (REAP and similar) or very-low-bit versions of 100–300B MoEs that fit 48–64 GB.
Target: the Pi coding agent (read/bash/edit/write, OpenAI-style tool calls) on vLLM/SGLang/llama.cpp with 16–64 GB.

Method: config.json, file sizes, licences and dates come from the HF API (`/api/models/<id>?blobs=true`, `/raw/main/config.json`, `/raw/main/README.md`), fetched 2026-11-04. Sizes are decimal GB summed from the HF file listing. Benchmarks are quoted from the source named next to each one. "Vendor" means the model's own authors reported it; "indep" means a third party did.
The SWE-bench leaderboard data is `SWE-bench/swe-bench.github.io` `data/leaderboards.json` (master). Its newest Verified entry is dated 2026-02-26. **None of the fine-tunes below has a bash-only (mini-swe-agent) row**; the only bash-only rows that matter here are for the *bases* of the pruned models.

KV/token is my arithmetic, using the convention in `memory_calc.md`: full-attention layers × kv_heads × head_dim × 2 (K+V) × 1 byte (FP8); for MLA, layers × (kv_lora_rank + qk_rope_head_dim).

---

## TL;DR ranked shortlist for Pi

1. **`Kwaipilot/KAT-Coder-V2.5-Dev`** (Qwen3.6-35B-A3B + SFT/RL, Apache-2.0, 2026-07-23). It was evaluated in the **Claude Code harness**, whose Read/Edit/Write/Bash tools closely match Pi's four. RL penalised failed or empty tool calls and excessive parallel calls. Vendor-run in-house: SWE-V 69.4 vs 64.4 for base Qwen3.6-35B-A3B under the same pipeline; TB 2.1 41.0 (avg of Terminus-2 and Claude Code). Q4_K_M is 21.4 GB, AWQ-INT4 24.4 GB, and KV is 10 KiB/token at FP8, so it fits 24–32 GB with long context. **Best fit.**
2. **`ornith-ai/Ornith-1.5-35B-A3B`** (Qwen3.5/3.6-35B-A3B lineage, MIT, 2026-08-18). Vendor claims are very high: SWE-V 79 (OpenHands), TB 2.1 67.8 (Terminus-2) and 68.5 (Claude Code). **Caution:** Kwaipilot's same-pipeline re-run of the *previous* Ornith-1.0-35B got SWE-V 55.8 and TB 2.1 35.98. Ornith's own figures for 1.0 were 75.6 and 64.2, so harness and eval differences are large. Same size and KV as #1. A/B it against #1 inside Pi.
3. **`ISTA-DASLab/Qwen3.8-Flash-Next-GSQ-RCO-Coder-GGUF`** (50% expert-pruned with RCO and quantised with GSQ, from the 177B Qwen3.8-Flash-Next). 58.4 GB on disk, but only **29.6 GB must be resident**; the other 28.8 GB is an n-gram lookup table that can stay on disk. Vendor: SWE-V 79.20 vs 82.80 for BF16 (95.7% retained), harness not stated. llama.cpp only, and the card calls it experimental. This is the strongest "big model in 32–64 GB" option, but no independent numbers exist.
4. **64 GB tier only: `cerebras/MiniMax-M2.5-REAP-139B-A10B` as i1-IQ3_XXS (53.6 GB) or i1-Q2_K (50.8 GB)** from `mradermacher/MiniMax-M2.5-REAP-139B-A10B-i1-GGUF`. Its *base* has the best independent bash-only score of anything here (MiniMax M2.5 (high) 75.8, mini-swe-agent 2.0.0, SWE-bench leaderboard). However, 40% REAP plus ~3-bit quantisation compounds, nobody has measured the result on agentic tasks, and KV is large (124 KiB/token FP8), so context stays around 32–64K.
5. **`allenai/tmax-27b`** (Qwen3.6-27B + DPPO RL, Apache-2.0). Trained and evaluated in a **mini-swe-agent-like persistent-shell harness**, which transfers most naturally to Pi's bash-heavy loop. The gain over base is small, though: TB 2.0 42.7 vs 39.6, TB 2.1 44.9 vs 40.5, TB Lite 68.6 vs 70.8 (vendor). Use it only if Qwen3.6-27B is already the plan and bash-loop behaviour matters. The alternative is `Logics-MLLM/Logics-SWE-Qwen3.6-27B`, which has SWE-bench Pro gains (vendor) but an unstated harness.

Not recommended: all 2025 Qwen2.5-Coder-32B and Qwen3-32B SWE fine-tunes (OpenHands-LM, SWE-agent-LM, Skywork-SWE, SWE-Dev, R2EGym, DeepSWE, SERA, KAT-Dev-32B, OpenThinkerAgent-32B, SWE-Master). They have 32–40K native context, 128 KiB/token KV, scaffold-specific formats (SWE-agent/OpenHands/R2E with `submit`/`finish` tools), and SWE-V scores at or below the Qwen3.6 bases already covered.
Kimi-Dev-72B and KAT-Dev-72B-Exp are dense 72B models with 160 KiB/token KV, too heavy for the gain. CWM is non-commercial.
gpt-oss-120b MXFP4 is 65.25 GB of weights alone, which exceeds 64 GB with KV, and its bash-only score was 26.0 (indep, Aug 2025).

---

## Scaffold-transfer notes (applies to everything in A)

- Pi's tools are read/bash/edit/write. Claude Code's are Read/Edit/Write/Bash plus extras, so models RL'd in the Claude Code harness should transfer best (KAT-Coder-V2.5-Dev; Ornith also reports Claude Code results).
- KAT's own card shows the failure mode: Qwen3.5-35B-A3B and Gemma-4-26B-A4B "hallucinated calls to the unsupported MultiEdit tool" under Claude Code, which cost several points. Expect the same with Pi: models will try tools Pi doesn't have. Watch Pi logs for unknown-tool calls. [src: KAT-Coder-V2.5-Dev card, "Anomaly description"]
- SWE-agent/SWE-smith-trained models (SWE-agent-LM, SERA, KAT-Dev-72B-Exp) learned `str_replace_editor` + `submit` style tools. SERA's card warns it "may attempt to call a nonexistent `submit` tool when finished". [src: allenai/SERA-32B card]
- R2E-Gym-trained models (DeepSWE, R2EGym-Agent) use `file_editor`, `execute_bash`, `search` and `finish` with a non-function-calling text format. [src: DeepSWE-Preview card]
- OpenHands-trained models (OpenHands-LM, Skywork-SWE, SWE-Dev, nebius SWE-rebench) learned OpenHands' `execute_bash`/`str_replace_editor`/`finish` function-call schema.
- Evidence that RL gains survive a harness change: Tmax-9B beats base Qwen3.5-9B under all four harnesses tested on TB Lite (authors' harness 65.6 vs 49.4; mini-SWE-agent 57.8 vs 44.9; Terminus-2 46.1 vs 37.9; OpenHands 42.7 vs 29.0). Absolute scores still vary by about 20 points across harnesses. [src: arXiv 2606.23321, Table 5]

---

## A) Agentic-coding fine-tunes / RL'd SWE models

### A1. `Kwaipilot/KAT-Coder-V2.5-Dev` (2026). **Top pick.**
- Base: Qwen3.6-35B-A3B (card YAML `base_model: Qwen3.6-35B-A3B`). SFT on 127K examples, then RL for 10 epochs (TITO, truncated IS, hierarchical harness rewards, plus penalties for excess parallel tool calls, failed/empty tool calls and repetition). The RL reduced "abnormal tool labels" from 9.34% to 0.28%. [card]
- Params: 34.66B total (HF safetensors metadata), about 3B active (card). Text-only release; the vision tower is not included.
- Created 2026-07-23. Licence apache-2.0. BF16 safetensors 69.33 GB.
- config.json (`text_config`, verbatim values): `model_type qwen3_5_moe`, `num_hidden_layers 40`, `layer_types {linear_attention: 30, full_attention: 10}`, `full_attention_interval 4`, `num_attention_heads 16`, `num_key_value_heads 2`, `head_dim 256`, `hidden_size 2048`, `linear_num_key_heads 16`, `linear_num_value_heads 32`, `linear_key_head_dim 128`, `linear_value_head_dim 128`, `num_experts 256`, `num_experts_per_tok 8`, `moe_intermediate_size 512`, `max_position_embeddings 262144`.
  → KV FP8 = 10 × 2 × 256 × 2 = **10.0 KiB/token**, plus a fixed GDN state per sequence (same architecture as Qwen3.6-35B-A3B, ~64 MB, see `memory_calc.md`).
- Native context 262,144; YaRN to about 1M is shown on the card.
- Thinking is on by default. Turn it off with `chat_template_kwargs: {"enable_thinking": false}`. It also supports `preserve_thinking: true`, and the card says it was "additionally trained to preserve" historical thinking.
- Serving (card): SGLang ≥0.5.10 / vLLM with `--reasoning-parser qwen3 --tool-call-parser qwen3_coder --enable-auto-tool-choice`. llama.cpp: GGUFs exist (qwen3.5-MoE arch, same as Qwen3.6 GGUFs). Tool calls need `--jinja`; see `serving_benchmarks.md` for Qwen3.6 llama.cpp caveats.
- Files: `bartowski/Kwaipilot_KAT-Coder-V2.5-Dev-GGUF` has Q8_0 36.91, Q6_K 30.05, Q5_K_M 25.02, **Q4_K_M 21.39**, IQ4_XS 18.81, Q3_K_M 16.23, Q2_K 12.62 GB. `cyankiwi/KAT-Coder-V2.5-Dev-AWQ-INT4` is 24.43 GB (compressed-tensors, group 32). An NVFP4A16 build exists (`sahilchachra/KAT-Coder-V2.5-Dev-NVFP4A16`, size not checked). There is no official FP8.
- Benchmarks (vendor, in-house re-run of all models: Claude Code 2.1.195, pass@1, T=1.0, 256K ctx, one run each):
  - SWE-V **69.40**. Same pipeline: Qwen3.5-27B 68.60, Qwen3.6-35B-A3B 64.40, Gemma4-31B 60.60, Qwen3.5-35B-A3B 58.60, Ornith-1.0-35B 55.80, Gemma4-26B-A4B 35.80, Qwen3-Coder-30B 31.80.
  - SWE Multilingual 63.00; SWE-bench Pro 45.96.
  - **Terminal-Bench 2.1** 41.02 (Terminus-2 32.60 / Claude Code 49.44).
  - The card notes its Qwen3.6 numbers sit about 10 pp below Qwen's official figures (harness difference).
  - No TB 2.0 or bash-only result exists. **Independent: none found.**
- Pruned derivative: `Ttimms/KAT-Coder-V2.5-Dev-REAP-50-GGUF` (community; 256→128 experts, about 19B). Q4_K_M 11.37 GB, Q8_0 19.75 GB. Card reports "SWE-bench Verified 52.0%" for its NVFP4A16 build, single draw, harness **UNVERIFIED**. That is a big drop from 69.4, though the harness may differ. Not recommended.

### A2. `ornith-ai/Ornith-1.5-35B-A3B` (2026)
- "Developed on top of Qwen3.5 and Gemma4 with additional continued pretraining, mid-training, and post-training". RL jointly optimises task generation, scaffold and rollouts. [card] The HF API shows no `base_model` tag, so the exact base checkpoint is **UNVERIFIED** (architecture matches Qwen3.5/3.6-35B-A3B).
- Params: 35.95B (HF metadata), about 3B active. MIT licence. Created 2026-08-18. BF16 71.9 GB.
- config: identical to A1 (`qwen3_5_moe`, 40 layers, 30 linear + 10 full, kv_heads 2, head_dim 256, 256 experts, top-8, max_pos 262144). KV FP8 10.0 KiB/token.
- Thinking on by default (`<think>`). Serving: vLLM `--reasoning-parser qwen3 --tool-call-parser qwen3_xml`; SGLang `--tool-call-parser qwen3_coder`. The card says it ships a modified chat template "to ensure consistency between training and inference".
- Files: `ornith-ai/Ornith-1.5-35B-A3B-GGUF` (official) has Q8_0 37.8, Q6_K 29.21, Q5_K_M 25.35, **Q4_K_M 21.71**, BF16 71.07 GB, plus mmproj 0.9. `ornith-ai/Ornith-1.5-35B-A3B-NVFP4` exists (size not checked).
- Benchmarks (vendor):
  - TB 2.1 (Terminus-2, 5-run avg, 128K) **67.8**; TB 2.1 (Claude Code 2.1.126) 68.5.
  - SWE-V **79** (OpenHands, 256K, T=1.0); SWE-Pro 59.6; SWE-Multilingual 71.4.
  - Same table, Qwen3.6-35B-A3B: TB 2.1 52.5 / SWE-V 73.4.
- Independent cross-check: Kwaipilot's pipeline measured the predecessor Ornith-1.0-35B at SWE-V 55.80 and TB 2.1 35.98, against Ornith's own 75.6 and 64.2. BenchLM lists the family as lacking independent benchmark coverage ([benchlm.ai](https://www.benchlm.ai/models/ornith-1-5-35b-a3b)). **Treat the vendor numbers as unconfirmed.**

### A3. `allenai/tmax-27b` (Ai2, 2026)
- Base Qwen3.6-27B (HF tag), trained with DPPO RL on TMax-15K terminal tasks. Apache-2.0. Created 2026-06-21. Safetensors 53.79 GB.
- config: `qwen3_5` dense, `num_hidden_layers 64`, `layer_types {linear_attention: 48, full_attention: 16}`, `num_attention_heads 24`, `num_key_value_heads 4`, `head_dim 256`, `hidden_size 5120`, linear 16/48 heads × 128, `max_position_embeddings 262144`. KV FP8 = 16 × 4 × 256 × 2 = **32 KiB/token**.
- Trained with max 65,536 tokens and 64 tool calls (paper). The card serves it at `--max-model-len 65536`, `--tool-call-parser qwen3_xml`, `--language_model_only` (vision head removed).
- Harness: "a simple harness based on mini-SWE-agent", persistent shell, interleaved thinking preserved across turns. [arXiv 2606.23321]
- Benchmarks (vendor; podman backend; 3 runs for Lite/2.1, 5 runs for TB 2.0 on daytona):
  - TB 2.0 **42.7±0.7** vs base Qwen3.6-27B 39.6±2.1.
  - TB 2.1 44.9±1.8 vs 40.5±2.4. The arXiv Table 3 instead gives 43.9±1.6 vs 43.6±1.9, so the card and paper disagree.
  - TB Lite 68.6 vs 70.8 (the card's base is better here).
  - **The gain is marginal.**
- GGUF: the only tmax-27b GGUF found, `mradermacher/tmax-27b-v1.1-i1-GGUF`, quantises `TMaxxx/tmax-27b-v1.1`, a **non-Ai2 repo**. Its provenance is UNVERIFIED (Q4_K_M 16.55 GB). For the official weights, quantise yourself or use FP8 online quant.

### A4. `Logics-MLLM/Logics-SWE-Qwen3.6-27B` (2026-09-18) and `Logics-MLLM/Logics-SWE-Qwen3.6-35B-A3B` (2026-10-30)
- Apache-2.0. Bases Qwen3.6-27B and Qwen3.6-35B-A3B (HF tags). Training: RRE category experts (RL + supervised repair), then distilled into a student. Paper arXiv 2609.23377, code github.com/alibaba/AgenticBigBang.
- configs: identical to Qwen3.6-27B (64 layers, 16 full, kv 4, hd 256 → 32 KiB/token) and to Qwen3.6-35B-A3B (→ 10 KiB/token).
- Benchmarks (vendor; each pair is base → model):
  - 27B: SWE-bench Pro 52.94 → **56.41**; Multilingual 67.89 → 71.78; Pro V2 81.62 → 86.19.
  - 35B-A3B: Pro 48.75 → 51.12; Multilingual 63.44 → 66.44.
  - **No SWE-V or TB numbers.** Harness **UNVERIFIED**; the card says agent use "requires the matching system prompt, tool schema, action parser", which points to a custom scaffold.
- GGUF: `bartowski/Logics-MLLM_Logics-SWE-Qwen3.6-35B-A3B-GGUF` has Q4_K_M 22.32, Q8_0 36.91 GB. `mradermacher/Logics-SWE-Qwen3.6-27B-i1-GGUF` has Q4_K_M 16.55, Q6_K 22.08 GB.

### A5. `allenai/SERA-32B` / `SERA-8B` (Ai2 "Open Coding Agents", 2026-01-27)
- Base Qwen3-32B / Qwen3-8B. SFT on 25K synthetic trajectories (teacher GLM-4.6, "Soft Verified Generation"). Apache-2.0. Safetensors 65.52 / 16.38 GB.
- config (32B): `qwen3`, `num_hidden_layers 64`, `num_key_value_heads 8`, `head_dim 128`, `max_position_embeddings 40960`. KV FP8 128 KiB/token. 8B: 36 layers, kv 8, hd 128 → 72 KiB/token.
- **Context: 32K (trained and evaluated).** Serving: vLLM `--tool-call-parser hermes`.
- SWE-V (vendor, 32K ctx, 3 seeds): 32B **49.5±1.9**, 32B-GA 46.6, 8B 31.7. Scaffold: SWE-agent is the default data-gen harness (github.com/allenai/SERA). Card warns of hallucinated `submit` calls. Weak fit for Pi.

### A6. Older (2025) fine-tunes for reference. **None recommended.**

| HF repo | Base | Released | Licence | Native ctx | KV FP8/token | SWE-V (harness, source) |
|---|---|---|---|---|---|---|
| `OpenHands/openhands-lm-32b-v0.1` | Qwen2.5-Coder-32B-Instruct | 2025-03-26 | MIT | 32768 (+YaRN ×4 in config → 128K) | 128 KiB | 37.2 (OpenHands iterative; vendor) |
| `SWE-bench/SWE-agent-LM-32B` | Qwen2.5-Coder-32B-Instruct | 2025-04-29 | Apache-2.0 | 32768 | 128 KiB | 40.2 (SWE-agent; leaderboard "checked": true) |
| `zai-org/SWE-Dev-32B` | Qwen2.5-Coder-32B-Instruct | 2025-04-06 | MIT | 32768 | 128 KiB | 36.6 (OpenHands, 75 rounds; vendor) |
| `R2E-Gym/R2EGym-32B-Agent` | Qwen2.5-Coder-32B (UNVERIFIED tag) | 2025-02-12 | none tagged | 32768 | 128 KiB | 34.4 (R2E-Gym; per DeepSWE card) |
| `Skywork/Skywork-SWE-32B` | Qwen2.5-Coder-32B-Instruct | 2025-06-11 | Apache-2.0 | 32768 | 128 KiB | 38.0 (OpenHands v0.32; leaderboard checked); 47.0 with Bo8 TTS |
| `agentica-org/DeepSWE-Preview` | Qwen3-32B (pure RL) | 2025-07-01 | MIT | 40960 (eval 64K) | 128 KiB | 42.2 pass@1 avg16 (R2E-Gym tools; leaderboard unchecked); 59.0 hybrid Bo16 |
| `Kwaipilot/KAT-Dev` (32B) | Qwen3-32B | 2025-09-15 | Apache-2.0 | 131072 | 128 KiB | 62.4 (vendor; harness UNVERIFIED in card) |
| `Kwaipilot/KAT-Dev-72B-Exp` | Qwen2.5-72B arch | 2025-10-10 | Apache-2.0 | 131072 | 160 KiB | 74.6 ("strictly with the SWE-agent scaffold"; vendor) |
| `moonshotai/Kimi-Dev-72B` | Qwen2.5-72B | 2025-06-16 | MIT | 131072 | 160 KiB | 60.4 (Agentless workflow, not agentic; vendor). Paper: 48.6 pass@1 as SWE-agent after light SFT ([arXiv 2509.23045](https://ar5iv.labs.arxiv.org/html/2509.23045)) |
| `facebook/cwm` | Meta-trained 32B dense | 2025-08/09 | **FAIR non-commercial research** (gated) | 131072; local/global 3:1, windows 8192/131072 | config gated, **UNVERIFIED** | 53.9 / 65.8 with TTS (vendor, [HF card](https://huggingface.co/facebook/cwm)). Tools bash/edit/create/submit (secondary sources; UNVERIFIED in primary) |
| `nebius/SWE-rebench-openhands-Qwen3-30B-A3B` | Qwen3-30B-A3B-Instruct-2507 (RFT) | 2025-12-01 | Apache-2.0 | 262144 | 48 KiB | 50.3 pass@1 at 500 turns (OpenHands v0.54.0; vendor) vs 50.0 for Qwen3-Coder-30B-A3B |
| `open-thoughts/OpenThinkerAgent-32B` | Qwen3-32B (SFT 100K, GLM-4.7 teacher) | 2026-06-08 | Apache-2.0 | 40960 | 128 KiB | SWE-V-100 subset 55.7, TB 2.0 26.2 (Terminus-2; vendor) |
| `RUC-AIBOX/SWE-Master-32B-RL` | Qwen2.5-32B arch | 2026-02-10 | none tagged | 32768 | 128 KiB | not checked |
| `NousResearch/NousCoder-14B` | Qwen3-14B | 2025-12-23 | Apache-2.0 | 81920 | 80 KiB | competitive programming only (LCB v6 67.87); not agentic |

Configs above (verbatim from config.json): Qwen2.5-32B family `num_hidden_layers 64, num_attention_heads 40, num_key_value_heads 8, hidden_size 5120` (head_dim = 128 implied). Qwen3-32B `64 / 64 / 8, head_dim 128`. 72B `80 / 64 / 8, hidden 8192` (head_dim 128).
Other 2026 items seen but not evaluated: `microsoft/AesCode-32B` (Qwen3-VL-32B, aesthetic web code, not agentic); `open-athena/Marin-Snowball-67B-A2B-v0.1` (an RL checkpoint whose card says SWE-V/TBLite runs "encountered endpoint errors"; not usable); `laion/*` research checkpoints; `nex-agi/Qwen3-30B-A3B-Nex-N1` (Nov 2025); `Lego-X/SWE-Lego-Qwen3-32B` and `GAIR/daVinci-Dev-32B/72B` (Jan 2026, Qwen3/2.5 dense, not checked). No 2026 open SWE fine-tunes were found from Prime Intellect (INTELLECT-3.1 is a 106B GLM-4.5-Air-Base generalist, 213.7 GB BF16), Salesforce, Microsoft or ByteDance in their HF org listings as of 2026-11-04.

---

## B) Pruned / compressed large MoEs into 48–64 GB

### Evidence on quality loss (agentic coding, tool calling)

- **REAP paper** (arXiv 2510.13999, Cerebras authors, so vendor). SWE-V uses the **mini-SWE-agent** scaffold, 64K input, sb-cli scoring, greedy, with up to 20 restarts if no patch. BFCLv3 uses the Gorilla harness.
  - Qwen3-Coder-480B-FP8: SWE-V 0.540 baseline → REAP 25% **0.540** → REAP 50% **0.522**. BFCLv3 multi-turn 0.380 → 0.392 → 0.371.
  - Kimi-K2-Instruct-W4A16: SWE-V 0.554 → 0.580 (25%) → 0.576 (50%). **BFCLv3 multi-turn 0.355 → 0.263 (25%) → 0.164 (50%)**; overall 0.666 → 0.635 → 0.564.
  - **Takeaway:** single-agent SWE patches survive pruning well, but multi-turn tool calling can collapse. That is Pi's core loop.
  - Frequency-based pruning at 50% scored 0.000 on SWE-V.
  - Table A11, BFCLv3 by REAP ratio: MiniMax-M2 0.626 → 0.615 (25%) → 0.599 (30%) → 0.579 (40%); GLM-4.6-FP8 (thinking) 0.784 → 0.773 → 0.768 → 0.742; GLM-4.5-Air (thinking) 0.768 → 0.763 (25%); Qwen3-Coder-30B 0.632 → 0.622 (20%).
  - Table A10, τ²-telecom for MiniMax-M2: 0.591 → 0.553 at 40%.
- **REAP model cards** report only HumanEval/MBPP for the newer checkpoints (MiniMax-M2.1/2.5, Step-3.5, GLM-4.7-Flash), not agentic or SWE scores.
- **ISTA-DASLab GSQ-RCO-Coder** (vendor): Qwen3.8-Flash-Next SWE-V 82.80 (BF16) → 79.20 (50% experts pruned with RCO + GSQ at 3.5 bpw), LCB v6 unchanged. The card itself notes multi-step agentic tasks "tolerate capacity reduction less well". Harness unstated.
- **Community/independent:**
  - `DJLougen/reap-eval` (github) reports that across 22 checkpoint-level attempts on 0xSero REAP releases there were "0 clear wins, ~23% marginal, ~60% failure at 50% prune". It also gives a rule of thumb that each ~1 bpw below 3 bpw costs about 10–12% score. The aggregates are not tied to named models.
  - Community REAP cards (AnonimousA, sh0wie, patrickbdevaney) report only HumanEval, KL or fabrication probes, no agentic tests. AnonimousA's card measured that pruning raises general-knowledge fabrication (25% vs 0% for the unpruned IQ1_M).
- **Low-bit quantisation and agentic work:** no controlled Q2/Q3 SWE-bench study was found. The one small experiment (edmcman blog 2026-05-15) used Qwen3.5-2B, a single SWE-V instance, OpenHands, and 8 attempts per quant. IQ2 builds went 0/8 with loops and timeouts, and results were non-monotonic at Q3–Q8. Anecdotal only.
- **Practical rule** (inference, not measured): prefer pruning to 25–40% at Q4 over the unpruned model at Q2. Avoid 50% REAP for multi-turn tool use unless a model-specific agentic eval exists. Validate any candidate with a Pi tool-call smoke test (unknown-tool rate, malformed-JSON rate, loop rate).

### B1. `cerebras/Qwen3-Coder-REAP-25B-A3B` (2025-10-20)
- From Qwen3-Coder-30B-A3B-Instruct, 20% experts pruned (128→103). Apache-2.0. BF16 49.74 GB; 24.87B params.
- config: `qwen3_moe`, `num_hidden_layers 48`, `num_attention_heads 32`, `num_key_value_heads 4`, `head_dim 128`, `num_experts 103`, `num_experts_per_tok 8`, `max_position_embeddings 262144` → KV FP8 **48 KiB/token**. Non-thinking.
- Serving `--tool-call-parser qwen3_coder`.
- GGUF (`bartowski/cerebras_Qwen3-Coder-REAP-25B-A3B-GGUF`): Q8_0 26.46, Q6_K 20.45, **Q4_K_M 15.19**, Q3_K_M 11.51 GB.
- Card (vendor): BFCL-v3 overall 63.2 → 62.2, multi-turn 29.6 → 30.5. No SWE result.
- Only useful if 16 GB VRAM forces it; Qwen3-Coder-30B-A3B at Q4 is ~18 GB and is already covered.

### B2. `cerebras/GLM-4.7-Flash-REAP-23B-A3B` (2026-01-23)
- From GLM-4.7-Flash, 25% pruned (64→48 routed experts). MIT. BF16 45.99 GB.
- config: `glm4_moe_lite`, `num_hidden_layers 47`, `num_attention_heads 20`, `num_key_value_heads 20`, `kv_lora_rank 512`, `q_lora_rank 768`, `qk_rope_head_dim 64`, `qk_nope_head_dim 192`, `v_head_dim 256`, `n_routed_experts 48`, `num_experts_per_tok 4`, `n_shared_experts 1`, `max_position_embeddings 202752`. MLA → (512 + 64) × 47 = **26.4 KiB/token** FP8.
- Serving: vLLM `--reasoning-parser glm45 --tool-call-parser glm47`.
- GGUF (`unsloth/GLM-4.7-Flash-REAP-23B-A3B-GGUF`): Q8_0 24.46, UD-Q4_K_XL 14.24, Q4_K_M 14.11 GB.
- Card: HumanEval 94.5 → 95.1 only.

### B3. `cerebras/GLM-4.5-Air-REAP-82B-A12B` (2025-10-20)
- From GLM-4.5-Air (106B), 25% pruned (128→96). MIT. BF16 169.99 GB; FP8 repo `cerebras/GLM-4.5-Air-REAP-82B-A12B-FP8`.
- config: `glm4_moe`, `num_hidden_layers 46`, `num_attention_heads 96`, `num_key_value_heads 8`, `head_dim 128`, `n_routed_experts 96`, `num_experts_per_tok 8`, `max_position_embeddings 131072` → KV FP8 **92 KiB/token**. Hybrid thinking (GLM-4.5 template; `enable_thinking`).
- Serving `--tool-call-parser glm45` (+ `--reasoning-parser glm45`).
- GGUF (`bartowski/cerebras_GLM-4.5-Air-REAP-82B-A12B-GGUF`): Q4_K_M 56.57, Q4_K_S 52.65, IQ4_XS 46.99, Q3_K_M 42.88, IQ3_XXS 38.98 GB.
- Card (vendor): BFCL-v3 73.4 → 71.8; BFCL (thinking) 76.8 → 76.3; τ²-airline 63.3 → 64.0, retail 72.8 → 75.1.
- Reference for the *unpruned* base: SWE-V 50.5 (per SERA card table, harness UNVERIFIED). Compare with unpruned GLM-4.5-Air: `unsloth/GLM-4.5-Air-GGUF` Q3_K_M 57.20, UD-Q3_K_XL 54.79, Q3_K_S 52.66, UD-Q2_K_XL 47.44 GB.
- Superseded quality-wise by the Qwen3.6-35B-A3B fine-tunes, so not shortlisted.

### B4. `cerebras/MiniMax-M2.5-REAP-139B-A10B` (2026-02-18); also M2.1 / M2 versions
- From MiniMax-M2.5 (230B), 40% pruned (256→154 experts). Licence "other" (MiniMax). The cerebras repo is FP8 (140.43 GB).
- config: `minimax_m2`, `num_hidden_layers 62`, `num_attention_heads 48`, `num_key_value_heads 8`, `head_dim 128`, `num_local_experts 154`, `num_experts_per_tok 8`, `max_position_embeddings 196608` → KV FP8 **124 KiB/token** (all full attention).
- Always thinks (interleaved `<think>`). Serving: vLLM `--tool-call-parser minimax_m2 --reasoning-parser minimax_m2_append_think`.
- GGUF (`mradermacher/MiniMax-M2.5-REAP-139B-A10B-i1-GGUF`): IQ3_M 61.00, IQ3_S 60.17, Q3_K_S 60.15, IQ3_XS 56.91, **IQ3_XXS 53.61**, **Q2_K 50.82**, IQ2_M 45.72, Q4_K_M 84.19 GB.
- For comparison, the unpruned `unsloth/MiniMax-M2.5-GGUF` UD-TQ1_0 is 55.77, UD-IQ1_M 68.44 and UD-Q2_K_XL 85.92 GB. Within 64 GB, **pruned IQ3 beats unpruned ~1.6-bit** on the bpw rule of thumb (inference).
- Base benchmark (independent): MiniMax M2.5 (high) **75.8** SWE-V bash-only, mini-swe-agent 2.0.0 (SWE-bench leaderboard, 2026-02-17). MiniMax M2 scored 61.0 (mini 1.17.0, checked).
- Pruned agentic numbers: **none.** Closest proxy is MiniMax-M2 under REAP 40%: BFCLv3 0.626 → 0.579, τ²-telecom 0.591 → 0.553 (REAP paper A10/A11).
- Community `mradermacher/m51Lab-MiniMax-M2.7-REAP-139B-A10B-i1-GGUF` (M2.7 base) has identical sizes (IQ3_XXS 53.61, Q2_K 50.82). Its pruning provenance is `dervig/m51Lab-…` and is **UNVERIFIED**.
- At 64 GB: IQ3_XXS 53.6 GB + 32K ctx × 124 KiB ≈ 3.9 GiB FP8 KV. Workable only for a 32–48K window.

### B5. `ISTA-DASLab/Qwen3.8-Flash-Next-GSQ-RCO-Coder-GGUF` (2026-09-26, updated 2026-10-17)
- From Qwen/Qwen3.8-Flash-Next (base: "125B with 6B activated, plus 51B n-gram embedding and 4B MTP", licence `qwen-community-1.0` per base card; the base itself is covered by the base-model researchers). 50% of routed experts removed (512→256 per layer) using RCO, with a code + agentic + vision calibration mix, then GSQ quantisation at 3.5 bpw. The card's YAML says apache-2.0, but its text says the weights "inherit the license of the base model", so the licence is **qwen-community-1.0 (check)**.
- Base config.json (`text_config`, verbatim): `model_type qwen4_exp`, `num_hidden_layers 48`, `layer_types {linear_attention: 36, full_attention: 12}`, `num_attention_heads 24`, `num_key_value_heads 2`, `head_dim 256`, `hidden_size 2560`, `linear_num_key_heads 16`, `linear_num_value_heads 48`, `linear_key/value_head_dim 128`, `num_experts 512`, `num_experts_per_tok 10`, `moe_intermediate_size 640`, `max_position_embeddings 262144`. The full layers use QSA sparse attention per the base card.
  → KV FP8 ≤ 12 × 2 × 256 × 2 = **12 KiB/token** (upper bound if QSA stores full KV; UNVERIFIED how llama.cpp stores it).
- Files: IQ1_M two shards, 29.6 GB transformer (must be resident) + 28.8 GB n-gram table (IQ4_NL; "may be served from disk"). Total 58.4 GB. **llama.cpp only**; the minimum llama.cpp build for the `qwen4_exp` arch is UNVERIFIED.
- Thinking: base defaults to thinking on, preserved thinking, and `reasoning_effort` xhigh/medium/low. Off via `chat_template_kwargs: {"enable_thinking": false}` (base card). All ISTA numbers were measured at xhigh.
- Benchmarks (vendor ISTA): SWE-V 82.80 (BF16) → **79.20**; LCB v6 87.43 → 87.43. Harness not stated (UNVERIFIED). Card says "experimental release".
- Other low-bit or pruned Flash-Next builds in range:
  - `ISTA-DASLab/Qwen3.8-Flash-Next-GSQ-RCO-GGUF` (unpruned): Q2_0 66.4, IQ2_XS 68.0 GB. Over 64 GB unless the n-gram shard is offloaded; check the card.
  - `AnonimousA/Qwen3.8-Flash-Next-REAP-320-GGUF`: UD-Q2_K_XL 61.5 GB, HumanEval only.
  - `sh0wie/…-REAP-288-GGUF`: Q4_K_M 83.8 GB, too big.
  - `unsloth/Qwen3.8-Flash-Next-GGUF` (unpruned): UD-Q2_K_XL 78.87 GB, too big.

### B6. Other big-MoE compressions seen (none fit well, or no agentic evidence)
- `cerebras/Step-3.5-Flash-REAP-121B-A11B` (196B → 121B, 40%). config: 45 layers, `layer_types {full_attention: 12, sliding_attention: 36}`, `sliding_window 512`, 64 heads, head_dim 128. **num_key_value_heads absent from config, so UNVERIFIED.** GGUF (mradermacher i1): IQ3_XXS 46.60, Q3_K_S 52.30, IQ3_M 52.86, Q3_K_M 57.72 GB. Card: HumanEval only. Parsers `step3p5`.
- `cerebras/MiniMax-M2.1-REAP-139B-A10B`: same shape as B4, M2.1 base.
- `patrickbdevaney/GLM-5.3-Flash-REAP50-GGUF` (301B → 157B, 12.5B active): smallest file IQ3_M 72.1 GB, **too big**. Card reports KL / top-1 agreement (83.39%) only.
- `patrickbdevaney/MiMo-V2.6-Flash-REAP50-GGUF` (315B → 164B): Q2_K 66.19 GB, too big. The card says llama.cpp `mimo2` support was pending.
- `puwaer/DeepSeek-V4-Flash-0731-reap-150b-gguf`: Q2_K 62.39, IQ3_XXS 67.0 GB. Card evals cover GSM8K, MATH, HumanEval+ and MBPP+ only.
- Community `lovedheart/Qwen3-Coder-Next-REAP-40B/48B-A3B` (from Qwen3-Coder-Next 80B; `mradermacher/Qwen3-Coder-Next-REAP-40B-A3B-i1-GGUF` Q4_K_M 24.98 GB): no agentic evals. Unpruned Qwen3-Coder-Next already fits 48 GB at Q4 (see `qwen_models.md`).
- `cerebras/Kimi-Linear-REAP-35B-A3B-Instruct`: not a coding model (BF16 70.27 GB).
- `nvidia/NVIDIA-Nemotron-3.5-Bolt-74B-A9B-24GB` (2026-10-07): NVIDIA's own compression of Bolt-74B-A9B to 24.08 GB (Q8_0/ggml-packed mixed). This is a base-model family and belongs to the other researchers; pointer only.
- **`openai/gpt-oss-120b`**: MXFP4 checkpoint 65.25 GB (top-level safetensors); `unsloth/gpt-oss-120b-GGUF` F16 65.37 / Q4_K_M 62.77 GB (all quants ≈ 62.6–65 GB because the experts stay MXFP4).
  - config: `num_hidden_layers 36`, `layer_types {sliding_attention: 18, full_attention: 18}`, `sliding_window 128`, `num_key_value_heads 8`, `head_dim 64`, 128 experts, top-4, `max_position_embeddings 131072` → KV FP8 = 18 × 8 × 64 × 2 = **18 KiB/token** on full layers.
  - Independent bash-only score **26.0** (mini-swe-agent 1.7.0, Aug 2025).
  - Does not fit 64 GB with KV and runtime overhead. Not recommended.

---

## UNVERIFIED / open items
- Exact base checkpoint for Ornith-1.5 (no `base_model` tag). Independent numbers for Ornith-1.5, KAT-Coder-V2.5-Dev, tmax-27b and Logics-SWE: none found (BenchLM marks Ornith as lacking independent coverage).
- CWM config values (gated repo) and its exact tool set (secondary sources say bash/edit/create/submit).
- Harnesses for the Logics-SWE, KAT-Dev-32B (62.4), GSQ-RCO-Coder (79.2) and KAT REAP-50 (52.0) numbers.
- llama.cpp support and minimum build for `qwen4_exp` (Qwen3.8-Flash-Next) and for the n-gram shard offload. How QSA layers store KV.
- `TMaxxx/tmax-27b-v1.1` and `dervig/m51Lab-MiniMax-M2.7-REAP` provenance.
- Licence of the GSQ-RCO-Coder GGUF (YAML says apache-2.0; text says it inherits qwen-community-1.0).

## Sources (primary)
- HF model cards/configs/listings: huggingface.co/{Kwaipilot/KAT-Coder-V2.5-Dev, bartowski/Kwaipilot_KAT-Coder-V2.5-Dev-GGUF, cyankiwi/KAT-Coder-V2.5-Dev-AWQ-INT4, Ttimms/KAT-Coder-V2.5-Dev-REAP-50-GGUF, ornith-ai/Ornith-1.5-35B-A3B, ornith-ai/Ornith-1.5-35B-A3B-GGUF, ornith-ai/Ornith-1.0-35B, allenai/tmax-27b, Logics-MLLM/Logics-SWE-Qwen3.6-27B, Logics-MLLM/Logics-SWE-Qwen3.6-35B-A3B, allenai/SERA-32B, agentica-org/DeepSWE-Preview, SWE-bench/SWE-agent-LM-32B, Skywork/Skywork-SWE-32B, OpenHands/openhands-lm-32b-v0.1, zai-org/SWE-Dev-32B, Kwaipilot/KAT-Dev, Kwaipilot/KAT-Dev-72B-Exp, moonshotai/Kimi-Dev-72B, facebook/cwm, nebius/SWE-rebench-openhands-Qwen3-30B-A3B, open-thoughts/OpenThinkerAgent-32B, cerebras/* REAP repos, mradermacher/MiniMax-M2.5-REAP-139B-A10B-i1-GGUF, unsloth/MiniMax-M2.5-GGUF, unsloth/GLM-4.5-Air-GGUF, bartowski/cerebras_GLM-4.5-Air-REAP-82B-A12B-GGUF, ISTA-DASLab/Qwen3.8-Flash-Next-GSQ-RCO-Coder-GGUF, Qwen/Qwen3.8-Flash-Next, openai/gpt-oss-120b, unsloth/gpt-oss-120b-GGUF}
- REAP paper: https://arxiv.org/html/2510.13999 (Table 3, A10, A11; Appendix C eval setup)
- Tmax paper: https://arxiv.org/abs/2606.23321 (Tables 3, 5)
- Kimi-Dev paper: https://ar5iv.labs.arxiv.org/html/2509.23045
- SWE-bench leaderboard data: https://raw.githubusercontent.com/SWE-bench/swe-bench.github.io/master/data/leaderboards.json
- SERA repo: https://github.com/allenai/SERA
- reap-eval: https://github.com/DJLougen/reap-eval
- Quant anecdote: https://edmcman.github.io/blog/2026-05-15--commonsense-benchmarks-for-local-coding-agents/
- BenchLM Ornith page: https://www.benchlm.ai/models/ornith-1-5-35b-a3b
