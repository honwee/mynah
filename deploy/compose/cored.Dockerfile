# syntax=docker/dockerfile:1
# Mynah cored (Go realtime core). Multi-stage: build the static-ish binary
# with the Go toolchain, then ship it on a slim runtime. web/, assets/, tls/ are
# bind-mounted at runtime (see docker-compose.yml) so editing them needs no rebuild.
#
# Build context = mynah-turn repo root (so the Go source + gen/go are present).
FROM golang:1.22 AS build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct \
    GOSUMDB=off \
    GOTOOLCHAIN=local \
    CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/cored-turn ./cmd/cored

FROM debian:stable-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/cored-turn /usr/local/bin/cored-turn
ENTRYPOINT ["cored-turn"]
