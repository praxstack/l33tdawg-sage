# SAGE v7.1 reranker sidecar

A tiny FastAPI server that wraps `sentence_transformers.CrossEncoder` to expose the same `POST /rerank` contract HuggingFace's Text Embeddings Inference (TEI) does. SAGE's hybrid-recall reranker client (`internal/embedding/reranker.go`) talks to either equivalently.

The sidecar exists because TEI ships amd64-only CPU images, and running them under Rosetta translation on Apple Silicon tends to OOM during the `bge-reranker-v2-m3` warmup phase. This sidecar runs natively on arm64 with MPS (Apple GPU) acceleration when available, so the v7.1 bench is reproducible on Mac without Docker emulation in the loop.

## When to use which

| Path | When |
|---|---|
| TEI Docker image | Linux x86_64 host with `docker pull ghcr.io/huggingface/text-embeddings-inference:cpu-latest --platform linux/amd64` working. Operator preference. |
| **This sidecar** | Apple Silicon (M-series), or any host where TEI's binary cold-start is unreliable, or you want native MPS/CUDA acceleration without a container around the inference loop. |

Either way the SAGE node points at it via `SAGE_RERANK_URL` and signs requests through the same Go client.

## Run

```bash
cd bench/rerank-server
pip install -r requirements.txt
python server.py
```

Defaults: model `BAAI/bge-reranker-v2-m3`, listens on `0.0.0.0:18090`, auto-picks the best torch device (`cuda` > `mps` > `cpu`).

First boot downloads the model (~2.3 GB) to `~/.cache/huggingface/`. Subsequent boots load from disk.

## Env vars

| Var | Default | Purpose |
|---|---|---|
| `RERANK_MODEL` | `BAAI/bge-reranker-v2-m3` | Any HF cross-encoder id. |
| `RERANK_DEVICE` | `auto` | `cuda`, `mps`, `cpu`, or `auto` to pick the best available. |
| `RERANK_HOST` | `0.0.0.0` | Bind address. |
| `RERANK_PORT` | `18090` | Port. |

## Point SAGE at it

When SAGE is running on the host:

```bash
export SAGE_RERANK_ENABLED=1
export SAGE_RERANK_URL=http://localhost:18090
sage-gui serve
```

When SAGE is in a Docker container (e.g. `sage:v7.1-bench` during a bench run), use `host.docker.internal` so the container reaches the sidecar process on the host:

```bash
docker run -d --name sage-bench --rm -p 18080:8080 \
  -v sage_v71_data:/root/.sage \
  -e REST_ADDR=0.0.0.0:8080 \
  -e SAGE_RERANK_ENABLED=1 \
  -e SAGE_RERANK_URL=http://host.docker.internal:18090 \
  -e SAGE_RERANK_MODEL=BAAI/bge-reranker-v2-m3 \
  sage:v7.1-bench
```

## Smoke test

```bash
curl -s http://localhost:18090/info
curl -s -X POST http://localhost:18090/rerank \
  -H 'Content-Type: application/json' \
  -d '{"query":"how does jwt work","texts":["JWT signs claims with HMAC.","Cats are mammals."]}' \
  | python3 -m json.tool
```

The JWT line should outscore the cat line by a wide margin.

## What it does not do

- Authentication. The sidecar listens on plain HTTP and trusts whoever can reach the port. Run it on localhost or inside a trusted network. SAGE's reranker client is the only intended caller.
- Multi-model serving. One process, one model. Spin up another instance on a different port for a second model.
- Batched cross-request inference. Each `/rerank` call is its own forward pass through the cross-encoder; that matches TEI's behaviour and keeps the model-loaded cost amortised across requests.
