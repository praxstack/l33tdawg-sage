#!/usr/bin/env python3
"""LongMemEval-S harness with LLM-answer + LLM-judge on top of retrieval.

Standard `run.py` scores SAGE's retrieval directly (R@5, R@10, MRR on
session-id match). This script runs the same retrieval but ALSO:
  1. Generates a short answer from the top-K retrieved sessions using
     mem0's published answer prompt (verbatim, for fair comparison).
  2. LLM-judges that answer against gold using mem0's ACCURACY_PROMPT
     (verbatim).

Output JSON shape preserves the retrieval metrics AND adds:
  - llm_answer: the generated short answer
  - llm_judge_label: CORRECT|WRONG
  - llm_score: 1 if CORRECT else 0

That makes the LongMemEval result directly comparable to mem0/Letta's
published LLM-judged numbers on their primary metric, without changing
any of the retrieval-side comparisons our existing v7.1 number rests on.

Env:
  SAGE_API_URL                 default http://localhost:18080
  OPENAI_API_KEY               required
  LONGMEMEVAL_DATA_PATH        optional local json; HF fallback otherwise
  LONGMEMEVAL_ANSWER_MODEL     default gpt-4o-mini
  LONGMEMEVAL_JUDGE_MODEL      default gpt-4o-mini
  LONGMEMEVAL_EMBED_MODEL      default text-embedding-3-small
"""

from __future__ import annotations

import argparse
import collections
import hashlib
import json
import os
import statistics
import struct
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

import httpx

try:
    from nacl.signing import SigningKey
except ImportError:
    sys.exit("missing dep: pip install pynacl")

try:
    from openai import OpenAI
except ImportError:
    sys.exit("missing dep: pip install openai")


BASE_URL = os.environ.get("SAGE_API_URL", "http://localhost:18080")
EMBED_MODEL = os.environ.get("LONGMEMEVAL_EMBED_MODEL", "text-embedding-3-small")
ANSWER_MODEL = os.environ.get("LONGMEMEVAL_ANSWER_MODEL", "gpt-4o-mini")
JUDGE_MODEL = os.environ.get("LONGMEMEVAL_JUDGE_MODEL", "gpt-4o-mini")

SESSION_ID_PREFIX = "[longmemeval-sid:"
SESSION_ID_SUFFIX = "]\n"
CONTENT_MAX_BYTES = 50_000
EMBED_INPUT_MAX_CHARS = 8000


# Verbatim from mem0's evaluation/src/rag.py (commit at time of writing).
# Identical prompt = the LLM gets the same context-to-answer instruction
# every other backend in their suite receives, so the LLM-judge measures
# memory-layer differences rather than prompt-engineering differences.
ANSWER_PROMPT = """
# Question:
{question}

# Context:
{context}

# Short answer:
"""

ANSWER_SYSTEM = (
    "You are a helpful assistant that can answer questions based on the "
    "provided context. If the question involves timing, use the conversation "
    "date for reference. Provide the shortest possible answer. Use words "
    "directly from the conversation when possible. Avoid using subjects in "
    "your answer."
)

# Verbatim from mem0's evaluation/metrics/llm_judge.py - same binary CORRECT/
# WRONG judge prompt their published numbers come from.
ACCURACY_PROMPT = """
Your task is to label an answer to a question as 'CORRECT' or 'WRONG'. You will be given the following data:
    (1) a question (posed by one user to another user),
    (2) a 'gold' (ground truth) answer,
    (3) a generated answer
which you will score as CORRECT/WRONG.

The point of the question is to ask about something one user should know about the other user based on their prior conversations.
The gold answer will usually be a concise and short answer that includes the referenced topic, for example:
Question: Do you remember what I got the last time I went to Hawaii?
Gold answer: A shell necklace
The generated answer might be much longer, but you should be generous with your grading - as long as it touches on the same topic as the gold answer, it should be counted as CORRECT.

For time related questions, the gold answer will be a specific date, month, year, etc. The generated answer might be much longer or use relative time references (like "last Tuesday" or "next month"), but you should be generous with your grading - as long as it refers to the same date or time period as the gold answer, it should be counted as CORRECT. Even if the format differs (e.g., "May 7th" vs "7 May"), consider it CORRECT if it's the same date.

Now it's time for the real question:
Question: {question}
Gold answer: {gold_answer}
Generated answer: {generated_answer}

First, provide a short (one sentence) explanation of your reasoning, then finish with CORRECT or WRONG.
Do NOT include both CORRECT and WRONG in your response, or it will break the evaluation script.

Just return the label CORRECT or WRONG in a json format with the key as "label".
"""


