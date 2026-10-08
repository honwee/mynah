---
title: Knowledge base
---

# Knowledge base

Give the digital human your material and it answers from it. The pipeline is retrieval-augmented generation: documents are chunked, embedded, stored in Postgres with pgvector, and the best chunks are prepended to the model's context on every turn.

<img src="/screens/kb.png" alt="Knowledge base page" style="border:1px solid #e5e7eb;border-radius:8px">


## Set up

1. 配置中心 → RAG: point `embed_url` at an ollama-compatible `/api/embed` endpoint running `bge-m3` (1024 dimensions). The same ollama you may already use for the LLM works.
2. 知识库 → 新建知识库. The embedding model and dimension are locked into the knowledge base at creation, so later model swaps cannot silently corrupt it.
3. Upload documents: `.txt` and `.md`, up to 10 MB each. Ingestion is asynchronous: `pending → processing → ready` (or `failed` with the reason). Default chunking is 500 characters with 50 overlap; both are configurable.
4. Flip `rag.enabled` on. Done; the next conversation turn retrieves.

## Tune without guessing

**检索测试** runs a query against the live index and shows every returned chunk with its score and the latency. Try the questions your visitors actually ask, then adjust:

| Knob | Effect |
|---|---|
| `top_k` | how many chunks go into the prompt |
| `threshold` | minimum cosine similarity; raise it when unrelated chunks leak in |
| `chunk_size` / `chunk_overlap` | smaller chunks for FAQ-style material, larger for narrative manuals |
| `timeout_ms` | retrieval budget per turn; past it the turn proceeds without context rather than stalling |

An optional second-stage **reranker** (any standard rerank API) can reorder candidates before they reach the model; set `rerank_url` and `rerank_model`.

## Where it applies

Retrieval runs in the **local brain** pipeline (ASR → LLM → TTS). If you switch the conversation brain to **Qwen realtime** (speech-to-speech in the cloud) *and* use cloud turn detection, the cloud starts answering the moment it hears the question and there is no step where chunks can be inserted; cored logs a warning when a session is in that state. Keep turn detection local, or use the local brain, when the knowledge base matters.

## Lifecycle

- Re-upload a changed document and delete the old one; or `POST /api/v1/documents/{id}/reindex` after changing chunking.
- Channels do not freeze the knowledge base contents, only the RAG settings, so updating documents changes answers on live channels immediately.
- Everything lives in Postgres; back it up with `pg_dump`.

## What it does not do yet

Rich formats (PDF, DOCX, OCR) are not parsed by the open-source build; convert to Markdown first. A "questions visitors asked that had no good match" report is on the roadmap.
