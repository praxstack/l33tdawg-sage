#!/usr/bin/env python3
"""Lightweight cross-encoder reranker sidecar for SAGE v7.1 hybrid recall.

Implements the same `POST /rerank` contract HuggingFace Text Embeddings
Inference (TEI) exposes, so SAGE's `internal/embedding/reranker.go` HTTP
client speaks to it without any adapter. The point of this file is to
provide a path that runs natively on Apple Silicon - TEI's CPU image is
amd64-only and Rosetta translation of the bge-reranker-v2-m3 warmup tends
to OOM under Docker Desktop's default memory cap.

Default model: BAAI/bge-reranker-v2-m3 (568M params, multilingual, 2024).
Override with RERANK_MODEL env var. Device is auto-selected:
  cuda -> mps (Apple GPU) -> cpu, in that order of preference.

Request shape (matches TEI):
  POST /rerank
  {"query": "...", "texts": ["doc1", "doc2", ...], "raw_scores": false}

Response (score-descending):
  [{"index": 0, "score": 0.92}, {"index": 1, "score": 0.45}, ...]
"""

from __future__ import annotations

import os
import sys
from typing import Any

try:
    from fastapi import FastAPI
    from pydantic import BaseModel
except ImportError:
    sys.exit("missing deps: pip install -r requirements.txt")

try:
    import torch
    from sentence_transformers import CrossEncoder
except ImportError:
    sys.exit("missing ML deps: pip install -r requirements.txt")


MODEL_ID = os.environ.get("RERANK_MODEL", "BAAI/bge-reranker-v2-m3")
DEVICE_ENV = os.environ.get("RERANK_DEVICE", "auto").lower()


def pick_device() -> str:
    """Return the best torch device available, with explicit override."""
    if DEVICE_ENV not in ("auto", ""):
        return DEVICE_ENV
    if torch.cuda.is_available():
        return "cuda"
    if hasattr(torch.backends, "mps") and torch.backends.mps.is_available():
        return "mps"
    return "cpu"


DEVICE = pick_device()
print(f"reranker sidecar: loading {MODEL_ID} on {DEVICE}...", flush=True)
model = CrossEncoder(MODEL_ID, device=DEVICE)
print("reranker sidecar: model loaded, ready for /rerank", flush=True)


app = FastAPI(title="SAGE v7.1 reranker sidecar", version="1.0")


class RerankRequest(BaseModel):
    query: str
    texts: list[str]
    # Matches TEI's optional field; we ignore raw_scores=true since CrossEncoder
    # already returns probability-like floats. Keeps the wire shape compatible.
    raw_scores: bool | None = False


class RerankResult(BaseModel):
    index: int
    score: float


@app.get("/info")
def info() -> dict[str, Any]:
    """TEI-style metadata endpoint. SAGE's reranker client doesn't strictly
    need this, but it's handy for `curl <host>/info` health checks during
    bench setup."""
    return {
        "model_id": MODEL_ID,
        "device": DEVICE,
        "type": "reranker",
        "backend": "sentence-transformers/CrossEncoder",
    }


@app.post("/rerank", response_model=list[RerankResult])
def rerank(req: RerankRequest) -> list[dict[str, Any]]:
    """Score (query, text) pairs and return them sorted score-descending.

    Matches TEI's response contract exactly so SAGE's HTTPReranker doesn't
    need to know which backend it's talking to.

    MPS memory hygiene: wrap inference in torch.no_grad() so we don't
    track gradients (forward-only) and explicitly drain the MPS cache
    after each call. PyTorch's MPS backend tends to retain allocations
    across calls otherwise, and a benchmark loop of thousands of /rerank
    calls hits the high-watermark limit and OOM-crashes the process.
    """
    if not req.texts:
        return []
    # Cap each text at 2048 chars before tokenization. CrossEncoder
    # truncates to max_length=512 tokens anyway; this cap bounds the
    # tokenizer work and keeps per-call allocations stable across very
    # long haystack sessions.
    truncated = [t[:2048] for t in req.texts]
    pairs = [(req.query, t) for t in truncated]
    with torch.no_grad():
        scores = model.predict(pairs, show_progress_bar=False)
    # Drain the MPS cache so per-call allocations don't accumulate across
    # the bench's ~10k+ rerank calls.
    if DEVICE == "mps" and hasattr(torch, "mps") and hasattr(torch.mps, "empty_cache"):
        torch.mps.empty_cache()
    indexed = list(enumerate(float(s) for s in scores))
    indexed.sort(key=lambda x: x[1], reverse=True)
    return [{"index": i, "score": s} for i, s in indexed]


if __name__ == "__main__":
    # When invoked directly we spin up uvicorn here so the file is a
    # self-contained "python server.py" experience for the bench harness.
    try:
        import uvicorn
    except ImportError:
        sys.exit("missing uvicorn: pip install -r requirements.txt")
    host = os.environ.get("RERANK_HOST", "0.0.0.0")
    port = int(os.environ.get("RERANK_PORT", "18090"))
    uvicorn.run(app, host=host, port=port, log_level="info")