class SageAgent:
    def __init__(self) -> None:
        self.signing_key = SigningKey.generate()
        self.agent_id = self.signing_key.verify_key.encode().hex()

    def _signature(self, method: str, path: str, body: bytes, ts: int) -> str:
        canonical = f"{method} {path}\n".encode() + body
        body_hash = hashlib.sha256(canonical).digest()
        ts_bytes = struct.pack(">q", ts)
        return self.signing_key.sign(body_hash + ts_bytes).signature.hex()

    def headers(self, method: str, path: str, body: bytes) -> dict[str, str]:
        ts = int(time.time())
        return {
            "Content-Type": "application/json",
            "X-Agent-ID": self.agent_id,
            "X-Signature": self._signature(method, path, body, ts),
            "X-Timestamp": str(ts),
        }


def embed(openai: OpenAI, text: str) -> list[float]:
    text = (text or "")[:EMBED_INPUT_MAX_CHARS]
    if not text.strip():
        text = "."
    r = openai.embeddings.create(model=EMBED_MODEL, input=text)
    return r.data[0].embedding


def session_to_text(session: list[dict[str, Any]], session_date: str = "") -> str:
    """Render one haystack session as flat content lines, no role labels.

    LongMemEval sessions are user/assistant chat transcripts. When seeded with
    `role:` prefixes (even relabeled `user-a:` / `user-b:`) the answer LLM
    pattern-matches against the chat-completion format and generates a
    CONTINUATION of the retrieved chat instead of extracting an answer to the
    bench question. Stripping role labels entirely forces the LLM to treat
    the retrieved content as a third-party transcript to summarise from.

    Session date is preserved as a leading metadata line so temporal queries
    can resolve relative references.
    """
    lines: list[str] = []
    if session_date:
        lines.append(f"[session date: {session_date}]")
    for turn in session:
        content = (turn.get("content") or "").strip()
        if content:
            lines.append(content)
    return "\n".join(lines)


def submit_session(sage: httpx.Client, agent: SageAgent, openai: OpenAI,
                   domain: str, session_id: str, session_text: str) -> bool:
    body_text = f"{SESSION_ID_PREFIX}{session_id}{SESSION_ID_SUFFIX}{session_text}"[:CONTENT_MAX_BYTES]
    body = {
        "content": body_text,
        "memory_type": "observation",
        "domain_tag": domain,
        "confidence_score": 0.85,
        "embedding": embed(openai, session_text),
    }
    body_bytes = json.dumps(body).encode()
    try:
        r = sage.post("/v1/memory/submit",
                      headers=agent.headers("POST", "/v1/memory/submit", body_bytes),
                      content=body_bytes, timeout=60.0)
        r.raise_for_status()
        return True
    except httpx.HTTPError as exc:
        print(f"  ! submit failed sid={session_id[:8]}: {exc}", file=sys.stderr)
        return False


def hybrid_recall(sage: httpx.Client, agent: SageAgent, openai: OpenAI,
                  domain: str, question: str, top_k: int,
                  n_expansions: int = 0) -> list[dict[str, Any]]:
    body: dict[str, Any] = {
        "query": question,
        "embedding": embed(openai, question),
        "domain_tag": domain,
        "top_k": top_k,
        "status_filter": "committed",
    }
    if n_expansions > 0:
        variants = generate_expansions(openai, question, n_expansions)
        if variants:
            body["expansions"] = [
                {"query": v, "embedding": embed(openai, v)} for v in variants
            ]
    body_bytes = json.dumps(body).encode()
    r = sage.post("/v1/memory/hybrid",
                  headers=agent.headers("POST", "/v1/memory/hybrid", body_bytes),
                  content=body_bytes, timeout=60.0)
    r.raise_for_status()
    return r.json().get("results", []) or []


