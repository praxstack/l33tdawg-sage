# LongMemEval-S benchmark for SAGE

LongMemEval (Wu et al., ICLR 2025) is a standard retrieval-quality benchmark for agent memory systems. This harness measures SAGE v7.0's hybrid recall (`POST /v1/memory/hybrid`) on the cleaned LongMemEval-S subset and reports R@5, R@10, and MRR overall and by question type.

## What it does

For each of the dataset's 500 questions:

1. Seed every haystack session into the running SAGE node as a committed memory in its own isolated domain (`bench-lme-<question_id>`).
2. Embed the probe question via OpenAI `text-embedding-3-small`.
3. Call `/v1/memory/hybrid` with the question text + embedding.
4. Score returned memories against the ground-truth `answer_session_ids`.

Per-question rows and aggregate summary land in `bench/results/longmemeval-<git_sha>.json`.

## Requirements

- A running SAGE node reachable at `SAGE_API_URL` (default `http://localhost:8080`). Personal mode is fine — every write goes through the same BFT pipeline as production deployments.
- `OPENAI_API_KEY` exported.
- Python 3.10+.
- Optional: a local copy of the dataset to avoid HuggingFace download time (`LONGMEMEVAL_DATA_PATH=/path/to/longmemeval_s.json`).

## Install

```bash
pip install -r bench/longmemeval/requirements.txt
```

## Run

```bash
# Smoke test on the first 5 questions (good for sanity-checking the wiring)
python bench/longmemeval/run.py --limit 5

# Focused run on one reasoning category
python bench/longmemeval/run.py --question-type temporal-reasoning --limit 20

# Full benchmark (slow — every write goes through consensus; budget hours)
python bench/longmemeval/run.py

# Or via make
make bench-longmemeval-smoke   # --limit 5
make bench-longmemeval         # full
```

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `SAGE_API_URL` | `http://localhost:8080` | SAGE REST endpoint |
| `OPENAI_API_KEY` | *(required)* | OpenAI auth for embeddings |
| `LONGMEMEVAL_EMBED_MODEL` | `text-embedding-3-small` | Embedding model |
| `LONGMEMEVAL_DATA_PATH` | *(unset)* | Local JSON file to avoid HF download |

## What we measure, what we don't

This benchmark measures **pure retrieval quality** — given a question and a haystack, can SAGE pull the right session(s) into the top-K. It does not measure:

- Cross-session reasoning quality (we score retrieval, not answer correctness).
- Confidence decay or governance behaviour over time.
- Multi-agent dynamics, federation, or BFT failure modes.

Those are SAGE's actual value-add and need their own evaluations. This bench is the comparability axis — the number you can put next to other memory systems' numbers on the same dataset.

## Reading the results

Each results JSON has:

```json
{
  "git_sha": "...",
  "sage_url": "http://localhost:8080",
  "embed_model": "text-embedding-3-small",
  "summary": {
    "overall": { "n": 500, "r5": 0.xxxx, "r10": 0.xxxx, "mrr": 0.xxxx },
    "per_type": {
      "single-session-user":     { "n": ..., "r5": ..., ... },
      "single-session-assistant": { "n": ..., "r5": ..., ... },
      "multi-session":           { "n": ..., "r5": ..., ... },
      "temporal-reasoning":      { "n": ..., "r5": ..., ... },
      "knowledge-update":        { "n": ..., "r5": ..., ... }
    }
  },
  "per_question": [ ... ]
}
```

Compare to previous runs by diffing `summary.overall` between two JSONs in `bench/results/`.

## Tuning knobs that affect the score

SAGE v7.0's hybrid recall exposes four env tunables — all read by the SAGE node, not by this script. Set them in the SAGE node's environment, then re-run:

| Var | Default | Effect |
|---|---|---|
| `SAGE_HYBRID_RRF_K` | 60 | RRF smoothing constant |
| `SAGE_HYBRID_BM25_WEIGHT` | 0.4 | weight on BM25 stream |
| `SAGE_HYBRID_VECTOR_WEIGHT` | 0.6 | weight on vector stream |
| `SAGE_HYBRID_OVERSAMPLE` | 2 | each stream samples `TopK * N` before merging |

Run a baseline first, then bisect.
