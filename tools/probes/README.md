# Probes

Headless acceptance probes used while developing Mynah. They are **not** part of a
deployment; each one drives a running cored (or a worker) through its real public
contract and prints PASS/FAIL.

| probe | what it verifies |
|---|---|
| `p1verify` | AvatarEngine gRPC contract against a worker |
| `p2probe` | cored media path end to end (pion WebRTC client, recvonly) |
| `p3probe` | voice input: sendrecv session, spoken question → spoken answer |
| `toolprobe` | host-page abilities: declares two abilities over the DataChannel, checks an action call lands mid-speech and a blocking call gets a filler + its result in the answer |
| `mixprobe` | mixed engine pool: a channel bound to an engine lands on that engine's worker |
| `capprobe` | pool capacity view from real GPU state |
| `svcprobe` | Docker supervisor service listing |
| `avatarprobe` | avatar preload broadcast: catalog → resolve → every worker |
| `qwen*probe`, `qwenragptt` | cloud-brain (qwen realtime) paths: greeting, echo, RAG injection, voice preview |

Build any of them with `go build ./tools/probes/<name>` and run with `-h` for flags.