def generate_expansions(openai: OpenAI, question: str, n: int) -> list[str]:
    if n <= 0 or not question.strip():
        return []
    prompt = (
        f"Generate {n} short paraphrase/entity/temporal variants of the question below. "
        "Output ONLY the variants, one per line, no numbering or commentary. "
        "Vary phrasing, surface named entities explicitly, and concretise relative "
        "time references when context permits."
        f"\n\nQuestion: {question}"
    )
    try:
        resp = openai.chat.completions.create(
            model=os.environ.get("LONGMEMEVAL_EXPANSION_MODEL", "gpt-4o-mini"),
            messages=[{"role": "user", "content": prompt}],
            temperature=0.4,
            max_tokens=200,
        )
        text = (resp.choices[0].message.content or "").strip()
    except Exception as exc:
        print(f"  ! expansion failed: {exc}", file=sys.stderr)
        return []
    out: list[str] = []
    for raw in text.splitlines():
        line = raw.strip().lstrip("-*0123456789.) ").strip()
        if line and line != question:
            out.append(line)
    return out[:n]


def extract_session_id(content: str) -> str | None:
    if not content.startswith(SESSION_ID_PREFIX):
        return None
    end = content.find(SESSION_ID_SUFFIX, len(SESSION_ID_PREFIX))
    if end == -1:
        return None
    return content[len(SESSION_ID_PREFIX):end]


def strip_session_prefix(content: str) -> str:
    if not content.startswith(SESSION_ID_PREFIX):
        return content
    end = content.find(SESSION_ID_SUFFIX, len(SESSION_ID_PREFIX))
    if end == -1:
        return content
    return content[end + len(SESSION_ID_SUFFIX):]


def score(returned_sids: list[str], answer_sids: set[str]) -> dict[str, float]:
    if not answer_sids:
        return {"r5": 0.0, "r10": 0.0, "rr": 0.0}
    top_5 = set(returned_sids[:5])
    top_10 = set(returned_sids[:10])
    r5 = len(top_5 & answer_sids) / len(answer_sids)
    r10 = len(top_10 & answer_sids) / len(answer_sids)
    rr = 0.0
    for i, sid in enumerate(returned_sids, start=1):
        if sid in answer_sids:
            rr = 1.0 / i
            break
    return {"r5": r5, "r10": r10, "rr": rr}


def generate_answer(openai: OpenAI, question: str, context: str,
                    question_date: str = "") -> tuple[str, float]:
    """Generate a short answer from retrieved context using mem0's exact prompt.

    LongMemEval supplies `question_date` per question - the date the question
    is asked. Surface it in the system message so the LLM can resolve relative
    references ("yesterday", "last week") against the right anchor.
    """
    prompt = ANSWER_PROMPT.format(question=question, context=context)
    system = ANSWER_SYSTEM
    if question_date:
        system = (f"Today's date is {question_date}. ") + system
    t1 = time.time()
    for attempt in range(3):
        try:
            resp = openai.chat.completions.create(
                model=ANSWER_MODEL,
                messages=[
                    {"role": "system", "content": system},
                    {"role": "user", "content": prompt},
                ],
                temperature=0,
            )
            return resp.choices[0].message.content.strip(), time.time() - t1
        except Exception as exc:
            if attempt == 2:
                print(f"  ! answer-gen failed: {exc}", file=sys.stderr)
                return "", time.time() - t1
            time.sleep(1)
    return "", time.time() - t1


def llm_judge(openai: OpenAI, question: str, gold: str, generated: str) -> int:
    """Binary CORRECT/WRONG using mem0's exact judge prompt. Returns 1 or 0."""
    prompt = ACCURACY_PROMPT.format(
        question=question, gold_answer=gold, generated_answer=generated
    )
    for attempt in range(3):
        try:
            resp = openai.chat.completions.create(
                model=JUDGE_MODEL,
                messages=[{"role": "user", "content": prompt}],
                response_format={"type": "json_object"},
                temperature=0.0,
            )
            content = resp.choices[0].message.content or ""
            # Best-effort JSON parse; some models leak prose around the JSON.
            try:
                data = json.loads(content)
            except json.JSONDecodeError:
                # Find the first {...} block and try again.
                start = content.find("{")
                end = content.rfind("}")
                if start >= 0 and end > start:
                    data = json.loads(content[start:end + 1])
                else:
                    return 0
            label = str(data.get("label", "")).strip().upper()
            return 1 if label == "CORRECT" else 0
        except Exception as exc:
            if attempt == 2:
                print(f"  ! judge failed: {exc}", file=sys.stderr)
                return 0
            time.sleep(1)
    return 0


