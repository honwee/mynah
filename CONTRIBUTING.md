# Contributing to Mynah

Thanks for your interest! This page gets you from clone to a working dev loop.

## Dev environment

The compose stack is the reference dev environment (NVIDIA GPU + Docker with
CDI required for the avatar engine):

```bash
cd deploy/compose
cp .env.example .env && $EDITOR .env     # GPU index, ports, voice
bash setup-flashhead.sh                  # engine code + weights
docker compose up -d --build
```

No GPU? You can still build and test the Go core (`go build ./... && go test
./...`) and run the training worker in simulate mode
(`PL_TRAIN_MODE=simulate python workers/avatartrain/server.py`).

Go 1.22+; Python workers target 3.10+. Regenerate gRPC stubs with `buf
generate` (config in `buf.gen.yaml`) — generated code lives in `gen/` and is
committed.

## Before you open a PR

- `go build ./... && go vet ./... && go test ./...` must pass (CI enforces).
- Python: `python -m compileall workers deploy/setup` must pass.
- Match the surrounding style — this codebase leans on package-level doc
  comments that explain *why*; keep them accurate when you change behavior.
- One logical change per PR. Include what you tested (the e2e path you drove,
  not just unit tests) in the description.

## Architecture orientation

Read the README's Architecture section first. The short version: `cored` (Go)
owns WebRTC + session pacing and talks to Python workers over gRPC contracts
in `proto/`. Feature extensions (training, voice clone, motion, bake, rerank)
mount through the `core.AdminExtension` seam — adding a new admin feature
usually means a new package under `internal/` plus a registration line in
`cmd/cored/main.go`, never edits to the control plane.

## Reporting issues

Use the issue templates. For anything security-sensitive, see
[SECURITY.md](SECURITY.md) — do not open a public issue.