def run_question(sage: httpx.Client, agent: SageAgent, openai: OpenAI,
                 question: dict[str, Any], top_k: int,
                 n_expansions: int = 0) -> dict[str, Any]:
    qid = question["question_id"]
    qtype = question.get("question_type", "unknown")
    domain = f"bench-lme-llm-{qid}"
    answer_sids = set(question.get("answer_session_ids", []) or [])
    gold_answer = str(question.get("answer", "") or "")
    haystack_ids = question.get("haystack_session_ids", []) or []
    haystack_sessions = question.get("haystack_sessions", []) or []
    haystack_dates = question.get("haystack_dates", []) or []
    question_date = str(question.get("question_date", "") or "")
    if len(haystack_ids) != len(haystack_sessions):
        return {"question_id": qid, "question_type": qtype,
                "error": "haystack id/session length mismatch"}
    # haystack_dates may be missing on older mirror dumps; pad with "" to keep zip aligned.
    if len(haystack_dates) < len(haystack_sessions):
        haystack_dates = list(haystack_dates) + [""] * (len(haystack_sessions) - len(haystack_dates))

    n_seeded = 0
    t_seed_start = time.time()
    for sid, session, sdate in zip(haystack_ids, haystack_sessions, haystack_dates):
        text = session_to_text(session, session_date=str(sdate or ""))
        if not text.strip():
            continue
        if submit_session(sage, agent, openai, domain, sid, text):
            n_seeded += 1
    seed_seconds = time.time() - t_seed_start

    t_q_start = time.time()
    results = hybrid_recall(sage, agent, openai, domain, question["question"],
                            top_k, n_expansions=n_expansions)
    query_seconds = time.time() - t_q_start

    returned_sids: list[str] = []
    retrieved_text: list[str] = []
    for r in results:
        content = r.get("content", "") or ""
        sid = extract_session_id(content)
        if sid is not None:
            returned_sids.append(sid)
        retrieved_text.append(strip_session_prefix(content))

    metrics = score(returned_sids, answer_sids)

    context = "\n<->\n".join(retrieved_text[:top_k])
    llm_answer, answer_seconds = generate_answer(openai, question["question"], context, question_date=question_date)
    llm_score = llm_judge(openai, question["question"], gold_answer, llm_answer) if gold_answer else 0

    return {
        "question_id": qid,
        "question_type": qtype,
        "n_seeded": n_seeded,
        "n_haystack": len(haystack_ids),
        "n_answer": len(answer_sids),
        "seed_seconds": round(seed_seconds, 2),
        "query_seconds": round(query_seconds, 2),
        "answer_seconds": round(answer_seconds, 2),
        "returned_sids": returned_sids,
        "gold_answer": gold_answer,
        "llm_answer": llm_answer,
        "llm_score": llm_score,
        **{k: round(v, 4) for k, v in metrics.items()},
    }


def load_dataset_local(path: Path) -> list[dict[str, Any]]:
    with path.open() as f:
        return json.load(f)


def load_dataset_hf() -> list[dict[str, Any]]:
    try:
        from datasets import load_dataset  # type: ignore
    except ImportError:
        sys.exit("Set LONGMEMEVAL_DATA_PATH or `pip install datasets`")
    return list(load_dataset("xiaowu0162/longmemeval-cleaned", split="train"))


def aggregate(per_q: list[dict[str, Any]]) -> dict[str, Any]:
    scored = [q for q in per_q if "r5" in q]
    if not scored:
        return {"n": 0}

    def mean(key: str, rows: list[dict[str, Any]]) -> float:
        vals = [r[key] for r in rows if key in r]
        return round(statistics.mean(vals), 4) if vals else 0.0

    by_type: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)
    for r in scored:
        by_type[r.get("question_type", "unknown")].append(r)

    overall = {
        "n": len(scored),
        "r5": mean("r5", scored),
        "r10": mean("r10", scored),
        "mrr": mean("rr", scored),
        "llm_score": mean("llm_score", scored),
    }
    per_type: dict[str, dict[str, Any]] = {}
    for t, rows in sorted(by_type.items()):
        per_type[t] = {
            "n": len(rows),
            "r5": mean("r5", rows),
            "r10": mean("r10", rows),
            "mrr": mean("rr", rows),
            "llm_score": mean("llm_score", rows),
        }
    return {"overall": overall, "per_type": per_type}


def git_sha() -> str:
    try:
        return subprocess.check_output(["git", "rev-parse", "--short", "HEAD"],
                                       stderr=subprocess.DEVNULL).decode().strip()
    except Exception:
        return "unknown"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--limit", type=int, default=0,
                        help="run only the first N questions (0 = all).")
    parser.add_argument("--per-type", type=int, default=0,
                        help="balanced cross-section: N questions per question_type.")
    parser.add_argument("--top-k", type=int, default=10)
    parser.add_argument("--expand", type=int, default=0,
                        help="LLM-generated query expansions per question.")
    parser.add_argument("--out", type=str, default=None)
    args = parser.parse_args()

    if not os.environ.get("OPENAI_API_KEY"):
        sys.exit("OPENAI_API_KEY not set in environment")

    data_path = os.environ.get("LONGMEMEVAL_DATA_PATH")
    if data_path:
        questions = load_dataset_local(Path(data_path))
        print(f"loaded {len(questions)} questions from {data_path}")
    else:
        questions = load_dataset_hf()
        print(f"loaded {len(questions)} questions from huggingface")

    if args.per_type > 0:
        by_type: dict[str, list[dict[str, Any]]] = collections.defaultdict(list)
        for q in questions:
            by_type[q.get("question_type", "unknown")].append(q)
        sampled: list[dict[str, Any]] = []
        for t in sorted(by_type):
            sampled.extend(by_type[t][: args.per_type])
        questions = sampled
        print(f"per-type sampling: {len(questions)} questions total")

    if args.limit > 0:
        questions = questions[: args.limit]
        print(f"limited to first {len(questions)} questions")

    openai_client = OpenAI()
    sage_client = httpx.Client(base_url=BASE_URL, timeout=60.0)
    agent = SageAgent()

    print(f"benchmarking {len(questions)} questions against {BASE_URL} (LLM-answer + judge)")

    per_q: list[dict[str, Any]] = []
    t_total = time.time()
    for i, q in enumerate(questions, start=1):
        try:
            row = run_question(sage_client, agent, openai_client, q,
                               args.top_k, n_expansions=args.expand)
        except KeyboardInterrupt:
            print("\ninterrupted - partial results will be written")
            break
        except Exception as exc:
            row = {
                "question_id": q.get("question_id", "?"),
                "question_type": q.get("question_type", "?"),
                "error": str(exc),
            }
        per_q.append(row)
        if "r5" in row:
            print(
                f"  [{i:4d}/{len(questions)}] {row['question_type'][:28]:28s} "
                f"r5={row['r5']:.2f} r10={row['r10']:.2f} llm={row['llm_score']} "
                f"q={row['query_seconds']}s",
                flush=True,
            )
        else:
            print(f"  [{i:4d}/{len(questions)}] ERROR: {row.get('error','?')}", flush=True)

    total_seconds = time.time() - t_total
    summary = aggregate(per_q)
    payload = {
        "git_sha": git_sha(),
        "sage_url": BASE_URL,
        "embed_model": EMBED_MODEL,
        "answer_model": ANSWER_MODEL,
        "judge_model": JUDGE_MODEL,
        "expand_n": args.expand,
        "top_k": args.top_k,
        "limit": args.limit,
        "n_total": len(per_q),
        "duration_seconds": round(total_seconds, 1),
        "summary": summary,
        "per_question": per_q,
    }

    out_path = args.out or f"bench/results/longmemeval-llm-{git_sha()}.json"
    out = Path(out_path)
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("w") as f:
        json.dump(payload, f, indent=2)

    print()
    print(f"wrote {out}")
    o = summary.get("overall", {})
    if o:
        print(f"OVERALL  n={o['n']}  R@5={o['r5']:.4f}  R@10={o['r10']:.4f}  "
              f"MRR={o['mrr']:.4f}  LLM-judge={o['llm_score']:.4f}")
        for t, m in summary.get("per_type", {}).items():
            print(f"  {t:30s} n={m['n']:4d}  R@5={m['r5']:.4f}  "
                  f"R@10={m['r10']:.4f}  LLM-judge={m['llm_score']:.4f}")
    print(f"total wall time: {total_seconds:.1f}s")
    return 0


if __name__ == "__main__":
    sys.exit(main())
